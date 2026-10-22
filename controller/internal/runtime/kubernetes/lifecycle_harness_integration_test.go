//go:build integration

package kubernetes_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/imdomestic/vex/controller/internal/api"
	"github.com/imdomestic/vex/controller/internal/archive"
	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/events"
	"github.com/imdomestic/vex/controller/internal/gitmirror"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/rooms"
	"github.com/imdomestic/vex/controller/internal/runtime"
	backend "github.com/imdomestic/vex/controller/internal/runtime/kubernetes"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/testproject"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
	"github.com/imdomestic/vex/controller/internal/worlds"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type lifecycleHarness struct {
	t        *testing.T
	ctx      context.Context
	cfg      config.ProjectConfig
	manager  *rooms.Manager
	backend  *backend.Client
	cluster  *kubernetes.Clientset
	db       *store.Store
	manifest model.Build
	project  *testproject.Project
	ns       string
	root     string
	baseURL  string
	token    string
	sources  *sync.Map
	// adminToken 属于平台管理员，只在测试项目中具有 viewer 角色。
	adminToken string
}

// newLifecycleHarness 建立独立的测试项目：自建 Git 远端、固定测试文件导入的 CI Build、独立 namespace 与数据目录。
// 运维配置只提供运行后端、节点与镜像设置；测试不读取任何实际项目的仓库、Build 或数据。
func newLifecycleHarness(t *testing.T, purpose string, tank bool) *lifecycleHarness {
	t.Helper()
	configFile, work := os.Getenv("VEX_IT_CONFIG"), os.Getenv("VEX_IT_WORK")
	runID, kubeconfig := os.Getenv("VEX_IT_RUN_ID"), os.Getenv("VEX_IT_KUBECONFIG")
	if configFile == "" || work == "" || runID == "" || kubeconfig == "" || os.Getenv("VEX_IT_FIXTURES") == "" {
		t.Skip("requires the Vex operator config, fixed test files, k3s and an isolated workspace")
	}
	if !filepath.IsAbs(work) || !strings.Contains(filepath.Clean(work), "/.work/") {
		t.Fatal("integration storage must be an absolute ignored workspace")
	}
	projectID := "vex-it-" + purpose + "-" + runID
	if !config.ValidID(projectID) {
		t.Fatal("integration project ID is invalid")
	}
	resolved, err := config.LoadDaemon(config.Options{Args: []string{"--config", configFile}, Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	global := resolved.Config
	if err = os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(work, purpose+"-")
	if err != nil {
		t.Fatal(err)
	}
	if err = worldarchive.Space(root); err != nil {
		t.Fatal(err)
	}
	repository, sha := testproject.New(t, filepath.Join(root, "project"))
	project := testproject.Configure(t, &global, repository.Remote, map[string]config.Role{"integration": config.Owner, "administrator": config.Viewer})
	project.Quota.MaxRooms, project.Quota.SnapshotKeep, project.Quota.SnapshotKeepDays = 1, 0, 0
	global.Projects = map[string]config.Project{projectID: project}
	global.Server.DataDir = filepath.Join(root, "data")
	global.Server.Zone = "integration.vex.internal"
	global.Rooms.StartTimeout = "300s"
	if purpose == "seed" || purpose == "seedssh" {
		global.Rooms.Idle.Commit = "10m"
	}
	global.Runtime.Kubernetes.KubeconfigFile = kubeconfig
	global.Runtime.Kubernetes.NamespacePrefix = "vex-p-"
	global.Runtime.Kubernetes.SystemNamespace = "vex-system"
	if purpose != "lost" {
		maxRooms := 1
		if purpose == "routea" || purpose == "routeb" || purpose == "wake" {
			maxRooms = 2
		}
		nodeID := os.Getenv("VEX_IT_NODE")
		if nodeID == "" {
			nodeID = "encore"
		}
		node, exists := global.Runtime.Kubernetes.Nodes[nodeID]
		if !exists || node.K8sName == "" {
			t.Fatal("integration node must exist in the actual operator configuration")
		}
		node.Roles, node.MaxRooms = []string{"persistent", "ephemeral"}, maxRooms
		global.Runtime.Kubernetes.Nodes = map[string]config.Node{nodeID: node}
	}
	global.Archive = config.Archive{Backend: "local", Local: config.LocalArchive{Directory: filepath.Join(root, "archive")}}
	if tank {
		target, identity, knownHosts := os.Getenv("VEX_IT_TANK"), os.Getenv("VEX_IT_IDENTITY"), os.Getenv("VEX_IT_KNOWN_HOSTS")
		_, destination, ok := strings.Cut(target, ":")
		if !ok || !strings.HasPrefix(destination, "/srv/archive/vex/it-") || strings.Contains(strings.TrimPrefix(destination, "/srv/archive/vex/it-"), "/") || identity == "" || knownHosts == "" {
			t.Fatal("real tank identity and a dedicated /srv/archive/vex/it-* target are required")
		}
		global.Archive = config.Archive{Backend: "rsync-ssh", RsyncSSH: config.RsyncSSH{Target: target, IdentityFile: identity, KnownHostsFile: knownHosts, BWLimit: "5M"}}
	}
	bind := os.Getenv("VEX_IT_HTTP_BIND")
	if bind == "" {
		t.Fatal("VEX_IT_HTTP_BIND must identify a real Pod-reachable local address")
	}
	listener, err := net.Listen("tcp", bind)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	baseURL := "http://" + listener.Addr().String()
	global.Server.InternalBaseURL, global.Server.PublicBaseURL = baseURL, baseURL
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret)
	tokenHash := sha256.Sum256([]byte(token))
	if _, err = rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	adminToken := hex.EncodeToString(secret)
	adminHash := sha256.Sum256([]byte(adminToken))
	global.Users = map[string]config.User{
		// 测试成员绑定一个随机 Minecraft UUID，供唤醒登录探测使用；不对应任何真实账号。
		"integration":   {TokenSHA256: hex.EncodeToString(tokenHash[:]), Minecraft: []string{uuid.NewString()}},
		"administrator": {TokenSHA256: hex.EncodeToString(adminHash[:]), Admin: true, Minecraft: []string{}},
	}
	global.Auth.SigningKeyFile = filepath.Join(root, "signing.key")
	if _, err = rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(global.Auth.SigningKeyFile, secret, 0600); err != nil {
		t.Fatal(err)
	}
	credentials, err := auth.New(global)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := global.ForProject(projectID)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Close() })
	db, err := owner.WithProject(projectID)
	if err != nil {
		t.Fatal(err)
	}
	testTimeout := 18 * time.Minute
	if purpose == "seed" || purpose == "seedssh" || purpose == "history" || purpose == "reset" {
		testTimeout = 40 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	rest, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	cluster, err := kubernetes.NewForConfig(rest)
	if err != nil {
		t.Fatal(err)
	}
	client, err := backend.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ns := "vex-p-" + projectID
	if _, err = cluster.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("integration namespace must not already exist", err)
	}
	setupErr := client.EnsureProject(ctx)
	namespace, err := cluster.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		t.Fatal("namespace setup failed", setupErr, err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 4*time.Minute)
		defer stop()
		if !strings.HasPrefix(ns, "vex-p-vex-it-") || ns != "vex-p-"+projectID {
			t.Fatal("refusing namespace cleanup")
		}
		if err := cluster.CoreV1().Namespaces().Delete(cleanup, ns, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &namespace.UID}}); err != nil {
			t.Error(err)
			return
		}
		for {
			_, err := cluster.CoreV1().Namespaces().Get(cleanup, ns, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				t.Logf("deleted isolated namespace %s; preserved local evidence %s", ns, root)
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			select {
			case <-cleanup.Done():
				t.Error(cleanup.Err())
				return
			case <-time.After(time.Second):
			}
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	mirror, err := gitmirror.New(cfg.Git.Remote, filepath.Join(root, "mirror.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = mirror.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	builder, err := build.New(cfg, db, mirror)
	if err != nil {
		t.Fatal(err)
	}
	manifest := testproject.ImportBuild(t, builder, sha)
	bus := events.New(owner, projectID)
	manager := rooms.New(cfg, db, mirror, builder, client, client, credentials, bus)
	manager.Worlds, err = worlds.New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	manager.Archive, err = archive.New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	manager.Archive.Worlds = manager.Worlds
	projectServer := &api.Server{Cfg: cfg, DB: db, Git: mirror, Builds: builder, Rooms: manager}
	application := api.New(global, map[string]*api.Server{projectID: projectServer}, credentials, bus)
	handler := application.Handler()
	sources := &sync.Map{}
	if purpose == "s1source" {
		handler = application.InternalHandler(func(ctx context.Context, address string) (bool, error) {
			allowed, err := client.RoomSource(ctx, address)
			if allowed && err == nil {
				pods, queryErr := cluster.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{FieldSelector: "status.podIP=" + address})
				if queryErr != nil {
					return false, queryErr
				}
				for _, pod := range pods.Items {
					sources.Store(address, pod.Labels["app.kubernetes.io/component"])
				}
			}
			return allowed, err
		})
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	t.Cleanup(func() { server.Close() })
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("actual API server: %v", err)
		}
	}()
	return &lifecycleHarness{t: t, ctx: ctx, cfg: cfg, manager: manager, backend: client, cluster: cluster, db: db, manifest: manifest, project: repository, ns: ns, root: root, baseURL: baseURL, token: token, adminToken: adminToken, sources: sources}
}

func (h *lifecycleHarness) wait(label string, timeout time.Duration, check func() bool) {
	h.t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for !check() {
		select {
		case <-h.ctx.Done():
			h.t.Fatal(h.ctx.Err())
		case <-deadline.C:
			h.t.Fatalf("timed out waiting for %s", label)
		case <-time.After(time.Second):
		}
	}
}

// request 以 token 调用测试服务的真实 API，返回 HTTP 状态与响应正文。
func (h *lifecycleHarness) request(token, method, route string, payload any) (int, []byte) {
	h.t.Helper()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			h.t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(h.ctx, method, h.baseURL+route, body)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal("actual project API request failed", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return response.StatusCode, data
}

// failure 断言 API 返回指定的 HTTP 状态与错误码。
func (h *lifecycleHarness) failure(token, method, route string, payload any, status int, code string) {
	h.t.Helper()
	actual, data := h.request(token, method, route, payload)
	var response struct {
		Error model.Error `json:"error"`
	}
	if err := json.Unmarshal(data, &response); err != nil || actual != status || response.Error.Code != code {
		h.t.Fatalf("%s %s returned %d %s, expected %d %s", method, route, actual, response.Error.Code, status, code)
	}
}

func (h *lifecycleHarness) current(id string) model.Room {
	h.t.Helper()
	if err := h.manager.Reconcile(h.ctx); err != nil {
		h.t.Fatal(err)
	}
	room, err := h.manager.Get(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return room
}

func (h *lifecycleHarness) ref(id string) runtime.ResourceRef {
	h.t.Helper()
	var rt rooms.Runtime
	if err := h.db.Get(h.ctx, "room_runtime", id, &rt); err != nil || rt.Resource.ID == "" {
		h.t.Fatal("room has no persisted resource reference", err)
	}
	return rt.Resource
}

func (h *lifecycleHarness) startCommit() model.Room {
	h.t.Helper()
	room, _, err := h.manager.Create(h.ctx, "commit", h.manifest.SHA, h.manifest.Loader, "empty", "")
	if err != nil {
		h.t.Fatal(err)
	}
	if _, _, err = h.manager.Start(h.ctx, room.ID, nil, auth.Identity{User: "integration"}); err != nil {
		h.t.Fatal(err)
	}
	h.wait("actual Minecraft ready", 6*time.Minute, func() bool {
		room = h.current(room.ID)
		if room.State == "failed" {
			h.t.Fatal("actual Minecraft startup failed")
		}
		return room.State == "ready"
	})
	return room
}

func (h *lifecycleHarness) stop(id string) model.Room {
	h.t.Helper()
	if _, _, err := h.manager.Stop(h.ctx, id, false); err != nil {
		h.t.Fatal(err)
	}
	var room model.Room
	h.wait("actual world archive", 5*time.Minute, func() bool {
		room = h.current(id)
		return room.State == "closed" || room.State == "stopped"
	})
	return room
}

func (h *lifecycleHarness) rcon(id, command string) string {
	h.t.Helper()
	output, err := h.backend.RCON(h.ctx, h.ref(id), command)
	if err != nil {
		h.t.Fatal("actual Minecraft control command failed", err)
	}
	return output
}

func (h *lifecycleHarness) logs(id string) string {
	h.t.Helper()
	output, err := h.backend.Logs(h.ctx, h.ref(id), runtime.LogOptions{Tail: 200})
	if err != nil {
		h.t.Fatal(err)
	}
	return output
}
