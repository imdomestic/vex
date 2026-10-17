package rooms

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

func TestDeleteClosedRoomPreservesGenerationAndProjectBoundary(t *testing.T) {
	global, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer global.Close()
	db, err := global.WithProject("alpha")
	if err != nil {
		t.Fatal(err)
	}
	other, err := global.WithProject("beta")
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{Cfg: config.ProjectConfig{ID: "alpha"}, DB: db}
	room := model.Room{Project: "alpha", ID: "c01234567-fabric", Kind: "commit", SHA: "0123456789012345678901234567890123456789", Loader: "fabric", Generation: 8, State: "closed"}
	if err = db.Put(t.Context(), "room", room.ID, room); err != nil {
		t.Fatal(err)
	}
	foreign := room
	foreign.Project = "beta"
	if err = other.Put(t.Context(), "room", room.ID, foreign); err != nil {
		t.Fatal(err)
	}
	if err = m.Delete(t.Context(), room.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Get(t.Context(), room.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("closed room record remained", err)
	}
	room.Generation = 0
	if err = m.restoreGeneration(t.Context(), &room); err != nil || room.Generation != 8 {
		t.Fatal("room deletion lost the permanent generation", err)
	}
	if err = other.Get(t.Context(), "room", room.ID, &foreign); err != nil || foreign.Project != "beta" {
		t.Fatal("room deletion crossed its project boundary", err)
	}
}

func TestDeleteRejectsActiveWorldLeaseAndRetainedSnapshot(t *testing.T) {
	global, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer global.Close()
	db, err := global.WithProject("alpha")
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{Cfg: config.ProjectConfig{ID: "alpha"}, DB: db}
	room := model.Room{Project: "alpha", ID: "protected", Kind: "commit", State: "ready"}
	put := func() {
		t.Helper()
		if err := db.Put(t.Context(), "room", room.ID, room); err != nil {
			t.Fatal(err)
		}
	}
	conflict := func() {
		t.Helper()
		var failure *model.Error
		if err := m.Delete(t.Context(), room.ID); !errors.As(err, &failure) || failure.Code != "conflict" {
			t.Fatal("protected room deletion did not return conflict", err)
		}
	}
	put()
	conflict()
	room.State = "closed"
	put()
	if err = db.Put(t.Context(), "room_runtime", room.ID, Runtime{WorldExists: true}); err != nil {
		t.Fatal(err)
	}
	conflict()
	if err = db.Put(t.Context(), "room_runtime", room.ID, Runtime{}); err != nil {
		t.Fatal(err)
	}
	lease := model.LeaseRecord{Project: "alpha", RoomID: room.ID, Lease: model.Lease{ID: "lease", ExpiresAt: time.Now().Add(time.Minute)}}
	if err = db.Put(t.Context(), "lease", lease.ID, lease); err != nil {
		t.Fatal(err)
	}
	conflict()
	if err = db.Delete(t.Context(), "lease", lease.ID); err != nil {
		t.Fatal(err)
	}
	snapshot := model.Snapshot{Project: "alpha", ID: "protected-g1", RoomID: room.ID, CapturedAt: time.Now()}
	if err = db.Put(t.Context(), "snapshot", snapshot.ID, snapshot); err != nil {
		t.Fatal(err)
	}
	conflict()
	if err = db.Get(t.Context(), "snapshot", snapshot.ID, &snapshot); err != nil {
		t.Fatal("protected snapshot was changed", err)
	}
}
