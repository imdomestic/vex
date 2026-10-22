//go:build integration

package kubernetes_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/slp"
	"github.com/imdomestic/vex/controller/internal/testproject"
)

// 被测试项目改写历史后，已有记录照常显示、启动、恢复与导出，只有在被删除的提交上重新构建或新建房间失败。
func TestRealRewrittenHistoryKeepsRecordsUsable(t *testing.T) {
	h := newLifecycleHarness(t, "history", false)
	base := h.manifest.SHA
	sha := h.project.Commit(t, "feat/rewrite", "commit removed by a history rewrite", map[string]string{"REWRITE.md": "将被改写的历史\n"})
	if err := h.manager.Git.Refresh(h.ctx); err != nil {
		t.Fatal(err)
	}
	build := testproject.ImportBuild(t, h.manager.Builds, sha)
	room, _, err := h.manager.Create(h.ctx, "commit", sha, build.Loader, "empty", "")
	if err != nil {
		t.Fatal(err)
	}
	if room.Commit == nil || room.Commit.SHA != sha || room.Commit.Subject != "commit removed by a history rewrite" {
		t.Fatal("room did not record its commit metadata", room.Commit)
	}
	if _, _, err = h.manager.Start(h.ctx, room.ID, nil, auth.Identity{User: "integration"}); err != nil {
		t.Fatal(err)
	}
	h.wait("Minecraft on the commit that will be removed", 6*time.Minute, func() bool {
		room = h.current(room.ID)
		if room.State == "failed" {
			t.Fatal("actual Minecraft startup failed")
		}
		return room.State == "ready"
	})
	h.rcon(room.ID, "forceload add 0 0")
	h.wait("loaded Minecraft chunk", 30*time.Second, func() bool {
		h.rcon(room.ID, "execute if loaded 0 100 0 run say vex-history-chunk-loaded")
		return strings.Contains(h.logs(room.ID), "vex-history-chunk-loaded")
	})
	h.rcon(room.ID, "setblock 0 100 0 minecraft:emerald_block")
	h.rcon(room.ID, "forceload remove 0 0")
	room = h.stop(room.ID)
	if err = h.manager.Archive.Step(h.ctx); err != nil {
		t.Fatal(err)
	}
	snapshotID := fmt.Sprintf("%s-g%d", room.ID, room.Generation)
	var snapshot model.Snapshot
	if err = h.db.Get(h.ctx, "snapshot", snapshotID, &snapshot); err != nil || snapshot.TankVerifiedAt == nil || snapshot.LocalAvailable || snapshot.Commit == nil || snapshot.Commit.SHA != sha || snapshot.Manifest == nil {
		t.Fatal("snapshot was not archived, evicted and self-contained", err)
	}

	// 被测试项目强制推送删除这个提交；镜像照常刷新、prune 并 gc。
	h.project.ForcePush(t, "feat/rewrite", base)
	if err = h.manager.Refresh(h.ctx); err != nil {
		t.Fatal(err)
	}
	testproject.Git(t, h.root, "--git-dir="+filepath.Join(h.root, "mirror.git"), "gc", "--prune=now")
	var failure *model.Error
	if _, err = h.manager.Git.CommitInfo(h.ctx, sha); !errors.As(err, &failure) || failure.Code != "commit_unavailable" {
		t.Fatal("removed commit is still readable after gc", err)
	}
	if err = h.manager.Refresh(h.ctx); err != nil {
		t.Fatal(err)
	}

	prefix := "/v2/projects/" + h.cfg.ID
	show := func(route string, output any) {
		t.Helper()
		status, data := h.request(h.token, http.MethodGet, prefix+route, nil)
		if status != http.StatusOK || json.Unmarshal(data, output) != nil {
			t.Fatalf("GET %s returned %d", route, status)
		}
	}
	var shownBuild model.Build
	show("/builds/"+build.ID, &shownBuild)
	if shownBuild.CommitMissingAt == nil || shownBuild.Commit == nil || shownBuild.Commit.SHA != sha || shownBuild.Manifest == nil || shownBuild.Status != "ready" {
		t.Fatal("build record lost its metadata or missing-commit mark")
	}
	var shownRoom model.Room
	show("/rooms/"+room.ID, &shownRoom)
	if shownRoom.CommitMissingAt == nil || shownRoom.Commit == nil || shownRoom.Commit.Subject != "commit removed by a history rewrite" {
		t.Fatal("room record lost its metadata or missing-commit mark")
	}
	var shownSnapshot model.Snapshot
	show("/snapshots/"+snapshotID, &shownSnapshot)
	if shownSnapshot.CommitMissingAt == nil || shownSnapshot.Commit == nil || shownSnapshot.Manifest == nil {
		t.Fatal("snapshot record lost its metadata or missing-commit mark")
	}
	var commits []model.Commit
	show("/commits?branch=feat/rewrite", &commits)
	if len(commits) != 1 || commits[0].SHA != base {
		t.Fatal("branch history still lists the removed commit", commits)
	}

	// 只有在被删除的提交上新建房间或重新导入构建失败。
	h.failure(h.token, http.MethodPost, prefix+"/rooms", map[string]string{"kind": "commit", "sha": sha, "loader": build.Loader, "seed": "empty"}, http.StatusConflict, "commit_unavailable")
	if _, _, err = h.manager.Builds.ImportCI(h.ctx, testproject.CIImport(t, sha)); !errors.As(err, &failure) || failure.Code != "commit_unavailable" {
		t.Fatal("rebuilding the removed commit did not report commit_unavailable", err)
	}

	// 已有房间以快照为种子启动：使用已有 Build，快照从归档恢复，方块仍在。
	status, data := h.request(h.token, http.MethodPost, prefix+"/rooms/"+room.ID+"/start", map[string]string{"seed": "snapshot:" + snapshotID})
	var started model.StartResult
	if status != http.StatusAccepted || json.Unmarshal(data, &started) != nil || started.Room.Phase != "restoring seed" || started.Room.BuildID != build.ID {
		t.Fatalf("restart on the removed commit returned %d", status)
	}
	if err = h.manager.Reconcile(h.ctx); err != nil {
		t.Fatal(err)
	}
	if err = h.manager.Archive.Step(h.ctx); err != nil {
		t.Fatal(err)
	}
	if err = h.db.Get(h.ctx, "snapshot", snapshotID, &snapshot); err != nil || !snapshot.LocalAvailable || snapshot.Restore == nil || snapshot.Restore.Status != "succeeded" {
		t.Fatal("snapshot restoration failed after the history rewrite", err)
	}
	h.wait("restored Minecraft ready", 6*time.Minute, func() bool {
		room = h.current(room.ID)
		if room.State == "failed" {
			t.Fatal("actual Minecraft restart failed after the history rewrite")
		}
		return room.State == "ready"
	})
	response, err := slp.Probe(h.ctx, h.cfg.Server.RouterAddress, room.Host, room.Port)
	if err != nil || !slp.Matches(response, room) {
		t.Fatal("routed identity mismatch after restart", err)
	}
	h.rcon(room.ID, "forceload add 0 0")
	h.wait("restored emerald block", 30*time.Second, func() bool {
		h.rcon(room.ID, "execute if block 0 100 0 minecraft:emerald_block run say vex-history-world-ok")
		return strings.Contains(h.logs(room.ID), "vex-history-world-ok")
	})
	h.rcon(room.ID, "forceload remove 0 0")

	// mrpack 只读取记录与已发布文件。
	for _, route := range []string{"/rooms/" + room.ID + "/pack.mrpack", "/builds/" + build.ID + "/pack.mrpack"} {
		status, data = h.request(h.token, http.MethodGet, prefix+route, nil)
		if status != http.StatusOK {
			t.Fatalf("GET %s returned %d", route, status)
		}
		archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range archive.File {
			found = found || entry.Name == "modrinth.index.json"
		}
		if !found {
			t.Fatalf("%s has no modrinth index", route)
		}
	}
	h.stop(room.ID)
	if err = h.manager.Archive.Step(h.ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("history rewrite: build %s, room %s and snapshot %s stayed usable after force-push and gc of %s", build.ID, room.ID, snapshotID, sha)
}
