//go:build integration

package kubernetes_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/model"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRealInternalSourcesStartAndArchive(t *testing.T) {
	h := newLifecycleHarness(t, "s1source", false)
	room, _, err := h.manager.Create(h.ctx, "commit", h.manifest.SHA, h.manifest.Loader, "empty", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = h.manager.Start(h.ctx, room.ID, nil, auth.Identity{User: "integration"}); err != nil {
		t.Fatal(err)
	}
	h.wait("Minecraft behind internal source checks", 6*time.Minute, func() bool {
		room = h.current(room.ID)
		if room.State == "failed" {
			t.Fatal("Minecraft failed while downloading through the internal source guard")
		}
		return room.State == "ready"
	})
	pods, err := h.cluster.CoreV1().Pods(h.ns).List(h.ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=minecraft"})
	if err != nil || len(pods.Items) != 1 {
		t.Fatal("expected one actual Minecraft Pod", err)
	}
	if _, ok := h.sources.Load(pods.Items[0].Status.PodIP); !ok {
		t.Fatal("the actual Minecraft download did not pass the source guard")
	}
	foreign, err := h.cluster.CoreV1().Pods("kube-system").List(h.ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rejected := 0
	for _, pod := range foreign.Items {
		if pod.Spec.HostNetwork || pod.Status.PodIP == "" || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		allowed, err := h.backend.RoomSource(h.ctx, pod.Status.PodIP)
		if err != nil || allowed {
			t.Fatal("unrelated actual Pod must not pass room source checks", err)
		}
		rejected++
	}
	if rejected == 0 {
		t.Fatal("no actual unrelated Pod was tested")
	}
	room = h.stop(room.ID)
	if room.State != "closed" {
		t.Fatal("Minecraft did not close after archive")
	}
	archiveSources := 0
	h.sources.Range(func(_, component any) bool {
		if component == "archive" {
			archiveSources++
		}
		return true
	})
	if archiveSources == 0 {
		t.Fatal("the actual archive upload did not pass the source guard")
	}
	var snapshot model.Snapshot
	if err = h.db.Get(h.ctx, "snapshot", fmt.Sprintf("%s-g%d", room.ID, room.Generation), &snapshot); err != nil || !snapshot.LocalAvailable || snapshot.Size <= 0 {
		t.Fatal("archive upload did not produce a verified local snapshot", err)
	}
	t.Logf("actual Minecraft download and archive upload accepted; %d unrelated Pods rejected; snapshot %s", rejected, snapshot.ID)
}
