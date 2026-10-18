//go:build integration

package kubernetes

import (
	"os"
	"testing"

	"github.com/imdomestic/vex/controller/internal/config"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestActualInternalRoomSources(t *testing.T) {
	kubeconfig, project := os.Getenv("VEX_IT_KUBECONFIG"), os.Getenv("VEX_IT_SOURCE_PROJECT")
	if kubeconfig == "" || project == "" {
		t.Skip("requires an existing real Minecraft room and VEX_IT_SOURCE_PROJECT")
	}
	defaults := config.Defaults()
	defaults.Runtime.Kubernetes.KubeconfigFile = kubeconfig
	client, err := New(config.ProjectConfig{ID: project, Runtime: defaults.Runtime})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	pods, err := client.api.CoreV1().Pods(client.ns).List(ctx, metav1.ListOptions{LabelSelector: "vex.imdomestic.com/managed=true"})
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || pod.Status.PodIP == "" {
			continue
		}
		allowed, err := client.RoomSource(ctx, pod.Status.PodIP)
		if err != nil || !allowed {
			t.Fatal("running room source rejected:", err)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no running actual room Pod was checked")
	}
	foreign, err := client.api.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rejected := 0
	for _, pod := range foreign.Items {
		if pod.Status.PodIP == "" || pod.Spec.HostNetwork || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		allowed, err := client.RoomSource(ctx, pod.Status.PodIP)
		if err != nil || allowed {
			t.Fatal("foreign Pod source accepted:", err)
		}
		rejected++
	}
	if rejected == 0 {
		t.Fatal("no actual foreign Pod was checked")
	}
	t.Logf("actual source checks: %d room Pods accepted, %d unrelated Pods rejected", checked, rejected)
}
