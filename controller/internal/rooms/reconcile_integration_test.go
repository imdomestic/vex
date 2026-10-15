//go:build integration

package rooms_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

func TestRealRoomFailureIsolationRouteRecoveryAndCapacity(t *testing.T) {
	configFile, manifestFile := os.Getenv("CHORUS_IT_ROOMS_CONFIG"), os.Getenv("CHORUS_IT_ROOMS_BUILD")
	work, kubeconfig := os.Getenv("CHORUS_IT_ROOMS_WORK"), os.Getenv("CHORUS_IT_KUBECONFIG")
	assetArchive, assetMetadata := os.Getenv("CHORUS_IT_ASSET_ARCHIVE"), os.Getenv("CHORUS_IT_ASSET_METADATA")
	if configFile == "" || manifestFile == "" || work == "" || kubeconfig == "" || assetArchive == "" || assetMetadata == "" {
		t.Skip("requires real build, configuration, k3s and isolated room work directory")
	}
	if !strings.Contains(filepath.Clean(work), "/.work/round2-rooms") {
		t.Fatal("test storage must use the isolated project directory")
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
	root, err := os.MkdirTemp(work, "rooms-")
	if err != nil {
		t.Fatal(err)
	}
	if err = worldarchive.Space(root); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
	namespace := fmt.Sprintf("chorus-it-round2-%d", time.Now().UnixNano())
	cfg.Server.DataDir = filepath.Join(root, "data")
	cfg.Rooms.Namespace, cfg.Rooms.Kubeconfig = namespace, kubeconfig
	cfg.Rooms.MaxRunning = map[string]int{"encore": 1, "seraph": 0}
	cfg.Rooms.StartTimeout = "300s"
	cfg.Minecraft.Zone = namespace + ".example"
	cfg.Archive.Enabled = false
	listener, err := net.Listen("tcp", "10.42.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "http://" + listener.Addr().String()
	cfg.Server.InternalBaseURL, cfg.Server.PublicBaseURL = baseURL, baseURL
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret)
	users, err := toml.Marshal(struct {
		Users map[string]string `toml:"users"`
	}{map[string]string{"integration": token}})
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
			t.Fatal("real artifact differs from manifest", err)
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
		input.Close()
		closeErr := output.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatal(copyErr, closeErr)
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
		if !strings.HasPrefix(namespace, "chorus-it-") {
			t.Fatal("refusing namespace cleanup")
		}
		cleanup, done := context.WithTimeout(context.Background(), 4*time.Minute)
		defer done()
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
	labels := map[string]string{"app": "round2-router"}
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
	server := &http.Server{Handler: api.New(cfg, db, mirror, builds, manager, credentials, bus).Handler(), ReadHeaderTimeout: 10 * time.Second}
	defer server.Close()
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	room, _, err := manager.Create(ctx, "commit", manifest.SHA, manifest.Loader, "empty", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = manager.Start(ctx, room.ID, nil, auth.Identity{User: "integration"}); err != nil {
		t.Fatal(err)
	}
	bad := model.Room{ID: "a-error", Kind: "branch", State: "stopped", Label: "testing", Host: "a-error." + cfg.Minecraft.Zone}
	if err = db.Put(ctx, "room", bad.ID, bad); err != nil {
		t.Fatal(err)
	}
	if _, err = cluster.CoreV1().Services(namespace).Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "room-" + bad.ID}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 25565}}}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	check := func() model.Room {
		t.Helper()
		err := manager.Reconcile(ctx)
		if bad.ID != "" && (err == nil || !strings.Contains(err.Error(), bad.ID)) {
			t.Fatal("real ownership conflict was not reported")
		}
		if bad.ID == "" && err != nil {
			t.Fatal(err)
		}
		current, err := manager.Get(ctx, room.ID)
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	wait := func(condition func() bool) {
		t.Helper()
		for !condition() {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Second):
			}
		}
	}
	wait(func() bool {
		room = check()
		if room.State == "failed" {
			t.Fatal("real Minecraft startup failed")
		}
		return room.State == "ready"
	})
	fault, err := manager.Get(ctx, bad.ID)
	if err != nil || fault.LastError == nil || fault.LastError.Code != "internal_error" {
		t.Fatal("room-specific ownership error was not persisted", err)
	}
	t.Log("actual ownership conflict persisted while the following real Minecraft room reached ready")
	if err = db.Delete(ctx, "room", bad.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.Delete(ctx, "room_runtime", bad.ID); err != nil {
		t.Fatal(err)
	}
	if err = cluster.CoreV1().Services(namespace).Delete(ctx, "room-"+bad.ID, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	bad.ID = ""
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:1", time.Second); err == nil {
		conn.Close()
		t.Fatal("route fault port is unexpectedly listening")
	}
	manager.Cfg.Rooms.RouterAddress, manager.Cfg.Rooms.StartTimeout = "127.0.0.1:1", "6s"
	manager.RequestReconcile(room.Host)
	until := time.Now().Add(8 * time.Second)
	wait(func() bool {
		room = check()
		status, err := client.ObserveRoom(ctx, room.ID)
		if err != nil || !status.PodReady || room.State != "starting" || room.Phase != "route unavailable" {
			t.Fatal("routing failure stopped or failed a ready Pod", err)
		}
		return time.Now().After(until)
	})
	manager.Cfg.Rooms.RouterAddress = cfg.Rooms.RouterAddress
	wait(func() bool { room = check(); return room.State == "ready" })
	response, err := slp.Probe(ctx, cfg.Rooms.RouterAddress, room.Host, room.Port)
	if err != nil || !slp.Matches(response, room) {
		t.Fatal("real router did not recover the same room identity", err)
	}
	t.Log("actual unreachable route exceeded the startup deadline without stopping the Ready Pod; restored real router verified the same generation")
	claimName := "data-room-" + room.ID + "-0"
	claim, err := cluster.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, claimName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	workload, err := cluster.AppsV1().StatefulSets(namespace).Get(ctx, "room-"+room.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range workload.Spec.Template.Spec.Containers[0].Env {
		variable := &workload.Spec.Template.Spec.Containers[0].Env[i]
		if variable.Name == "JAVA_TOOL_OPTIONS" {
			variable.Value += " -XX:ChorusInvalidOption=1"
		}
	}
	if _, err = cluster.AppsV1().StatefulSets(namespace).Update(ctx, workload, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		pod, err := cluster.CoreV1().Pods(namespace).Get(ctx, "room-"+room.ID+"-0", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == "mc" && (status.State.Terminated != nil && status.State.Terminated.ExitCode != 0 || status.LastTerminationState.Terminated != nil && status.LastTerminationState.Terminated.ExitCode != 0) {
				return true
			}
		}
		return false
	})
	room = check()
	var runtime rooms.Runtime
	if err = db.Get(ctx, "room_runtime", room.ID, &runtime); err != nil || runtime.PodNotReadySince == nil {
		t.Fatal("real Pod failure did not start continuous unready timing", err)
	}
	wait(func() bool { room = check(); return room.State == "failed" })
	status, err := client.ObserveRoom(ctx, room.ID)
	if err != nil || status.PodExists {
		t.Fatal("failed room retained a Pod", err)
	}
	workload, err = cluster.AppsV1().StatefulSets(namespace).Get(ctx, "room-"+room.ID, metav1.GetOptions{})
	if err != nil || workload.Spec.Replicas == nil || *workload.Spec.Replicas != 0 {
		t.Fatal("failed workload was not scaled to zero", err)
	}
	retained, err := cluster.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, claimName, metav1.GetOptions{})
	if err != nil || retained.UID != claim.UID {
		t.Fatal("failed room lost its original PVC", err)
	}
	logs, err := manager.Log(ctx, room.ID)
	if err != nil || !strings.Contains(logs, "ChorusInvalidOption") {
		t.Fatal("actual JVM crash diagnostic was not retained", err)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/v1/rooms/"+room.ID+"/log", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	logResponse, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	logData, err := io.ReadAll(logResponse.Body)
	logResponse.Body.Close()
	if err != nil || logResponse.StatusCode != 200 || !strings.Contains(string(logData), "ChorusInvalidOption") {
		t.Fatal("real authenticated HTTP log did not retain the crash diagnostic", err)
	}
	t.Log("real JVM rejected its invalid option; failed followed Pod exit, replicas=0, retained PVC UID, and stored diagnostic through the actual HTTP handler")
	service, err := cluster.CoreV1().Services(namespace).Get(ctx, "room-"+room.ID, metav1.GetOptions{})
	if err != nil || service.Annotations["mc-router.itzg.me/externalServerName"] != room.Host {
		t.Fatal("failed room did not retain its original Service route", err)
	}
	previousServiceUID, previousServiceVersion := service.UID, service.ResourceVersion
	failedGeneration, failedBuild := room.Generation, room.BuildID
	manager.Cfg.Minecraft.Zone = namespace + ".changed.example"
	client, err = kube.New(manager.Cfg)
	if err != nil {
		t.Fatal(err)
	}
	manager.Kube = client
	room = check()
	expectedHost := room.ID + "." + manager.Cfg.Minecraft.Zone
	if room.State != "failed" || room.Host != expectedHost || room.Generation != failedGeneration || room.BuildID != failedBuild || room.LastError == nil || room.LastError.Code != "start_timeout" {
		t.Fatal("zone synchronization changed failed room identity or failure state")
	}
	service, err = cluster.CoreV1().Services(namespace).Get(ctx, "room-"+room.ID, metav1.GetOptions{})
	if err != nil || service.UID != previousServiceUID || service.ResourceVersion == previousServiceVersion || service.Annotations["mc-router.itzg.me/externalServerName"] != expectedHost {
		t.Fatal("zone synchronization did not update the existing failed room Service", err)
	}
	synchronizedVersion := service.ResourceVersion
	room = check()
	service, err = cluster.CoreV1().Services(namespace).Get(ctx, "room-"+room.ID, metav1.GetOptions{})
	if err != nil || service.ResourceVersion != synchronizedVersion {
		t.Fatal("repeated failed room reconciliation rewrote the unchanged Service", err)
	}
	status, err = client.ObserveRoom(ctx, room.ID)
	if err != nil || status.PodExists {
		t.Fatal("zone synchronization recreated a failed room Pod", err)
	}
	workload, err = cluster.AppsV1().StatefulSets(namespace).Get(ctx, "room-"+room.ID, metav1.GetOptions{})
	if err != nil || workload.Spec.Replicas == nil || *workload.Spec.Replicas != 0 {
		t.Fatal("zone synchronization changed failed room replicas", err)
	}
	retained, err = cluster.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, claimName, metav1.GetOptions{})
	if err != nil || retained.UID != claim.UID {
		t.Fatal("zone synchronization changed the retained world PVC", err)
	}
	t.Log("failed room synchronized its new zone into the existing Service; repeat kept resourceVersion stable, replicas zero, no Pod, and original PVC UID")
	metadataBytes, err := os.ReadFile(assetMetadata)
	if err != nil {
		t.Fatal(err)
	}
	var metadata model.AssetMetadata
	if err = json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatal(err)
	}
	if _, _, err = manager.Assets.Publish(ctx, metadata, assetArchive); err != nil {
		t.Fatal(err)
	}
	next, _, err := manager.Create(ctx, "asset", manifest.SHA, manifest.Loader, "", metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	manager.Cfg.Rooms.StartTimeout = "300s"
	if _, _, err = manager.Start(ctx, next.ID, nil, auth.Identity{User: "integration"}); err != nil {
		t.Fatal(err)
	}
	room = next
	wait(func() bool {
		room = check()
		if room.State == "failed" {
			t.Fatal("replacement room failed")
		}
		return room.State == "ready"
	})
	t.Logf("max_running=1 accepted and started the next real room after failure; namespace=%s data=%s", namespace, root)
	if _, err = client.RCON(ctx, room.ID, "save-all flush"); err != nil {
		t.Fatal(err)
	}
	if err = client.ScaleRoom(ctx, room.ID, 0); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		status, err := client.ObserveRoom(ctx, room.ID)
		if err != nil {
			t.Fatal(err)
		}
		return !status.PodExists
	})
}
