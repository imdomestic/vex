//go:build integration

package kubernetes_test

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/rooms"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// 分支房间重置世界：前提条件不满足时拒绝；满足时按 UID 删除世界卷，旧快照保持不变，再次启动得到新的空世界。
func TestRealResetWorldReplacesBranchVolume(t *testing.T) {
	binary := os.Getenv("VEX_IT_CLI")
	if binary == "" {
		t.Skip("requires the compiled Vex CLI for the confirmation prompt")
	}
	h := newLifecycleHarness(t, "reset", false)
	if err := h.manager.SyncBranches(h.ctx); err != nil {
		t.Fatal(err)
	}
	room, err := h.manager.Get(h.ctx, "main-fabric")
	if err != nil || room.Kind != "branch" || room.SHA != h.manifest.SHA {
		t.Fatal("test project branch room was not registered", err)
	}
	if _, _, err = h.manager.Start(h.ctx, room.ID, nil, auth.Identity{User: "integration"}); err != nil {
		t.Fatal(err)
	}
	h.wait("branch room Minecraft ready", 6*time.Minute, func() bool {
		room = h.current(room.ID)
		if room.State == "failed" {
			t.Fatal("actual Minecraft startup failed")
		}
		return room.State == "ready"
	})
	h.rcon(room.ID, "forceload add 0 0")
	h.wait("loaded Minecraft chunk", 30*time.Second, func() bool {
		h.rcon(room.ID, "execute if loaded 0 100 0 run say vex-reset-chunk-loaded")
		return strings.Contains(h.logs(room.ID), "vex-reset-chunk-loaded")
	})
	h.rcon(room.ID, "setblock 0 100 0 minecraft:lapis_block")
	h.rcon(room.ID, "forceload remove 0 0")
	room = h.stop(room.ID)
	if room.State != "stopped" {
		t.Fatal("branch room did not keep its world after stopping", room.State)
	}
	claim := func() *corev1.PersistentVolumeClaim {
		t.Helper()
		claims, err := h.cluster.CoreV1().PersistentVolumeClaims(h.ns).List(h.ctx, metav1.ListOptions{LabelSelector: "vex.imdomestic.com/room=" + room.ID})
		if err != nil || len(claims.Items) != 1 {
			t.Fatal("branch room must own exactly one world volume", err)
		}
		return &claims.Items[0]
	}
	original := claim()
	snapshotID := fmt.Sprintf("%s-g%d", room.ID, room.Generation)
	prefix := "/v2/projects/" + h.cfg.ID + "/rooms/" + room.ID
	reset := prefix + "/reset-world"
	body := map[string]string{"confirm": room.ID, "seed": "empty"}

	// 前提条件：label、确认、管理员与 tank 校验。
	h.failure(h.adminToken, http.MethodPost, reset, body, http.StatusConflict, "conflict")
	if status, _ := h.request(h.token, http.MethodPatch, prefix, map[string]string{"label": "finished"}); status != http.StatusOK {
		t.Fatalf("label finished returned %d", status)
	}
	h.failure(h.token, http.MethodPost, reset, body, http.StatusForbidden, "forbidden")
	h.failure(h.adminToken, http.MethodPost, reset, map[string]string{"confirm": "main-fabri"}, http.StatusBadRequest, "invalid_request")
	var snapshot model.Snapshot
	if err = h.db.Get(h.ctx, "snapshot", snapshotID, &snapshot); err != nil || snapshot.TankVerifiedAt != nil {
		t.Fatal("snapshot verification state differs from the test sequence", err)
	}
	h.failure(h.adminToken, http.MethodPost, reset, body, http.StatusConflict, "conflict")
	if err = h.manager.Archive.Step(h.ctx); err != nil {
		t.Fatal(err)
	}
	if err = h.db.Get(h.ctx, "snapshot", snapshotID, &snapshot); err != nil || snapshot.TankVerifiedAt == nil {
		t.Fatal("snapshot was not verified by the archive backend", err)
	}
	archived := filepath.Join(h.cfg.Archive.Local.Directory, "projects", h.cfg.ID, filepath.FromSlash(snapshot.Path))
	before, beforeSize, err := worldarchive.Digest(archived)
	if err != nil || before != snapshot.SHA256 || beforeSize != snapshot.Size {
		t.Fatal("archived snapshot differs from its record", err)
	}
	if current := claim(); current.UID != original.UID {
		t.Fatal("rejected resets changed the world volume")
	}

	// 真实 CLI：要求输入房间 ID，经管理员凭据调用 API。
	tokenFile := filepath.Join(h.root, "admin.token")
	if err = os.WriteFile(tokenFile, []byte(h.adminToken), 0600); err != nil {
		t.Fatal(err)
	}
	configuration := filepath.Join(h.root, "reset-client.toml")
	if err = os.WriteFile(configuration, nil, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(h.ctx, binary, "--config", configuration, "--url", h.baseURL, "--token-file", tokenFile, "-p", h.cfg.ID, "room", "reset-world", room.ID, "--seed", "empty")
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "VEX_") && !strings.HasPrefix(variable, "TMPDIR=") {
			command.Env = append(command.Env, variable)
		}
	}
	command.Env = append(command.Env, "TMPDIR="+filepath.Join(h.cfg.DataDir, ".work"))
	var output, diagnostic bytes.Buffer
	command.Stdin, command.Stdout, command.Stderr = strings.NewReader(room.ID+"\n"), &output, &diagnostic
	if err = command.Run(); err != nil {
		t.Fatalf("actual reset-world CLI failed: %v: %s", err, diagnostic.String())
	}
	if !strings.Contains(output.String(), "请输入房间 ID") {
		t.Fatal("CLI did not ask for the room ID")
	}
	if _, err = h.cluster.CoreV1().PersistentVolumeClaims(h.ns).Get(h.ctx, original.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("original world volume remains after reset", err)
	}
	var rt rooms.Runtime
	if err = h.db.Get(h.ctx, "room_runtime", room.ID, &rt); err != nil || rt.WorldExists {
		t.Fatal("runtime still records the deleted world", err)
	}
	var record rooms.WorldReset
	if err = h.db.Get(h.ctx, "world_reset", snapshotID, &record); err != nil || record.VolumeUID != string(original.UID) || record.SnapshotID != snapshotID || record.Seed != "empty" {
		t.Fatal("world reset event was not recorded", err)
	}
	after, afterSize, err := worldarchive.Digest(archived)
	if err != nil || after != before || afterSize != beforeSize {
		t.Fatal("reset changed the archived snapshot", err)
	}
	h.failure(h.adminToken, http.MethodPost, reset, body, http.StatusConflict, "conflict")

	// 再次启动：新的世界卷，没有之前写入的方块。
	if status, _ := h.request(h.token, http.MethodPatch, prefix, map[string]string{"label": "testing"}); status != http.StatusOK {
		t.Fatalf("label testing returned %d", status)
	}
	if _, _, err = h.manager.Start(h.ctx, room.ID, nil, auth.Identity{User: "integration"}); err != nil {
		t.Fatal(err)
	}
	h.wait("Minecraft on the new world", 6*time.Minute, func() bool {
		room = h.current(room.ID)
		if room.State == "failed" {
			t.Fatal("actual Minecraft startup on the new world failed")
		}
		return room.State == "ready"
	})
	if room.SeedResolved == nil || room.SeedResolved.Kind != "empty" {
		t.Fatal("restart did not use the reset seed", room.SeedResolved)
	}
	replacement := claim()
	if replacement.UID == original.UID {
		t.Fatal("restart reused the deleted volume UID")
	}
	h.rcon(room.ID, "forceload add 0 0")
	h.wait("block absent from the new world", 30*time.Second, func() bool {
		h.rcon(room.ID, "execute unless block 0 100 0 minecraft:lapis_block run say vex-reset-world-empty")
		return strings.Contains(h.logs(room.ID), "vex-reset-world-empty")
	})
	h.rcon(room.ID, "forceload remove 0 0")
	h.stop(room.ID)
	if err = h.manager.Archive.Step(h.ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("reset-world: volume %s replaced by %s, snapshot %s unchanged (%s)", original.UID, replacement.UID, snapshotID, before)
}
