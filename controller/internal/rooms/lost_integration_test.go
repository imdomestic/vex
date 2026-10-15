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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/api"
	"github.com/imdomestic/chorus-testbed/controller/internal/archive"
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

func TestRealSeraphLostRecovery(t *testing.T) {
	if os.Getenv("CHORUS_IT_SERAPH_RECOVERY") != "1" {
		t.Skip("requires authorized seraph k3s stop/start and real Minecraft")
	}
	configFile, manifestFile := os.Getenv("CHORUS_IT_ROOMS_CONFIG"), os.Getenv("CHORUS_IT_ROOMS_BUILD")
	work, kubeconfig := os.Getenv("CHORUS_IT_ROOMS_WORK"), os.Getenv("CHORUS_IT_KUBECONFIG")
	if configFile == "" || manifestFile == "" || work == "" || kubeconfig == "" {
		t.Fatal("requires real configuration, ready build, k3s and isolated storage")
	}
	if !strings.Contains(filepath.Clean(work), "/.work/round2-rooms") {
		t.Fatal("storage must use the isolated project directory")
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
	root, err := os.MkdirTemp(work, "seraph-")
	if err != nil {
		t.Fatal(err)
	}
	if err = worldarchive.Space(root); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()
	namespace := fmt.Sprintf("chorus-it-seraph-%d", time.Now().UnixNano())
	cfg.Server.DataDir = filepath.Join(root, "data")
	cfg.Rooms.Namespace, cfg.Rooms.Kubeconfig = namespace, kubeconfig
	cfg.Rooms.MaxRunning = map[string]int{"encore": 2, "seraph": 4}
	cfg.Rooms.StartTimeout = "300s"
	cfg.Minecraft.Zone = namespace + ".example"
	cfg.Archive.Enabled = false
	listener, err := net.Listen("tcp", "10.42.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.InternalBaseURL = "http://" + listener.Addr().String()
	cfg.Server.PublicBaseURL = cfg.Server.InternalBaseURL
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
	labels := map[string]string{"app": "seraph-recovery-router"}
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
	wait := func(name string, duration time.Duration, check func() bool) {
		t.Helper()
		deadline := time.NewTimer(duration)
		defer deadline.Stop()
		for !check() {
			select {
			case err := <-served:
				t.Fatalf("actual API stopped: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-deadline.C:
				t.Fatalf("timed out waiting for %s", name)
			case <-time.After(time.Second):
			}
		}
	}
	reconcile := func() model.Room {
		t.Helper()
		if err := manager.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		current, err := manager.Get(ctx, room.ID)
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	wait("real commit room ready", 6*time.Minute, func() bool { return reconcile().State == "ready" })
	room = reconcile()
	if room.Node == nil || *room.Node != "seraph" {
		t.Fatalf("normal commit placement selected %v", room.Node)
	}
	pod, err := cluster.CoreV1().Pods(namespace).Get(ctx, "room-"+room.ID+"-0", metav1.GetOptions{})
	if err != nil || pod.Spec.NodeName != "seraph" {
		t.Fatal("actual Pod must run on seraph", err)
	}
	t.Logf("%s normal commit scheduling ready: namespace=%s room=%s generation=%d node=seraph pod=%s data=%s", time.Now().UTC().Format(time.RFC3339), namespace, room.ID, room.Generation, pod.UID, root)
	checkMarker := func(condition, marker string) {
		t.Helper()
		checking, finish := context.WithTimeout(ctx, 20*time.Second)
		defer finish()
		wait("Minecraft log marker "+marker, 20*time.Second, func() bool {
			if _, err := client.RCON(checking, room.ID, "execute if "+condition+" run say "+marker); err != nil {
				t.Fatal(err)
			}
			logs, err := client.Logs(checking, room.ID, 100)
			if err != nil {
				t.Fatal(err)
			}
			return strings.Contains(logs, "[Rcon] "+marker)
		})
		t.Logf("%s actual Minecraft log verified %s", time.Now().UTC().Format(time.RFC3339), marker)
	}
	checkBlock := func(phase string) {
		t.Helper()
		marker := namespace + "-" + phase
		checkMarker("block 0 90 0 minecraft:emerald_block", marker+"-present")
		if _, err := client.RCON(ctx, room.ID, "execute unless block 0 90 0 minecraft:emerald_block run say "+marker+"-missing"); err != nil {
			t.Fatal(err)
		}
		logs, err := client.Logs(ctx, room.ID, 100)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(logs, "[Rcon] "+marker+"-missing") {
			t.Fatal("Minecraft reported the saved block missing")
		}
	}
	if _, err = client.RCON(ctx, room.ID, "forceload add 0 0"); err != nil {
		t.Fatal(err)
	}
	checkMarker("loaded 0 90 0", namespace+"-chunk-loaded")
	if _, err = client.RCON(ctx, room.ID, "setblock 0 90 0 minecraft:emerald_block"); err != nil {
		t.Fatal(err)
	}
	checkBlock("initial-world")
	if _, err = client.RCON(ctx, room.ID, "save-all flush"); err != nil {
		t.Fatal(err)
	}
	remote := func(remoteCtx context.Context, args ...string) error {
		command := append([]string{"-u", "linwhite", "ssh", "-o", "BatchMode=yes", "-o", "ControlMaster=no", "-o", "ControlPath=none", "seraph", "sudo", "-n"}, args...)
		output, err := exec.CommandContext(remoteCtx, "sudo", command...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("seraph service operation failed: %w: %s", err, output)
		}
		return nil
	}
	guardPrefix := "chorus-it-seraph-recover-" + fmt.Sprint(time.Now().UnixNano())
	guard, guardArmed := "", false
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 60*time.Second)
		defer done()
		if err := remote(cleanup, "systemctl", "start", "k3s.service"); err != nil {
			t.Error(err)
			return
		}
		if guardArmed {
			if err := remote(cleanup, "systemctl", "stop", guard+".timer"); err != nil {
				t.Error(err)
			}
		}
	})
	for cycle := 1; cycle <= 2; cycle++ {
		guard = fmt.Sprintf("%s-%d", guardPrefix, cycle)
		if err := remote(ctx, "systemd-run", "--on-active=300", "--unit="+guard, "--setenv=TMPDIR=/home/linwhite/chorus-testbed/.work/temp", "/run/current-system/sw/bin/systemctl", "start", "k3s.service"); err != nil {
			t.Fatal(err)
		}
		guardArmed = true
		if err := remote(ctx, "systemctl", "stop", "k3s.service"); err != nil {
			t.Fatal(err)
		}
		stopped := time.Now()
		var unavailable time.Time
		wait("real Node unavailable and room lost", 3*time.Minute, func() bool {
			nodes, err := client.Nodes(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, node := range nodes {
				if node.Name == "seraph" && !node.Ready && unavailable.IsZero() {
					unavailable = time.Now()
				}
			}
			return reconcile().State == "lost"
		})
		var runtime rooms.Runtime
		if err := db.Get(ctx, "room_runtime", room.ID, &runtime); err != nil {
			t.Fatal(err)
		}
		if unavailable.IsZero() || runtime.NodeMissingSince == nil || time.Since(*runtime.NodeMissingSince) < 60*time.Second {
			t.Fatal("lost requires the persisted Node unavailability interval to reach 60 seconds")
		}
		actualNode, err := cluster.CoreV1().Nodes().Get(ctx, "seraph", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var nodeTransition time.Time
		for _, condition := range actualNode.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue {
				nodeTransition = condition.LastTransitionTime.Time
			}
		}
		if nodeTransition.IsZero() || time.Since(nodeTransition) < 60*time.Second {
			t.Fatal("actual Node Ready condition must remain unavailable for at least 60 seconds")
		}
		t.Logf("%s cycle=%d lost after k3s stop=%s persistedUnavailable=%s NodeNotReady=%s externalObservationOffset=%s", time.Now().UTC().Format(time.RFC3339), cycle, time.Since(stopped).Round(time.Millisecond), time.Since(*runtime.NodeMissingSince).Round(time.Millisecond), time.Since(nodeTransition).Round(time.Millisecond), unavailable.Sub(*runtime.NodeMissingSince).Round(time.Millisecond))
		if cycle == 2 {
			if err := client.ScaleRoom(ctx, room.ID, 0); err != nil {
				t.Fatal(err)
			}
		}
		if err := remote(ctx, "systemctl", "start", "k3s.service"); err != nil {
			t.Fatal(err)
		}
		wait("seraph Node Ready after k3s start", 90*time.Second, func() bool {
			nodes, err := client.Nodes(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, node := range nodes {
				if node.Name == "seraph" {
					return node.Ready
				}
			}
			return false
		})
		if cycle == 1 {
			wait("same real Pod ready", time.Minute, func() bool {
				status, err := client.ObserveRoom(ctx, room.ID)
				if err != nil {
					t.Fatal(err)
				}
				return status.Ready && status.Generation == room.Generation && status.BuildID == room.BuildID
			})
			current := reconcile()
			if current.State != "starting" && current.State != "ready" {
				t.Fatalf("ready matching Pod recovery state=%s", current.State)
			}
			wait("restored SLP ready", time.Minute, func() bool { return reconcile().State == "ready" })
			current = reconcile()
			response, err := slp.Probe(ctx, cfg.Rooms.RouterAddress, current.Host, current.Port)
			if err != nil || !slp.Matches(response, current) || current.Generation != room.Generation || current.BuildID != room.BuildID {
				t.Fatal("actual SLP must preserve room, generation and build identity", err)
			}
			checkBlock("recovered-world")
			currentPod, err := cluster.CoreV1().Pods(namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if err != nil || currentPod.UID != pod.UID {
				t.Fatal("recovery replaced the original Pod", err)
			}
			t.Logf("%s cycle=1 lost -> starting -> ready; same Pod/generation/build, real SLP and saved emerald verified", time.Now().UTC().Format(time.RFC3339))
		} else {
			wait("actual Minecraft Pod exit before lost recovery", 3*time.Minute, func() bool {
				status, err := client.ObserveRoom(ctx, room.ID)
				if err != nil {
					t.Fatal(err)
				}
				return !status.PodExists
			})
			current := reconcile()
			if current.State != "stopping" && current.State != "archiving" && current.State != "closed" {
				t.Fatalf("removed Pod recovery state=%s", current.State)
			}
			wait("normal stopped-room archive and closed", 4*time.Minute, func() bool { return reconcile().State == "closed" })
			snapshots, err := store.All[model.Snapshot](ctx, db, "snapshot")
			if err != nil || len(snapshots) != 1 || snapshots[0].RoomID != room.ID || snapshots[0].Size == 0 {
				t.Fatal("normal recovery stop must publish the real world snapshot", err)
			}
			t.Logf("%s cycle=2 lost -> stopping -> archiving -> closed; snapshot=%s bytes=%d", time.Now().UTC().Format(time.RFC3339), snapshots[0].ID, snapshots[0].Size)
		}
		if err := remote(ctx, "systemctl", "stop", guard+".timer"); err != nil {
			t.Fatal(err)
		}
		guardArmed = false
	}
}
