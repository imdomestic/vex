//go:build integration

package kubernetes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/runtime"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

func integrationClient(t *testing.T) *Client {
	t.Helper()
	kubeconfig, runID := os.Getenv("VEX_IT_KUBECONFIG"), os.Getenv("VEX_IT_RUN_ID")
	if kubeconfig == "" || runID == "" {
		t.Skip("actual component tests require kubeconfig and a dedicated run ID")
	}
	purpose := map[string]string{"TestActualEnsureRoomStableResourceVersions": "stable", "TestActualFailureContainerLogs": "logs", "TestActualKubernetesRoomObjects": "objects"}[t.Name()]
	return integrationClientWithNamespace(t, purpose, "vex-p-", "vex-system")
}

func integrationClientWithNamespace(t *testing.T, purpose, prefix, systemNamespace string) *Client {
	t.Helper()
	kubeconfig, runID := os.Getenv("VEX_IT_KUBECONFIG"), os.Getenv("VEX_IT_RUN_ID")
	if kubeconfig == "" || runID == "" {
		t.Skip("actual component tests require kubeconfig and a dedicated run ID")
	}
	project := "vex-it-" + purpose + "-" + runID
	if purpose == "" || !config.ValidID(project) {
		t.Fatal("component test project must have a known purpose and valid run ID")
	}
	cfg := config.ProjectConfig{
		ID: project, DataDir: t.TempDir(),
		Server:    config.Server{InternalBaseURL: "http://10.1.2.137:8790", Zone: "vex.internal"},
		Minecraft: config.ProjectMinecraft{Target: config.Target{Version: "26.3", Java: 25}, AcceptEULA: true, Image: "itzg/minecraft-server:java25@sha256:7462fe34a56686719463ff898a674c1a1ce802988138d2bfdeff2507fb1644e8", Whitelist: []string{"linWhite"}},
		Runtimes:  map[string]config.JavaRuntime{"java25": {ServerImage: "itzg/minecraft-server:java25@sha256:7462fe34a56686719463ff898a674c1a1ce802988138d2bfdeff2507fb1644e8"}},
		Rooms:     config.EffectiveRooms{Heap: "4G", MemoryLimit: "5Gi", MaxRooms: 2},
		Quota:     config.Quota{MaxRooms: 2},
		Runtime:   config.Runtime{Backend: "kubernetes", Kubernetes: config.Kubernetes{NamespacePrefix: prefix, SystemNamespace: systemNamespace, KubeconfigFile: kubeconfig, StorageClass: "local-path", Nodes: map[string]config.Node{"encore": {K8sName: "encore", Roles: []string{"persistent", "ephemeral"}, MaxRooms: 2, Priority: 10}}}},
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err = c.api.CoreV1().Namespaces().Get(ctx, c.ns, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("component test namespace must not already exist", err)
	}
	setupErr := c.EnsureProject(ctx)
	namespace, err := c.api.CoreV1().Namespaces().Get(ctx, c.ns, metav1.GetOptions{})
	if err != nil {
		t.Fatal("namespace setup failed", setupErr, err)
	}
	t.Cleanup(func() {
		if !strings.HasPrefix(c.ns, "vex-p-vex-it-") || c.ns != prefix+project {
			t.Fatal("refusing namespace cleanup")
		}
		cleanup, stop := context.WithTimeout(context.Background(), 90*time.Second)
		defer stop()
		if err := c.api.CoreV1().Namespaces().Delete(cleanup, c.ns, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &namespace.UID}}); err != nil {
			t.Error(err)
			return
		}
		err := wait.PollUntilContextCancel(cleanup, time.Second, true, func(ctx context.Context) (bool, error) {
			_, err := c.api.CoreV1().Namespaces().Get(ctx, c.ns, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		})
		if err != nil {
			t.Error(err)
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	return c
}

func integrationRoomSpec(t *testing.T, c *Client) RoomSpec {
	t.Helper()
	version, loader, java := "26.3", "0.19.5", 25
	credential := make([]byte, 32)
	if _, err := rand.Read(credential); err != nil {
		t.Fatal(err)
	}
	return RoomSpec{
		Room:  model.Room{Project: c.cfg.ID, ID: "main-fabric", Kind: "branch", Loader: "fabric", BuildID: "fabric-fc16eaac252b", Generation: 1},
		Build: model.Build{Project: c.cfg.ID, ID: "fabric-fc16eaac252b", Loader: "fabric", Status: "ready", Minecraft: &version, LoaderVersion: &loader, Java: &java},
		Node:  "encore", RoomToken: hex.EncodeToString(credential), RCONPassword: hex.EncodeToString(credential[:16]),
		Whitelist: c.cfg.Minecraft.Whitelist, EnforceWhitelist: true,
	}
}

type integrationObjects struct {
	secret   *corev1.Secret
	service  *corev1.Service
	workload *appsv1.StatefulSet
}

func readIntegrationObjects(t *testing.T, ctx context.Context, c *Client, id string) integrationObjects {
	t.Helper()
	name, err := objectName(id)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := c.api.CoreV1().Secrets(c.ns).Get(ctx, name+"-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := c.api.CoreV1().Services(c.ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	workload, err := c.api.AppsV1().StatefulSets(c.ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return integrationObjects{secret, service, workload}
}

func (o integrationObjects) versions() [3]string {
	return [3]string{o.secret.ResourceVersion, o.service.ResourceVersion, o.workload.ResourceVersion}
}

func TestActualEnsureRoomStableResourceVersions(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// 实际配额禁止创建 Pod，此测试只检查 API 资源协调和服务器默认字段。
	quota := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "object-verification"}, Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{corev1.ResourcePods: resource.MustParse("0")}}}
	if _, err := c.api.CoreV1().ResourceQuotas(c.ns).Create(ctx, quota, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	spec := integrationRoomSpec(t, c)
	if _, err := c.EnsureRoom(ctx, spec); err != nil {
		t.Fatal(err)
	}
	err := wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		workload, err := c.api.AppsV1().StatefulSets(c.ns).Get(ctx, "room-"+spec.Room.ID, metav1.GetOptions{})
		return err == nil && workload.Status.ObservedGeneration >= workload.Generation, err
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := readIntegrationObjects(t, ctx, c, spec.Room.ID)
	if initial.service.Spec.ClusterIP == "" || initial.service.Spec.Ports[0].Protocol != corev1.ProtocolTCP || initial.workload.Spec.Template.Spec.DNSPolicy == "" {
		t.Fatal("API did not provide allocated/defaulted fields")
	}
	for _, metadata := range []*metav1.ObjectMeta{&initial.secret.ObjectMeta, &initial.service.ObjectMeta, &initial.workload.ObjectMeta} {
		metadata.Labels["integration.vex.imdomestic.com/preserved"] = "yes"
	}
	if _, err = c.api.CoreV1().Secrets(c.ns).Update(ctx, initial.secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.api.CoreV1().Services(c.ns).Update(ctx, initial.service, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.api.AppsV1().StatefulSets(c.ns).Update(ctx, initial.workload, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	stable := readIntegrationObjects(t, ctx, c, spec.Room.ID)
	for i := 0; i < 5; i++ {
		if _, err = c.EnsureRoom(ctx, spec); err != nil {
			t.Fatal(err)
		}
		after := readIntegrationObjects(t, ctx, c, spec.Room.ID)
		if after.versions() != stable.versions() {
			t.Fatalf("unchanged EnsureRoom wrote resources: before=%v after=%v", stable.versions(), after.versions())
		}
		if after.service.Spec.ClusterIP != initial.service.Spec.ClusterIP {
			t.Fatal("Service allocation changed")
		}
	}
	t.Logf("five unchanged reconciliations preserved Secret/Service/StatefulSet resourceVersions %v", stable.versions())
	spec.RoomToken = integrationRoomSpec(t, c).RoomToken
	if _, err = c.EnsureRoom(ctx, spec); err != nil {
		t.Fatal(err)
	}
	changedSecret := readIntegrationObjects(t, ctx, c, spec.Room.ID)
	if changedSecret.secret.ResourceVersion == stable.secret.ResourceVersion || changedSecret.service.ResourceVersion != stable.service.ResourceVersion || changedSecret.workload.ResourceVersion != stable.workload.ResourceVersion {
		t.Fatal("credential change did not update only Secret")
	}
	c.cfg.Server.Zone = "changed.vex.internal"
	if _, err = c.EnsureRoom(ctx, spec); err != nil {
		t.Fatal(err)
	}
	changedService := readIntegrationObjects(t, ctx, c, spec.Room.ID)
	if changedService.secret.ResourceVersion != changedSecret.secret.ResourceVersion || changedService.service.ResourceVersion == changedSecret.service.ResourceVersion || changedService.workload.ResourceVersion != changedSecret.workload.ResourceVersion {
		t.Fatal("zone change did not update only Service")
	}
	ref, err := c.Reference(spec.Room)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.StopRoom(ctx, ref, runtime.StopOptions{Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.EnsureRoom(ctx, spec); err != nil {
		t.Fatal(err)
	}
	restarted := readIntegrationObjects(t, ctx, c, spec.Room.ID)
	if *restarted.workload.Spec.Replicas != 1 || restarted.secret.ResourceVersion != changedService.secret.ResourceVersion || restarted.service.ResourceVersion != changedService.service.ResourceVersion {
		t.Fatal("replica reconciliation changed unrelated resources")
	}
	pods, err := c.api.CoreV1().Pods(c.ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 0 {
		t.Fatal("object verification quota allowed a Pod")
	}
}

func TestActualFailureContainerLogs(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	for _, initFailure := range []bool{false, true} {
		id := "crashing-main"
		if initFailure {
			id = "crashing-init"
		}
		labels := map[string]string{"vex.imdomestic.com/managed": "true", "vex.imdomestic.com/project": c.cfg.ID, "vex.imdomestic.com/room": id, "vex.imdomestic.com/generation": "1", "vex.imdomestic.com/build": "fabric-fc16eaac252b"}
		container := corev1.Container{Name: "mc", Image: c.cfg.Minecraft.Image, Command: []string{"/bin/sh", "-c", "echo vex-container-failure-evidence; exit 7"}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Mi"), corev1.ResourceCPU: resource.MustParse("10m")}, Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi"), corev1.ResourceCPU: resource.MustParse("100m")}}}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "room-" + id + "-0", Labels: labels}, Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyAlways, Containers: []corev1.Container{container}}}
		if initFailure {
			init := container.DeepCopy()
			init.Name = "seed-world"
			pod.Spec.InitContainers = []corev1.Container{*init}
		}
		if _, err := c.api.CoreV1().Pods(c.ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		err := wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
			current, err := c.api.CoreV1().Pods(c.ns).Get(ctx, pod.Name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			statuses := current.Status.ContainerStatuses
			if initFailure {
				statuses = current.Status.InitContainerStatuses
			}
			for _, status := range statuses {
				if status.State.Waiting != nil && status.State.Waiting.Reason == "CrashLoopBackOff" && status.RestartCount > 0 && status.LastTerminationState.Terminated != nil {
					return true, nil
				}
			}
			return false, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		ref, err := c.Reference(model.Room{Project: c.cfg.ID, ID: id, Kind: "commit", Generation: 1, BuildID: "fabric-fc16eaac252b"})
		if err != nil {
			t.Fatal(err)
		}
		logs, err := c.Logs(ctx, ref, runtime.LogOptions{Tail: 200, Diagnostics: true})
		if err != nil {
			t.Fatal(err)
		}
		expected := "[mc previous=true]"
		if initFailure {
			expected = "[seed-world previous=true]"
		}
		if !strings.Contains(logs, expected) || !strings.Contains(logs, "vex-container-failure-evidence") {
			t.Fatal("failure logs did not include the actual previous failed container output")
		}
		observed, err := c.ObserveRoom(ctx, ref)
		if err != nil || observed.Exists || !observed.ProcessExists || !observed.IdentityMatches {
			t.Fatal("actual orphan Pod must remain observable without its StatefulSet", err)
		}
		if err = c.StopRoom(ctx, ref, runtime.StopOptions{Timeout: time.Minute}); err != nil {
			t.Fatal(err)
		}
		if err = wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
			_, err := c.api.CoreV1().Pods(c.ns).Get(ctx, pod.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}); err != nil {
			t.Fatal(err)
		}
		t.Logf("captured actual CrashLoopBackOff logs for %s", id)
	}
}
