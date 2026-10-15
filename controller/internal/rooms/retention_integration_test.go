//go:build integration

package rooms_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/api"
	"github.com/imdomestic/chorus-testbed/controller/internal/archive"
	"github.com/imdomestic/chorus-testbed/controller/internal/assets"
	"github.com/imdomestic/chorus-testbed/controller/internal/auth"
	"github.com/imdomestic/chorus-testbed/controller/internal/build"
	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/events"
	"github.com/imdomestic/chorus-testbed/controller/internal/gitmirror"
	"github.com/imdomestic/chorus-testbed/controller/internal/kube"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/rooms"
	"github.com/imdomestic/chorus-testbed/controller/internal/slp"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
	"github.com/imdomestic/chorus-testbed/controller/internal/worldarchive"
	"github.com/pelletier/go-toml/v2"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func TestRealClosedRoomPruningPreservesGenerationThroughTankArchive(t *testing.T) {
	configFile, manifestFile := os.Getenv("CHORUS_IT_ROOMS_CONFIG"), os.Getenv("CHORUS_IT_ROOMS_BUILD")
	work, kubeconfig := os.Getenv("CHORUS_IT_RETENTION_WORK"), os.Getenv("CHORUS_IT_KUBECONFIG")
	tank, identity, knownHosts := os.Getenv("CHORUS_IT_TANK"), os.Getenv("CHORUS_IT_IDENTITY"), os.Getenv("CHORUS_IT_KNOWN_HOSTS")
	if configFile == "" || manifestFile == "" || work == "" || kubeconfig == "" || tank == "" || identity == "" || knownHosts == "" {
		t.Skip("requires real production build, k3s, isolated workspace and tank identity")
	}
	_, destination, ok := strings.Cut(tank, ":")
	if !ok || !strings.HasPrefix(destination, "/srv/archive/chorus/it-") || strings.Contains(strings.TrimPrefix(destination, "/srv/archive/chorus/it-"), "/") {
		t.Fatal("isolated tank test directory required")
	}
	if !strings.Contains(filepath.Clean(work), "/.work/round3/") {
		t.Fatal("isolated round3 workspace required")
	}
	cfg, err := config.Load(configFile)
	if err != nil {
		t.Fatal(err)
	}
	sourceData := cfg.Server.DataDir
	data, err := os.ReadFile(manifestFile)
	if err != nil {
		t.Fatal(err)
	}
	var manifest model.Build
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Status != "ready" || manifest.Minecraft == nil || *manifest.Minecraft != "26.3" {
		t.Fatal("requires a real ready Minecraft 26.3 build")
	}
	if err = os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(work, "retention-")
	if err != nil {
		t.Fatal(err)
	}
	if err = worldarchive.Space(root); err != nil {
		t.Fatal(err)
	}
	t.Logf("preserved evidence directory=%s tank=%s", root, tank)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	namespace := fmt.Sprintf("chorus-it-retention-%d", time.Now().UnixNano())
	cfg.Server.DataDir = filepath.Join(root, "data")
	cfg.Rooms.Namespace, cfg.Rooms.Kubeconfig = namespace, kubeconfig
	cfg.Rooms.MaxRunning = map[string]int{"encore": 1, "seraph": 0}
	cfg.Rooms.StartTimeout = "300s"
	cfg.Minecraft.Zone = namespace + ".example"
	cfg.Archive = config.Archive{Enabled: true, KeepPerRoom: 0, KeepDays: 0, TankTarget: tank, TankBWLimit: "5M", IdentityFile: identity, KnownHostsFile: knownHosts}
	listener, err := net.Listen("tcp", "10.42.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	baseURL := "http://" + listener.Addr().String()
	cfg.Server.InternalBaseURL, cfg.Server.PublicBaseURL = baseURL, baseURL
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	users, err := toml.Marshal(struct {
		Users map[string]string `toml:"users"`
	}{map[string]string{"integration": hex.EncodeToString(secret)}})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Auth.UsersFile, cfg.Auth.SigningKeyFile = filepath.Join(root, "users.toml"), filepath.Join(root, "signing.key")
	if err = os.WriteFile(cfg.Auth.UsersFile, users, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.Auth.SigningKeyFile, secret, 0600); err != nil {
		t.Fatal(err)
	}
	credentials, err := auth.Load(cfg.Auth.UsersFile, cfg.Auth.SigningKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, artifact := range manifest.Files {
		var relative string
		switch {
		case strings.HasPrefix(artifact.URL, "/v1/deps/"):
			relative = filepath.Join("deps", artifact.SHA256+".jar")
		case strings.HasPrefix(artifact.URL, "/v1/mods/chorus-hop/"):
			relative = filepath.Join("mods", "chorus-hop", *manifest.Minecraft, "published", artifact.SHA256+".jar")
		default:
			relative = filepath.Join("builds", manifest.ID, fmt.Sprintf("attempt-%d", manifest.Attempt), artifact.Name)
		}
		if filepath.IsAbs(relative) || strings.HasPrefix(filepath.Clean(relative), "..") {
			t.Fatal("unsafe artifact path")
		}
		source, destination := filepath.Join(sourceData, relative), filepath.Join(cfg.Server.DataDir, relative)
		digest, size, err := worldarchive.Digest(source)
		if err != nil || digest != artifact.SHA256 || size != artifact.Size {
			t.Fatal("real build artifact differs from manifest", err)
		}
		if err = os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			t.Fatal(err)
		}
		input, err := os.Open(source)
		if err != nil {
			t.Fatal(err)
		}
		output, err := os.Create(destination)
		if err != nil {
			input.Close()
			t.Fatal(err)
		}
		_, copyErr := io.Copy(output, input)
		inputErr, outputErr := input.Close(), output.Close()
		if err = errors.Join(copyErr, inputErr, outputErr); err != nil {
			t.Fatal(err)
		}
	}
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
	if _, err = cluster.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 4*time.Minute)
		defer done()
		if !strings.HasPrefix(namespace, "chorus-it-") {
			t.Fatal("refusing namespace cleanup")
		}
		if err := cluster.CoreV1().Namespaces().Delete(cleanup, namespace, metav1.DeleteOptions{}); err != nil {
			t.Error(err)
			return
		}
		for {
			_, err := cluster.CoreV1().Namespaces().Get(cleanup, namespace, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				t.Logf("deleted isolated namespace %s", namespace)
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
	if _, err = cluster.CoreV1().ServiceAccounts(namespace).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "mc-router"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "mc-router"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"services", "endpoints"}, Verbs: []string{"get", "list", "watch"}}, {APIGroups: []string{"discovery.k8s.io"}, Resources: []string{"endpointslices"}, Verbs: []string{"get", "list", "watch"}}}}
	if _, err = cluster.RbacV1().Roles(namespace).Create(ctx, role, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "mc-router"}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "mc-router", Namespace: namespace}}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "mc-router"}}
	if _, err = cluster.RbacV1().RoleBindings(namespace).Create(ctx, binding, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"app": "retention-router"}
	router := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "mc-router", Labels: labels}, Spec: corev1.PodSpec{ServiceAccountName: "mc-router", NodeSelector: map[string]string{"kubernetes.io/hostname": "encore"}, Containers: []corev1.Container{{Name: "router", Image: "itzg/mc-router:1.47.2@sha256:64ae69eaa7a6800a79bbf962f027f4020878efa25183359115bc7f6ee569c36e", Args: []string{"--in-kube-cluster", "--api-binding", ":8080"}, Env: []corev1.EnvVar{{Name: "KUBE_NAMESPACE", Value: namespace}, {Name: "AUTO_SCALE_UP", Value: "false"}, {Name: "AUTO_SCALE_DOWN", Value: "false"}, {Name: "TMPDIR", Value: "/work"}}, Ports: []corev1.ContainerPort{{Name: "minecraft", ContainerPort: 25565}}, VolumeMounts: []corev1.VolumeMount{{Name: "work", MountPath: "/work"}}}}, Volumes: []corev1.Volume{{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}}}
	if _, err = cluster.CoreV1().Pods(namespace).Create(ctx, router, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	routerService, err := cluster.CoreV1().Services(namespace).Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "mc-router"}, Spec: corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{{Name: "minecraft", Port: 25565, TargetPort: intstr.FromString("minecraft")}}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Rooms.RouterAddress = net.JoinHostPort(routerService.Spec.ClusterIP, "25565")
	mirror, err := gitmirror.New(cfg.Git.Remote, filepath.Join(sourceData, "git", "chorus.git"))
	if err != nil {
		t.Fatal(err)
	}
	builds, err := build.New(cfg, db, mirror)
	if err != nil {
		t.Fatal(err)
	}
	client, err := kube.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New(db)
	manager := rooms.New(cfg, db, mirror, builds, client, credentials, bus)
	manager.Archive, err = archive.New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	manager.Assets, err = assets.New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.InitializeRecords(ctx); err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: api.New(cfg, db, mirror, builds, manager, credentials, bus).Handler(), ReadHeaderTimeout: 10 * time.Second}
	defer server.Close()
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	wait := func(condition func() bool) {
		t.Helper()
		for !condition() {
			select {
			case err := <-served:
				t.Fatal("real HTTP handler stopped", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Second):
			}
		}
	}
	var roomID string
	for generation := int64(1); generation <= 2; generation++ {
		room, _, err := manager.Create(ctx, "commit", manifest.SHA, manifest.Loader, "empty", "")
		if err != nil {
			t.Fatal(err)
		}
		if generation == 2 && (room.ID != roomID || room.Generation != 1) {
			t.Fatal("recreated room did not recover its generation tombstone")
		}
		roomID = room.ID
		started, _, err := manager.Start(ctx, room.ID, nil, auth.Identity{User: "integration"})
		if err != nil || started.Room.Generation != generation {
			t.Fatal("unexpected new room generation", err)
		}
		wait(func() bool {
			if err = manager.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			room, err = manager.Get(ctx, roomID)
			if err != nil || room.State == "failed" {
				t.Fatal("real Minecraft startup failed", err)
			}
			return room.State == "ready"
		})
		response, err := slp.Probe(ctx, cfg.Rooms.RouterAddress, room.Host, room.Port)
		if err != nil || !slp.Matches(response, room) {
			t.Fatal("real router identity mismatch", err)
		}
		t.Logf("real Minecraft ready room=%s generation=%d build=%s", room.ID, room.Generation, room.BuildID)
		if _, _, err = manager.Stop(ctx, room.ID, false); err != nil {
			t.Fatal(err)
		}
		wait(func() bool {
			if err = manager.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			room, err = manager.Get(ctx, roomID)
			if err != nil || room.State == "failed" {
				t.Fatal("real Minecraft shutdown failed", err)
			}
			return room.State == "closed"
		})
		if room.ClosedAt == nil {
			t.Fatal("normal shutdown did not persist closedAt")
		}
		if err = manager.Archive.Step(ctx); err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("%s-g%d", roomID, generation)
		var snapshot model.Snapshot
		if err = db.Get(ctx, "snapshot", id, &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.TankVerifiedAt == nil || snapshot.RoomGeneration != generation || snapshot.BuildID != manifest.ID || snapshot.Size == 0 {
			t.Fatal("actual snapshot did not complete tank verification")
		}
		tasks, err := store.All[model.Transfer](ctx, db, "transfer")
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range tasks {
			if task.Status != "verified" || task.VerifiedAt == nil {
				t.Fatal("real tank transfer remains unverified")
			}
			t.Logf("preserved tank file=%s/%s sha256=%s size=%d", tank, task.RemotePath, task.SHA256, task.Size)
		}
		if generation == 1 {
			old := time.Now().UTC().Add(-31 * 24 * time.Hour)
			room.ClosedAt, snapshot.CapturedAt = &old, old
			if err = db.Update(ctx, func(tx *store.Tx) error {
				if err := tx.Put(ctx, "room", roomID, room); err != nil {
					return err
				}
				return tx.Put(ctx, "snapshot", snapshot.ID, snapshot)
			}); err != nil {
				t.Fatal(err)
			}
			if err = manager.PruneClosed(ctx); err != nil {
				t.Fatal(err)
			}
			var absent json.RawMessage
			for _, kind := range []string{"room", "room_runtime"} {
				if err = db.Get(ctx, kind, roomID, &absent); !errors.Is(err, store.ErrNotFound) {
					t.Fatal("expired room data was not pruned", kind, err)
				}
			}
			var tombstone struct {
				Generation int64 `json:"generation"`
			}
			if err = db.Get(ctx, "room_tombstone", roomID, &tombstone); err != nil || tombstone.Generation != 1 {
				t.Fatal("generation tombstone missing", err)
			}
			var preserved model.Snapshot
			if err = db.Get(ctx, "snapshot", id, &preserved); err != nil || preserved.SHA256 != snapshot.SHA256 || preserved.TankVerifiedAt == nil {
				t.Fatal("old verified snapshot record was not preserved", err)
			}
			t.Logf("pruned expired room/runtime; retained generation=%d tombstone and snapshot=%s", tombstone.Generation, preserved.ID)
		}
	}
	snapshots, err := store.All[model.Snapshot](ctx, db, "snapshot")
	if err != nil || len(snapshots) != 2 || snapshots[0].ID == snapshots[1].ID {
		t.Fatal("two distinct snapshot records required", err)
	}
	t.Logf("real successive snapshots retained: %s and %s", snapshots[0].ID, snapshots[1].ID)
}
