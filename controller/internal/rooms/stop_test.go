package rooms

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

func TestStoppingQueuedGenerationKeepsArchivedWorld(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db, err = db.WithProject("test")
	if err != nil {
		t.Fatal(err)
	}
	manager := &Manager{Cfg: config.ProjectConfig{ID: "test"}, DB: db}
	ctx := context.Background()
	node := "seraph"
	archived := time.Date(2026, 10, 20, 19, 8, 28, 0, time.UTC)
	room := model.Room{Project: "test", ID: "pr1-fabric", Kind: "pr", Label: "testing", State: "queued", Phase: "waiting for capacity",
		Generation: 5, Node: &node, LastSnapshot: &archived}
	if err = manager.save(ctx, room, Runtime{Generation: 5, WorldExists: true}); err != nil {
		t.Fatal(err)
	}
	stopping, status, err := manager.Stop(ctx, room.ID, false)
	if err != nil || status != 202 || stopping.State != "stopping" {
		t.Fatal("queued room did not enter the stopping flow", status, err)
	}
	current, err := manager.Get(ctx, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := manager.runtime(ctx, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.reconcileStop(ctx, current, stored); err != nil {
		t.Fatal(err)
	}
	current, err = manager.Get(ctx, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err = manager.runtime(ctx, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != "stopped" || current.Phase != "stopped" || current.Generation != 5 || current.Node == nil || *current.Node != node {
		t.Fatal("queued generation did not return to stopped on its retained node", current.State, current.Phase)
	}
	if current.LastSnapshot == nil || !current.LastSnapshot.Equal(archived) || !stored.WorldExists || stored.Spec != nil {
		t.Fatal("stop changed the archived world record")
	}
	var snapshot model.Snapshot
	if err = manager.DB.Get(ctx, "snapshot", "pr1-fabric-g5", &snapshot); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("queued generation produced a snapshot without running", err)
	}
}
