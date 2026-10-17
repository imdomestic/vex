//go:build integration

package kubernetes

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/runtime"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestActualInstanceNamespaceIsolation(t *testing.T) {
	a := integrationClientWithNamespace(t, "instance", "vex-p-vex-it-a-", "vex-system")
	b := integrationClientWithNamespace(t, "instance", "vex-p-vex-it-b-", "kube-system")
	b.cfg.Server.Zone = "instance-b.vex.internal"
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, client := range []*Client{a, b} {
		quota := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "object-verification"}, Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{corev1.ResourcePods: resource.MustParse("0")}}}
		if _, err := client.api.CoreV1().ResourceQuotas(client.ns).Create(ctx, quota, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		policy, err := client.api.NetworkingV1().NetworkPolicies(client.ns).Get(ctx, "vex-project", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if policy.Spec.Ingress[0].From[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != client.cfg.Runtime.Kubernetes.SystemNamespace {
			t.Fatal("actual NetworkPolicy ignored the configured router namespace")
		}
	}
	aSpec, bSpec := integrationRoomSpec(t, a), integrationRoomSpec(t, b)
	aRef, err := a.EnsureRoom(ctx, aSpec)
	if err != nil {
		t.Fatal(err)
	}
	bRef, err := b.EnsureRoom(ctx, bSpec)
	if err != nil {
		t.Fatal(err)
	}
	aObjects := readIntegrationObjects(t, ctx, a, aRef.ID)
	bObjects := readIntegrationObjects(t, ctx, b, bRef.ID)
	if a.cfg.ID != b.cfg.ID || aRef.WorldID == bRef.WorldID || aObjects.secret.UID == bObjects.secret.UID || aObjects.service.UID == bObjects.service.UID || aObjects.workload.UID == bObjects.workload.UID || aObjects.service.Spec.ClusterIP == bObjects.service.Spec.ClusterIP {
		t.Fatal("same-project instances did not receive distinct actual objects")
	}
	if _, err = b.ObserveRoom(ctx, aRef); err == nil {
		t.Fatal("foreign instance reference was accepted for observation")
	}
	if err = b.StopRoom(ctx, aRef, runtime.StopOptions{Timeout: time.Minute}); err == nil {
		t.Fatal("foreign instance reference was accepted for stopping")
	}
	if err = a.Sync(ctx, bRef, bRef.ID+"--"+b.cfg.ID+"."+b.cfg.Server.Zone); err == nil {
		t.Fatal("foreign instance reference was accepted for routing")
	}
	if err = a.Remove(ctx, bRef); err == nil {
		t.Fatal("foreign instance reference was accepted for route removal")
	}
	bExtra := integrationRoomSpec(t, b)
	bExtra.Room.ID = "second-fabric"
	if _, err = b.EnsureRoom(ctx, bExtra); err != nil {
		t.Fatal(err)
	}
	for _, client := range []*Client{a, b} {
		nodes, err := client.Capacity(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, node := range nodes {
			if node.Name != "encore" {
				continue
			}
			found = true
			want := []string{client.cfg.ID + "/main-fabric"}
			if client == b {
				want = append(want, client.cfg.ID+"/second-fabric")
			}
			if !slices.Equal(node.Rooms, want) {
				t.Fatalf("instance capacity included another prefix: got %v want %v", node.Rooms, want)
			}
		}
		if !found {
			t.Fatal("configured actual node was absent")
		}
	}
	if err = b.StopRoom(ctx, bRef, runtime.StopOptions{Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	aAfter := readIntegrationObjects(t, ctx, a, aRef.ID)
	bAfter := readIntegrationObjects(t, ctx, b, bRef.ID)
	if *aAfter.workload.Spec.Replicas != 1 || *bAfter.workload.Spec.Replicas != 0 || aAfter.secret.ResourceVersion != aObjects.secret.ResourceVersion || aAfter.service.ResourceVersion != aObjects.service.ResourceVersion {
		t.Fatal("stopping the second instance changed the first instance")
	}
	routers, err := a.api.CoreV1().Pods(a.cfg.Runtime.Kubernetes.SystemNamespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=mc-router,app.kubernetes.io/part-of=vex"})
	if err != nil {
		t.Fatal(err)
	}
	checkedRouter := false
	for _, pod := range routers.Items {
		if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil || pod.Status.PodIP == "" {
			continue
		}
		allowedA, err := a.RouterSource(ctx, pod.Status.PodIP)
		if err != nil {
			t.Fatal(err)
		}
		allowedB, err := b.RouterSource(ctx, pod.Status.PodIP)
		if err != nil {
			t.Fatal(err)
		}
		if !allowedA || allowedB {
			t.Fatal("actual router source did not respect the configured namespace")
		}
		checkedRouter = true
	}
	if !checkedRouter {
		t.Fatal("actual mc-router Pod was unavailable")
	}
	for _, client := range []*Client{a, b} {
		pods, err := client.api.CoreV1().Pods(client.ns).List(ctx, metav1.ListOptions{})
		if err != nil || len(pods.Items) != 0 {
			t.Fatal("instance object verification unexpectedly created a Pod", err)
		}
	}
	t.Logf("same project %s isolated in %s and %s; actual references, capacity, router source and NetworkPolicy verified", a.cfg.ID, a.ns, b.ns)
}
