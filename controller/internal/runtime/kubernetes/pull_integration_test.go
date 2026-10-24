//go:build integration

package kubernetes_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/testproject"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRealPreviewRollsAfterLeaseAndPreservesWorld(t *testing.T) {
	h := newLifecycleHarness(t, "preview", false)
	h.manager.Cfg.CI = config.ProjectCI{PRPreviews: true, PreviewAccess: "members", ForkPreviews: "approval"}
	h.manager.Cfg.Policy.OnNewHead = "when_empty"
	h.manager.Cfg.Rooms.DefaultSeed = "empty"
	h.manager.Cfg.Rooms.PRIdle = "10m"
	next := previewBuild(t, h)
	if next.SHA == h.manifest.SHA || next.Loader != h.manifest.Loader || next.Minecraft == nil || *next.Minecraft != *h.manifest.Minecraft {
		t.Fatal("preview rolling requires two distinct real builds for the same loader and Minecraft")
	}
	pull := model.Pull{Project: h.cfg.ID, Number: 123, Title: "真实预览世界检查", Author: "integration", HeadSHA: h.manifest.SHA, State: "open"}
	if err := h.manager.SyncPull(h.ctx, pull, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.manager.Get(h.ctx, "pr123-"+h.manifest.Loader); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("preview registered before a ready build was supplied", err)
	}
	if err := h.manager.SyncPull(h.ctx, pull, []model.Build{h.manifest}); err != nil {
		t.Fatal(err)
	}
	room, err := h.manager.Get(h.ctx, "pr123-"+h.manifest.Loader)
	if err != nil || room.State != "stopped" || room.Generation != 0 {
		t.Fatal("preview registration did not stay lazy", err)
	}
	statefulSets, err := h.cluster.AppsV1().StatefulSets(h.ns).List(h.ctx, metav1.ListOptions{})
	if err != nil || len(statefulSets.Items) != 0 {
		t.Fatal("lazy preview allocated a server", err)
	}
	started, _, err := h.manager.Start(h.ctx, room.ID, nil, auth.Identity{User: "integration"})
	if err != nil {
		t.Fatal(err)
	}
	h.wait("first actual preview build ready", 6*time.Minute, func() bool {
		room = h.current(room.ID)
		return room.State == "ready"
	})
	firstNode := *room.Node
	claimName := strings.Split(h.ref(room.ID).WorldID, "/")[1]
	claim, err := h.cluster.CoreV1().PersistentVolumeClaims(h.ns).Get(h.ctx, claimName, metav1.GetOptions{})
	if err != nil || string(claim.Status.Phase) != "Bound" {
		t.Fatal("first preview world PVC was not bound", err)
	}
	marker := func(condition, suffix string) {
		t.Helper()
		value := h.ns + "-" + suffix
		h.wait("actual preview condition "+suffix, 30*time.Second, func() bool {
			h.rcon(room.ID, "execute if "+condition+" run say "+value)
			return strings.Contains(h.logs(room.ID), "[Rcon] "+value)
		})
	}
	h.rcon(room.ID, "forceload add 0 0")
	marker("loaded 0 90 0", "loaded-first")
	h.rcon(room.ID, "setblock 0 90 0 minecraft:emerald_block")
	marker("block 0 90 0 minecraft:emerald_block", "block-first")
	h.rcon(room.ID, "save-all flush")
	pull.HeadSHA, pull.Title = next.SHA, "真实预览最新标题"
	if err = h.manager.SyncPull(h.ctx, pull, []model.Build{next}); err != nil {
		t.Fatal(err)
	}
	room = h.current(room.ID)
	if room.State != "ready" || room.Generation != 1 || room.BuildID != h.manifest.ID || room.PR == nil || room.PR.HeadSHA != next.SHA || room.PR.Title != pull.Title {
		t.Fatal("active lease did not preserve the running build and current PR metadata")
	}
	if err = h.manager.DeleteLease(h.ctx, room.ID, started.Lease.ID, auth.Identity{User: "integration"}); err != nil {
		t.Fatal(err)
	}
	seenStop := false
	h.wait("normal archive and second actual preview build ready", 8*time.Minute, func() bool {
		room = h.current(room.ID)
		seenStop = seenStop || room.State == "stopping" || room.State == "archiving"
		if room.State == "failed" {
			t.Fatal("preview rolling failed")
		}
		return room.State == "ready" && room.Generation == 2 && room.BuildID == next.ID
	})
	if !seenStop || room.Node == nil || *room.Node != firstNode || room.LastSnapshot == nil {
		t.Fatal("preview rolling skipped archive or changed the retained world's node")
	}
	retained, err := h.cluster.CoreV1().PersistentVolumeClaims(h.ns).Get(h.ctx, claimName, metav1.GetOptions{})
	if err != nil || retained.UID != claim.UID || string(retained.Status.Phase) != "Bound" {
		t.Fatal("preview rolling changed its world PVC", err)
	}
	h.wait("completed first-generation archive cleanup", time.Minute, func() bool {
		jobs, err := h.cluster.BatchV1().Jobs(h.ns).List(h.ctx, metav1.ListOptions{LabelSelector: "vex.imdomestic.com/room=" + room.ID + ",vex.imdomestic.com/generation=1"})
		if err != nil {
			t.Fatal(err)
		}
		return len(jobs.Items) == 0
	})
	h.rcon(room.ID, "forceload add 0 0")
	marker("block 0 90 0 minecraft:emerald_block", "block-after-roll")
	t.Logf("actual preview %s generation 1→2, builds %s→%s, live metadata and lease guard verified; node=%s PVC=%s retained and emerald block confirmed", room.ID, h.manifest.ID, next.ID, firstNode, retained.UID)
	room = h.stop(room.ID)
	if room.State != "stopped" || room.LastSnapshot == nil {
		t.Fatal("second preview generation did not stop with its world archived")
	}
	pull.State = "closed"
	if err = h.manager.SyncPull(h.ctx, pull, nil); err != nil {
		t.Fatal(err)
	}
	h.wait("closed preview normal archive", 5*time.Minute, func() bool {
		room = h.current(room.ID)
		return room.State == "closed"
	})
	if room.Label != "finished" || room.LastSnapshot == nil || room.ClosedAt == nil {
		t.Fatal("closed preview omitted its final lifecycle metadata")
	}
	snapshots, err := store.All[model.Snapshot](h.ctx, h.db, "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, snapshot := range snapshots {
		if snapshot.RoomID == room.ID {
			path := filepath.Join(h.cfg.DataDir, filepath.FromSlash(snapshot.Path))
			digest, size, err := worldarchive.Digest(path)
			if err != nil || digest != snapshot.SHA256 || size != snapshot.Size {
				t.Fatal("preview snapshot bytes differ from recorded digest", err)
			}
			count++
		}
	}
	if count != 2 {
		t.Fatal("preview did not preserve both real generation snapshots")
	}
	h.wait("closed preview removes its own PVC after archive", time.Minute, func() bool {
		_, err := h.cluster.CoreV1().PersistentVolumeClaims(h.ns).Get(h.ctx, claimName, metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	})
	t.Log("closed preview retained two digest-verified snapshots, normally stopped Minecraft and removed its isolated runtime PVC")
}

// previewBuild 在测试项目的 PR 分支上提交新的改动，并导入它的 CI Build。
func previewBuild(t *testing.T, h *lifecycleHarness) model.Build {
	t.Helper()
	sha := h.project.Commit(t, "feat/preview", "update the preview", map[string]string{"PREVIEW.md": "预览更新\n"})
	if err := h.manager.Git.Refresh(h.ctx); err != nil {
		t.Fatal(err)
	}
	return testproject.ImportBuild(t, h.manager.Builds, sha)
}
