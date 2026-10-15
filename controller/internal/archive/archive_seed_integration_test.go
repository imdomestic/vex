//go:build integration

package archive_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func realPackCLI(t *testing.T, ctx context.Context, root, baseURL, usersFile, minecraft string) {
	t.Helper()
	binary, versionFile := os.Getenv("CHORUS_IT_CHORUSCTL"), os.Getenv("CHORUS_IT_GAME_VERSION")
	if binary == "" || versionFile == "" {
		t.Fatal("pack CLI check requires the compiled CLI and recorded official Minecraft version metadata")
	}
	data, err := os.ReadFile(versionFile)
	if err != nil {
		t.Fatal(err)
	}
	var version struct {
		ID   string `json:"id"`
		Pack struct {
			Major int `json:"resource_major"`
			Minor int `json:"resource_minor"`
		} `json:"pack_version"`
	}
	if err = json.Unmarshal(data, &version); err != nil {
		t.Fatal(err)
	}
	if version.ID != minecraft || version.Pack.Major <= 0 {
		t.Fatal("recorded Minecraft resource-pack format differs from the real build")
	}
	metadata, err := json.Marshal(map[string]any{"pack": map[string]any{"description": "Chorus API integration", "min_format": []int{version.Pack.Major, version.Pack.Minor}, "max_format": []int{version.Pack.Major, version.Pack.Minor}}})
	if err != nil {
		t.Fatal(err)
	}
	translation, err := json.Marshal(map[string]string{"chorus.integration": "Chorus integration"})
	if err != nil {
		t.Fatal(err)
	}
	packPath := filepath.Join(root, "api-resource-pack.zip")
	file, err := os.Create(packPath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, entry := range []struct {
		Name string
		Data []byte
	}{{"pack.mcmeta", metadata}, {"assets/chorus/lang/en_us.json", translation}} {
		part, err := writer.Create(entry.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(entry.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	bytesOnDisk, err := os.ReadFile(packPath)
	if err != nil {
		t.Fatal(err)
	}
	digest, size, err := worldarchive.Digest(packPath)
	if err != nil {
		t.Fatal(err)
	}
	legacy := fmt.Sprintf("%x", sha1.Sum(bytesOnDisk))
	run := func(arguments ...string) []byte {
		t.Helper()
		args := append([]string{"--url", baseURL, "--users-file", usersFile, "--user", "integration"}, arguments...)
		command := exec.CommandContext(ctx, binary, args...)
		var output, diagnostic bytes.Buffer
		command.Stdout, command.Stderr = &output, &diagnostic
		if err := command.Run(); err != nil {
			t.Fatalf("real pack CLI failed: %v: %s", err, diagnostic.String())
		}
		return output.Bytes()
	}
	var first, repeated model.PackRevision
	if err = json.Unmarshal(run("pack", "publish", "p6-cli", "--file", packPath), &first); err != nil {
		t.Fatal(err)
	}
	if first.ID != "p6-cli" || first.Rev != digest || first.SHA256 != digest || first.SHA1 != legacy || first.Size != size {
		t.Fatal("real CLI publication metadata differs from actual ZIP bytes")
	}
	if err = json.Unmarshal(run("pack", "publish", "p6-cli", "--file", packPath), &repeated); err != nil {
		t.Fatal(err)
	}
	if repeated != first {
		t.Fatal("identical CLI upload changed the published revision")
	}
	if !bytes.Equal(run("request", "GET", first.URL), bytesOnDisk) {
		t.Fatal("real CLI download differs from the uploaded ZIP")
	}
	t.Logf("real pack CLI upload, SHA256/SHA1/size, repeated revision and downloaded bytes verified: sha256=%s sha1=%s size=%d", digest, legacy, size)
}

func copiedRealFile(t *testing.T, source, destination, digest string, size int64) {
	t.Helper()
	actual, actualSize, err := worldarchive.Digest(source)
	if err != nil || actual != digest || actualSize != size {
		t.Fatal("source file differs from real published metadata", err)
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(output, input)
	closeErr := output.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func realSnapshotRecord(t *testing.T, database, id string) model.Snapshot {
	t.Helper()
	u := &url.URL{Scheme: "file", Path: database, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var document []byte
	if err = db.QueryRow("SELECT data FROM records WHERE kind='snapshot' AND id=?", id).Scan(&document); err != nil {
		t.Fatal(err)
	}
	var snapshot model.Snapshot
	if err = json.Unmarshal(document, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.TankVerifiedAt == nil {
		t.Fatal("source snapshot has no actual tank verification")
	}
	return snapshot
}

func TestRealTankSeedStartsMinecraftThroughRouter(t *testing.T) {
	sourceConfig, manifestFile := os.Getenv("CHORUS_IT_SEED_CONFIG"), os.Getenv("CHORUS_IT_SEED_BUILD")
	snapshotDB, snapshotID := os.Getenv("CHORUS_IT_SEED_DATABASE"), os.Getenv("CHORUS_IT_SEED_SNAPSHOT")
	tank, identity, knownHosts := os.Getenv("CHORUS_IT_TANK"), os.Getenv("CHORUS_IT_IDENTITY"), os.Getenv("CHORUS_IT_KNOWN_HOSTS")
	probe, work := os.Getenv("CHORUS_IT_WORLD_PROBE"), os.Getenv("CHORUS_IT_ARCHIVE_WORK")
	kubeconfig := os.Getenv("CHORUS_IT_KUBECONFIG")
	if sourceConfig == "" || manifestFile == "" || snapshotDB == "" || snapshotID == "" || tank == "" || identity == "" || knownHosts == "" || probe == "" || work == "" || kubeconfig == "" {
		t.Skip("requires explicit real seed, build, kubeconfig, isolated tank source and known world block probe")
	}
	if !strings.Contains(tank, ":/srv/archive/chorus/it-") || !strings.Contains(filepath.Clean(work), "/.work/p6-") {
		t.Fatal("seed test requires isolated test storage")
	}
	if !strings.HasPrefix(probe, "execute ") || !strings.HasSuffix(probe, " run say chorus-p6-world-ok") {
		t.Fatal("world probe must test an observed block and emit the test marker")
	}
	cfg, err := config.Load(sourceConfig)
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
		t.Fatal("requires real ready Minecraft 26.3 build")
	}
	snapshot := realSnapshotRecord(t, snapshotDB, snapshotID)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	if err = os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(work, "seed-")
	if err != nil {
		t.Fatal(err)
	}
	if err = worldarchive.Space(root); err != nil {
		t.Fatal(err)
	}
	namespace := fmt.Sprintf("chorus-it-p6-%d", time.Now().Unix())
	cfg.Server.DataDir = filepath.Join(root, "data")
	if err = os.MkdirAll(filepath.Join(cfg.Server.DataDir, ".work"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Rooms.Namespace, cfg.Rooms.Kubeconfig = namespace, kubeconfig
	cfg.Rooms.MaxRunning = map[string]int{"encore": 1, "seraph": 0}
	cfg.Minecraft.Zone = namespace + ".100-64-0-43.sslip.io"
	cfg.Archive = config.Archive{Enabled: true, KeepPerRoom: 5, KeepDays: 14, TankTarget: tank, TankBWLimit: "16M", IdentityFile: identity, KnownHostsFile: knownHosts}
	listener, err := net.Listen("tcp", "10.42.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	server.Listener.Close()
	server.Listener = listener
	defer server.Close()
	cfg.Server.InternalBaseURL, cfg.Server.PublicBaseURL = "http://"+listener.Addr().String(), "http://"+listener.Addr().String()
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	userToken := hex.EncodeToString(secret)
	users, err := toml.Marshal(struct {
		Users map[string]string `toml:"users"`
	}{Users: map[string]string{"integration": userToken}})
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
	for _, file := range manifest.Files {
		var relative string
		switch {
		case strings.HasPrefix(file.URL, "/v1/deps/"):
			relative = filepath.Join("deps", file.SHA256+".jar")
		case strings.HasPrefix(file.URL, "/v1/mods/chorus-hop/"):
			if file.URL != "/v1/mods/chorus-hop/"+url.PathEscape(*manifest.Minecraft)+"/"+file.SHA256+".jar" {
				t.Fatal("hop artifact URL differs from immutable digest")
			}
			relative = filepath.Join("mods", "chorus-hop", *manifest.Minecraft, "published", file.SHA256+".jar")
		default:
			relative = filepath.Join("builds", manifest.ID, fmt.Sprintf("attempt-%d", manifest.Attempt), file.Name)
		}
		if filepath.IsAbs(relative) || strings.HasPrefix(filepath.Clean(relative), "..") {
			t.Fatal("unsafe published artifact path")
		}
		copiedRealFile(t, filepath.Join(sourceData, relative), filepath.Join(cfg.Server.DataDir, relative), file.SHA256, file.Size)
	}
	if err = db.Put(ctx, "build", manifest.ID, manifest); err != nil {
		t.Fatal(err)
	}
	snapshot.LocalAvailable = false
	if err = db.Put(ctx, "snapshot", snapshot.ID, snapshot); err != nil {
		t.Fatal(err)
	}
	rest, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	kubernetesClient, err := kubernetes.NewForConfig(rest)
	if err != nil {
		t.Fatal(err)
	}
	_, err = kubernetesClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !strings.HasPrefix(namespace, "chorus-it-") {
			t.Fatal("refusing namespace cleanup")
		}
		cleanup, done := context.WithTimeout(context.Background(), 240*time.Second)
		defer done()
		if err := kubernetesClient.CoreV1().Namespaces().Delete(cleanup, namespace, metav1.DeleteOptions{}); err != nil {
			t.Error(err)
			return
		}
		for {
			_, err := kubernetesClient.CoreV1().Namespaces().Get(cleanup, namespace, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				t.Logf("deleted test namespace %s", namespace)
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
	_, err = kubernetesClient.CoreV1().ServiceAccounts(namespace).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "mc-router"}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "mc-router"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"services", "endpoints"}, Verbs: []string{"get", "list", "watch"}}, {APIGroups: []string{"discovery.k8s.io"}, Resources: []string{"endpointslices"}, Verbs: []string{"get", "list", "watch"}}}}
	if _, err = kubernetesClient.RbacV1().Roles(namespace).Create(ctx, role, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "mc-router"}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "mc-router", Namespace: namespace}}, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "mc-router"}}
	if _, err = kubernetesClient.RbacV1().RoleBindings(namespace).Create(ctx, binding, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"app": "chorus-it-router"}
	router := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "mc-router", Labels: labels}, Spec: corev1.PodSpec{ServiceAccountName: "mc-router", NodeSelector: map[string]string{"kubernetes.io/hostname": "encore"}, Containers: []corev1.Container{{Name: "router", Image: "itzg/mc-router:1.47.2@sha256:64ae69eaa7a6800a79bbf962f027f4020878efa25183359115bc7f6ee569c36e", Args: []string{"--in-kube-cluster", "--api-binding", ":8080"}, Env: []corev1.EnvVar{{Name: "KUBE_NAMESPACE", Value: namespace}, {Name: "AUTO_SCALE_UP", Value: "false"}, {Name: "AUTO_SCALE_DOWN", Value: "false"}, {Name: "TMPDIR", Value: "/work"}}, Ports: []corev1.ContainerPort{{Name: "minecraft", ContainerPort: 25565}}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")}, Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")}}, VolumeMounts: []corev1.VolumeMount{{Name: "work", MountPath: "/work"}}}}, Volumes: []corev1.Volume{{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}}}
	if _, err = kubernetesClient.CoreV1().Pods(namespace).Create(ctx, router, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	routerService, err := kubernetesClient.CoreV1().Services(namespace).Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "mc-router"}, Spec: corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{{Name: "minecraft", Port: 25565, TargetPort: intstr.FromString("minecraft")}}}}, metav1.CreateOptions{})
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
	kubeClient, err := kube.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New(db)
	manager := rooms.New(cfg, db, mirror, builds, kubeClient, credentials, bus)
	manager.Archive, err = archive.New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	manager.Assets, err = assets.New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	manager.Archive.Assets = manager.Assets
	server.Config.Handler = api.New(cfg, db, mirror, builds, manager, credentials, bus).Handler()
	server.Start()
	request := func(method, route string, payload any, output any, expected int) {
		t.Helper()
		var body io.Reader
		if payload != nil {
			data, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.NewReader(data)
		}
		req, err := http.NewRequestWithContext(ctx, method, server.URL+route, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+userToken)
		req.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != expected {
			t.Fatalf("real handler %s %s status=%d expected=%d", method, route, response.StatusCode, expected)
		}
		if output != nil {
			if err := json.NewDecoder(response.Body).Decode(output); err != nil {
				t.Fatal(err)
			}
		}
	}
	var room model.Room
	request("POST", "/v1/rooms", map[string]string{"kind": "commit", "sha": manifest.SHA, "loader": manifest.Loader, "seed": "snapshot:" + snapshot.ID}, &room, http.StatusCreated)
	var started model.StartResult
	request("POST", "/v1/rooms/"+room.ID+"/start", nil, &started, http.StatusAccepted)
	if started.Room.Phase != "restoring seed" || started.Room.SeedResolved == nil || started.Room.SeedResolved.ID != snapshot.ID {
		t.Fatal("tank-only seed did not enter restoration")
	}
	t.Logf("real handler accepted tank seed: namespace=%s room=%s generation=%d build=%s", namespace, room.ID, started.Room.Generation, started.Room.BuildID)
	if err = manager.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err = manager.Archive.Step(ctx); err != nil {
		t.Fatal(err)
	}
	var restored model.Snapshot
	if err = db.Get(ctx, "snapshot", snapshot.ID, &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.LocalAvailable || restored.Restore == nil || restored.Restore.Status != "succeeded" {
		t.Fatal("automatic seed restoration failed")
	}
	for {
		if err = manager.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		room, err = manager.Get(ctx, room.ID)
		if err != nil {
			t.Fatal(err)
		}
		if room.State == "ready" {
			break
		}
		if room.State == "failed" {
			t.Fatalf("Minecraft seed startup failed: %v", room.LastError)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Minecraft startup: state=%s phase=%s %v", room.State, room.Phase, ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	status, err := kubeClient.ObserveRoom(ctx, room.ID)
	if err != nil || !status.Ready || status.Generation != room.Generation || status.BuildID != room.BuildID {
		t.Fatal("real Pod identity was not ready", err)
	}
	response, err := slp.Probe(ctx, cfg.Rooms.RouterAddress, room.Host, room.Port)
	if err != nil || !slp.Matches(response, room) {
		t.Fatal("router SLP room/build/generation mismatch", err)
	}
	if _, err = kubeClient.RCON(ctx, room.ID, "forceload add 0 0"); err != nil {
		t.Fatal(err)
	}
	checkMarker := func(command, marker string) {
		t.Helper()
		checking, finish := context.WithTimeout(ctx, 20*time.Second)
		defer finish()
		for {
			if _, err := kubeClient.RCON(checking, room.ID, command); err != nil {
				t.Fatal(err)
			}
			logs, err := kubeClient.Logs(checking, room.ID, 100)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(logs, marker) {
				t.Logf("actual Minecraft log contains %s", marker)
				return
			}
			select {
			case <-checking.Done():
				t.Fatalf("Minecraft log marker %s missing: %v", marker, checking.Err())
			case <-time.After(time.Second):
			}
		}
	}
	checkMarker("execute if loaded 0 -39 0 run say chorus-p6-chunk-loaded", "chorus-p6-chunk-loaded")
	checkMarker(probe, "chorus-p6-world-ok")
	if _, err = kubeClient.RCON(ctx, room.ID, "forceload remove 0 0"); err != nil {
		t.Fatal(err)
	}
	if _, err = kubeClient.RCON(ctx, room.ID, "save-all flush"); err != nil {
		t.Fatal(err)
	}
	if err = kubeClient.ScaleRoom(ctx, room.ID, 0); err != nil {
		t.Fatal(err)
	}
	realPackCLI(t, ctx, root, server.URL, cfg.Auth.UsersFile, *manifest.Minecraft)
	t.Logf("actual Minecraft restored seed passed Pod Ready, router room/build/generation SLP and RCON block check; local=%s", root)
}
