//go:build integration

package kubernetes_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/rooms"
	"github.com/imdomestic/vex/controller/internal/runtime"
	"github.com/imdomestic/vex/controller/internal/slp"
	"github.com/imdomestic/vex/controller/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRealSeraphLostRecovery(t *testing.T) {
	if os.Getenv("VEX_IT_SERAPH_RECOVERY") != "1" {
		t.Skip("requires an authorized seraph k3s stop/start window")
	}
	h := newLifecycleHarness(t, "lost", false)
	room := h.startCommit()
	if room.Node == nil || *room.Node != "seraph" {
		t.Fatal("configured ephemeral priority did not select seraph")
	}
	ref := h.ref(room.ID)
	pod, err := h.cluster.CoreV1().Pods(h.ns).Get(h.ctx, ref.Attributes["objectName"]+"-0", metav1.GetOptions{})
	if err != nil || pod.Spec.NodeName != "seraph" {
		t.Fatal("actual room process did not run on seraph", err)
	}
	marker := func(condition, suffix string) {
		t.Helper()
		value := h.ns + "-" + suffix
		h.wait("actual Minecraft condition "+suffix, 30*time.Second, func() bool {
			h.rcon(room.ID, "execute if "+condition+" run say "+value)
			return strings.Contains(h.logs(room.ID), "[Rcon] "+value)
		})
	}
	h.rcon(room.ID, "forceload add 0 0")
	marker("loaded 0 90 0", "chunk-loaded")
	h.rcon(room.ID, "setblock 0 90 0 minecraft:emerald_block")
	marker("block 0 90 0 minecraft:emerald_block", "block-before")
	h.rcon(room.ID, "save-all flush")
	remote := func(ctx context.Context, args ...string) error {
		command := append([]string{"-u", "linwhite", "ssh", "-o", "BatchMode=yes", "-o", "ControlMaster=no", "-o", "ControlPath=none", "seraph", "sudo", "-n"}, args...)
		output, err := exec.CommandContext(ctx, "sudo", command...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("authorized seraph service operation failed: %w: %s", err, output)
		}
		return nil
	}
	guard := ""
	armed := false
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := remote(ctx, "systemctl", "start", "k3s.service"); err != nil {
			t.Error(err)
			return
		}
		if armed {
			if err := remote(ctx, "systemctl", "stop", guard+".timer"); err != nil {
				t.Error(err)
			}
		}
	})
	for cycle := 1; cycle <= 2; cycle++ {
		guard = fmt.Sprintf("vex-it-seraph-%s-%d", os.Getenv("VEX_IT_RUN_ID"), cycle)
		if err = remote(h.ctx, "systemd-run", "--on-active=300", "--unit="+guard, "/run/current-system/sw/bin/systemctl", "start", "k3s.service"); err != nil {
			t.Fatal(err)
		}
		armed = true
		if err = remote(h.ctx, "systemctl", "is-active", "--quiet", guard+".timer"); err != nil {
			t.Fatal(err)
		}
		if err = remote(h.ctx, "systemctl", "stop", "k3s.service"); err != nil {
			t.Fatal(err)
		}
		h.wait("actual node unavailable", 2*time.Minute, func() bool {
			node, err := h.cluster.CoreV1().Nodes().Get(h.ctx, "seraph", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue {
					return true
				}
			}
			return false
		})
		h.wait("persisted 60 second lost condition", 2*time.Minute, func() bool { return h.current(room.ID).State == "lost" })
		var rt rooms.Runtime
		if err = h.db.Get(h.ctx, "room_runtime", room.ID, &rt); err != nil || rt.NodeMissingSince == nil || time.Since(*rt.NodeMissingSince) < time.Minute {
			t.Fatal("lost omitted real persisted unavailability interval", err)
		}
		node, err := h.cluster.CoreV1().Nodes().Get(h.ctx, "seraph", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var transition time.Time
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue {
				transition = condition.LastTransitionTime.Time
			}
		}
		if transition.IsZero() || time.Since(transition) < time.Minute {
			t.Fatal("actual Node Ready condition did not remain unavailable for a minute")
		}
		if cycle == 2 {
			if err = h.backend.StopRoom(h.ctx, ref, runtime.StopOptions{Timeout: time.Minute}); err != nil {
				t.Fatal(err)
			}
		}
		if err = remote(h.ctx, "systemctl", "start", "k3s.service"); err != nil {
			t.Fatal(err)
		}
		h.wait("actual seraph Ready", 90*time.Second, func() bool {
			nodes, err := h.backend.Capacity(h.ctx)
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
			h.wait("same Minecraft process ready", 90*time.Second, func() bool {
				status, err := h.backend.ObserveRoom(h.ctx, ref)
				if err != nil {
					t.Fatal(err)
				}
				return status.Ready
			})
			h.wait("routed ready after recovery", time.Minute, func() bool { return h.current(room.ID).State == "ready" })
			current := h.current(room.ID)
			response, err := slp.Probe(h.ctx, h.cfg.Server.RouterAddress, current.Host, current.Port)
			if err != nil || !slp.Matches(response, current) || current.Generation != room.Generation || current.BuildID != room.BuildID {
				t.Fatal("actual routed recovery changed room identity", err)
			}
			currentPod, err := h.cluster.CoreV1().Pods(h.ns).Get(h.ctx, pod.Name, metav1.GetOptions{})
			if err != nil || currentPod.UID != pod.UID {
				t.Fatal("recovery replaced the original process", err)
			}
			marker("block 0 90 0 minecraft:emerald_block", "block-after")
		} else {
			h.wait("stopped Minecraft process exits", 3*time.Minute, func() bool {
				status, err := h.backend.ObserveRoom(h.ctx, ref)
				if err != nil {
					t.Fatal(err)
				}
				return !status.ProcessExists
			})
			h.wait("lost process follows normal archive", 5*time.Minute, func() bool { return h.current(room.ID).State == "closed" })
			snapshots, err := store.All[model.Snapshot](h.ctx, h.db, "snapshot")
			if err != nil || len(snapshots) != 1 || snapshots[0].Size == 0 {
				t.Fatal("recovered stop omitted actual world archive", err)
			}
		}
		if err = remote(h.ctx, "systemctl", "stop", guard+".timer"); err != nil {
			t.Fatal(err)
		}
		armed = false
	}
}
