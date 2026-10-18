//go:build integration

package kubernetes_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	ns       string
	root     string
	baseURL  string
	token    string
	sources  *sync.Map
}

func newLifecycleHarness(t *testing.T, purpose string, tank bool) *lifecycleHarness {
	t.Helper()
	configFile, manifestFile := os.Getenv("VEX_IT_CONFIG"), os.Getenv("VEX_IT_BUILD")
	work, mirrorPath := os.Getenv("VEX_IT_WORK"), os.Getenv("VEX_IT_MIRROR")
	if purpose == "routeb" {
		manifestFile, mirrorPath = os.Getenv("VEX_IT_BUILD_B"), os.Getenv("VEX_IT_MIRROR_B")
		if manifestFile == "" || mirrorPath == "" {
			t.Fatal("second routed project requires its independently built ready manifest and actual mirror")
		}
	}
	runID, kubeconfig := os.Getenv("VEX_IT_RUN_ID"), os.Getenv("VEX_IT_KUBECONFIG")
	if configFile == "" || manifestFile == "" || work == "" || mirrorPath == "" || runID == "" || kubeconfig == "" {
		t.Skip("requires actual Vex config/build/mirror, k3s and isolated workspace")
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
	data, err := os.ReadFile(manifestFile)
	if err != nil {
		t.Fatal(err)
	}
	var manifest model.Build
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Status != "ready" || manifest.Minecraft == nil || manifest.Java == nil {
		t.Fatal("integration requires an actual ready build manifest")
	}
	source, err := global.ForProject(manifest.Project)
	if err != nil {
		t.Fatal(err)
	}
	project := global.Projects[manifest.Project]
	project.Members = map[string]config.Role{"integration": config.Owner}
	project.Quota.MaxRooms, project.Quota.SnapshotKeep, project.Quota.SnapshotKeepDays = 1, 0, 0
	global.Projects = map[string]config.Project{projectID: project}
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
		if purpose == "routea" || purpose == "routeb" {
			maxRooms = 2
		}
		global.Runtime.Kubernetes.Nodes = map[string]config.Node{"encore": {K8sName: "encore", Roles: []string{"persistent", "ephemeral"}, MaxRooms: maxRooms, Priority: 10}}
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
	global.Users = map[string]config.User{"integration": {TokenSHA256: hex.EncodeToString(tokenHash[:])}}
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
	for i := range manifest.Files {
		artifact := &manifest.Files[i]
		if !filepath.IsLocal(artifact.Path) || artifact.Path == "." {
			t.Fatal("actual manifest artifact path is not local")
		}
		original := filepath.Join(source.DataDir, filepath.FromSlash(artifact.Path))
		digest, size, err := worldarchive.Digest(original)
		if err != nil || digest != artifact.SHA256 || size != artifact.Size {
			t.Fatal("actual artifact differs from ready manifest", err)
		}
		destination := filepath.Join(cfg.DataDir, filepath.FromSlash(artifact.Path))
		if err = os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			t.Fatal(err)
		}
		input, err := os.Open(original)
		if err != nil {
			t.Fatal(err)
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			input.Close()
			t.Fatal(err)
		}
		_, copyErr := io.Copy(output, input)
		if err = errors.Join(copyErr, output.Close(), input.Close()); err != nil {
			t.Fatal(err)
		}
		artifact.URL = "/v2/projects/" + projectID + "/builds/" + manifest.ID + "/files/" + url.PathEscape(artifact.Name)
	}
	manifest.Project = projectID
	testTimeout := 18 * time.Minute
	if purpose == "seed" || purpose == "seedssh" {
		testTimeout = 40 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	if err = db.Put(ctx, "build", manifest.ID, manifest); err != nil {
		t.Fatal(err)
	}
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
	mirror, err := gitmirror.New(cfg.Git.Remote, mirrorPath)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := build.New(cfg, db, mirror)
	if err != nil {
		t.Fatal(err)
	}
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
	return &lifecycleHarness{t: t, ctx: ctx, cfg: cfg, manager: manager, backend: client, cluster: cluster, db: db, manifest: manifest, ns: ns, root: root, baseURL: baseURL, token: token, sources: sources}
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
