package rooms

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/auth"
	"github.com/imdomestic/chorus-testbed/controller/internal/build"
	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/events"
	"github.com/imdomestic/chorus-testbed/controller/internal/gitmirror"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
)

func freshnessGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", append([]string{"-c", "user.name=Chorus Integration", "-c", "user.email=chorus@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func freshnessCommit(t *testing.T, source, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(source, "content.txt"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	freshnessGit(t, source, "add", "content.txt")
	freshnessGit(t, source, "commit", "-m", content)
	return freshnessGit(t, source, "rev-parse", "HEAD")
}

func freshnessManager(t *testing.T) (*Manager, string, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	freshnessGit(t, root, "init", "--initial-branch=main", source)
	sha := freshnessCommit(t, source, "初始分支")
	mirror, err := gitmirror.New(source, filepath.Join(root, "mirror.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = mirror.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := config.Config{
		Server:    config.Server{DataDir: filepath.Join(root, "data")},
		Loaders:   map[string]config.Loader{"fabric": {Enabled: true}},
		Minecraft: config.Minecraft{Zone: "chorus.invalid"},
		Rooms:     config.Rooms{DefaultSeed: "empty", JoinLease: "1m"},
	}
	builder, err := build.New(cfg, db, mirror)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(cfg, db, mirror, builder, nil, nil, events.New(db))
	if err = manager.SyncBranches(t.Context()); err != nil {
		t.Fatal(err)
	}
	return manager, source, sha
}

func TestBranchStartFetchesRealHeadAndPublishesEvent(t *testing.T) {
	for _, internal := range []bool{false, true} {
		name := "public start"
		if internal {
			name = "internal restart"
		}
		t.Run(name, func(t *testing.T) {
			manager, source, _ := freshnessManager(t)
			_, changes, unsubscribe, err := manager.Events.Subscribe(t.Context(), "")
			if err != nil {
				t.Fatal(err)
			}
			defer unsubscribe()
			sha := freshnessCommit(t, source, "启动前新增内容")
			var result model.StartResult
			var status int
			if internal {
				manager.mu.Lock()
				result, status, err = manager.start(t.Context(), "main-fabric", nil, auth.Identity{User: "controller"})
				manager.mu.Unlock()
			} else {
				result, status, err = manager.Start(t.Context(), "main-fabric", nil, auth.Identity{User: "integration"})
			}
			if err != nil || status != 202 || result.Room.SHA != sha || result.Room.Generation != 1 || strings.Contains(result.Room.Phase, "使用缓存") {
				t.Fatalf("fresh branch start: %+v status=%d err=%v", result.Room, status, err)
			}
			queued, err := manager.Builds.Get(t.Context(), result.Room.BuildID)
			if err != nil || queued.SHA != sha || queued.Status != "queued" {
				t.Fatalf("actual build queue did not receive fetched SHA: %+v %v", queued, err)
			}
			select {
			case change := <-changes:
				var branch model.Branch
				if err = json.Unmarshal(change.Data, &branch); err != nil || change.Name != "branch_moved" || branch.Head != sha || branch.Rooms["fabric"] != result.Room.ID {
					t.Fatalf("branch event mismatch: name=%s branch=%+v err=%v", change.Name, branch, err)
				}
			default:
				t.Fatal("successful start fetch did not publish a branch event")
			}
			freshnessCommit(t, source, "当前周期启动之后新增内容")
			again, _, err := manager.Start(t.Context(), "main-fabric", nil, auth.Identity{User: "integration"})
			if err != nil || again.Room.SHA != sha || again.Room.Generation != result.Room.Generation {
				t.Fatalf("active cycle changed its pinned SHA: %+v %v", again.Room, err)
			}
			if err = manager.Refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
			current, err := manager.Get(t.Context(), result.Room.ID)
			if err != nil || current.SHA != sha || !current.BehindHead || current.Generation != result.Room.Generation {
				t.Fatalf("refresh changed an active cycle or omitted behindHead: %+v %v", current, err)
			}
		})
	}
}

func TestBranchStartUsesRealCachedMirrorWhenSourceUnavailable(t *testing.T) {
	manager, source, sha := freshnessManager(t)
	if err := os.Rename(source, source+"-offline"); err != nil {
		t.Fatal(err)
	}
	result, status, err := manager.Start(t.Context(), "main-fabric", nil, auth.Identity{User: "integration"})
	if err != nil || status != 202 || result.Room.SHA != sha || !strings.Contains(result.Room.Phase, "使用缓存的分支信息") {
		t.Fatalf("cached branch start: %+v status=%d err=%v", result.Room, status, err)
	}
	stored, err := manager.Get(t.Context(), result.Room.ID)
	if err != nil || stored.Phase != result.Room.Phase {
		t.Fatal("cached branch phase was not persisted:", err)
	}
}

func TestRefreshRegistersAndPrunesRealBranches(t *testing.T) {
	manager, source, _ := freshnessManager(t)
	_, changes, unsubscribe, err := manager.Events.Subscribe(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	freshnessGit(t, source, "branch", "feature")
	if err = manager.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-changes:
		if event.Name != "branch_added" {
			t.Fatal("branch registration event:", event.Name)
		}
	case <-time.After(time.Second):
		t.Fatal("branch registration event missing")
	}
	room, err := manager.Get(t.Context(), "feature-fabric")
	if err != nil || room.State != "stopped" || room.Generation != 0 {
		t.Fatalf("new branch started unexpectedly: %+v %v", room, err)
	}
	freshnessGit(t, source, "branch", "-D", "feature")
	if err = manager.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-changes:
		if event.Name != "branch_deleted" {
			t.Fatal("branch deletion event:", event.Name)
		}
	case <-time.After(time.Second):
		t.Fatal("branch deletion event missing")
	}
	room, err = manager.Get(t.Context(), room.ID)
	if err != nil || room.State != "closed" || room.Label != "finished" {
		t.Fatalf("deleted branch stayed startable: %+v %v", room, err)
	}
}

func TestRefreshDeadlineIncludesRoomLock(t *testing.T) {
	manager, _, _ := freshnessManager(t)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := manager.Refresh(ctx); !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("refresh ignored the caller deadline while waiting for room state: %v", err)
	}
}
