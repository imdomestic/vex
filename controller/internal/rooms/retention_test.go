package rooms

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
)

func TestClosedRoomRetentionAndPermanentGeneration(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := &Manager{DB: db}
	now := time.Now().UTC()
	old, recent := now.Add(-31*24*time.Hour), now.Add(-29*24*time.Hour)
	for _, item := range []struct {
		id, kind string
		closed   time.Time
	}{
		{"expired", "commit", old}, {"asset", "asset", old}, {"recent", "commit", recent},
		{"branch", "branch", old}, {"retained", "commit", old}, {"seed-source", "commit", old},
		{"world", "commit", old},
	} {
		room := model.Room{ID: item.id, Kind: item.kind, SHA: "0123456789012345678901234567890123456789", Loader: "fabric", State: "closed", Generation: 7, ClosedAt: &item.closed}
		if err = db.Put(ctx, "room", room.ID, room); err != nil {
			t.Fatal(err)
		}
		if err = db.Put(ctx, "room_runtime", room.ID, Runtime{WorldExists: item.id == "world"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, snapshot := range []model.Snapshot{
		{ID: "retained-g7", RoomID: "retained", CapturedAt: now},
		{ID: "seed-source-g7", RoomID: "seed-source", CapturedAt: old, TankVerifiedAt: &old},
		{ID: "expired-g7", RoomID: "expired", CapturedAt: old, TankVerifiedAt: &old},
	} {
		if err = db.Put(ctx, "snapshot", snapshot.ID, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	active := model.Room{ID: "running", Kind: "commit", State: "ready", SeedResolved: &model.SeedResolved{Kind: "snapshot", ID: "seed-source-g7"}}
	if err = db.Put(ctx, "room", active.ID, active); err != nil {
		t.Fatal(err)
	}
	lease := model.LeaseRecord{Lease: model.Lease{ID: "expired-lease", Generation: 7, ExpiresAt: old}, RoomID: "expired"}
	if err = db.Put(ctx, "lease", lease.ID, lease); err != nil {
		t.Fatal(err)
	}
	if err = m.PruneClosed(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"expired", "asset"} {
		for _, kind := range []string{"room", "room_runtime"} {
			var value any
			if err = db.Get(ctx, kind, id, &value); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("%s %s remained after retention: %v", kind, id, err)
			}
		}
	}
	for _, id := range []string{"recent", "branch", "retained", "seed-source", "world", "running"} {
		if _, err = m.Get(ctx, id); err != nil {
			t.Fatalf("protected room %s was removed: %v", id, err)
		}
	}
	var value any
	if err = db.Get(ctx, "lease", lease.ID, &value); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired room lease remained: %v", err)
	}
	if err = db.Get(ctx, "snapshot", "expired-g7", &value); err != nil {
		t.Fatal("room cleanup changed snapshot metadata", err)
	}
	room := model.Room{ID: "expired", Kind: "commit", SHA: "0123456789012345678901234567890123456789", Loader: "fabric"}
	if err = m.restoreGeneration(ctx, &room); err != nil || room.Generation != 7 {
		t.Fatal("generation was not recovered from permanent tombstone", err)
	}
	room.Generation = 8
	room.State, room.ClosedAt = "closed", &old
	if err = db.Put(ctx, "room", room.ID, room); err != nil {
		t.Fatal(err)
	}
	if err = m.PruneClosed(ctx); err != nil {
		t.Fatal(err)
	}
	room.Generation = 0
	if err = m.restoreGeneration(ctx, &room); err != nil || room.Generation != 8 {
		t.Fatal("second cleanup restarted generation numbering", err)
	}
	room.SHA = "1123456789012345678901234567890123456789"
	var conflict *model.Error
	if err = m.restoreGeneration(ctx, &room); !errors.As(err, &conflict) || conflict.Code != "conflict" {
		t.Fatal("tombstone allowed a different full commit to reuse the room ID", err)
	}
}

func TestClosedAtMigrationAndStateTransitions(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := &Manager{DB: db}
	old := time.Now().UTC().Add(-48 * time.Hour)
	room := model.Room{ID: "history", State: "closed", UpdatedAt: old}
	if err = db.Put(ctx, "room", room.ID, room); err != nil {
		t.Fatal(err)
	}
	if err = m.InitializeRecords(ctx); err != nil {
		t.Fatal(err)
	}
	room, err = m.Get(ctx, room.ID)
	if err != nil || room.ClosedAt == nil || !room.ClosedAt.Equal(old) || !room.UpdatedAt.Equal(old) {
		t.Fatal("historical close time changed during migration", err)
	}
	room.Host = "history.chorus.internal"
	if err = m.save(ctx, room, Runtime{}); err != nil {
		t.Fatal(err)
	}
	room, err = m.Get(ctx, room.ID)
	if err != nil || room.ClosedAt == nil || !room.ClosedAt.Equal(old) {
		t.Fatal("metadata update changed close time", err)
	}
	room.State = "queued"
	if err = m.save(ctx, room, Runtime{}); err != nil {
		t.Fatal(err)
	}
	room, err = m.Get(ctx, room.ID)
	if err != nil || room.ClosedAt != nil {
		t.Fatal("new lifecycle retained previous close time", err)
	}
	room.State = "closed"
	if err = m.save(ctx, room, Runtime{}); err != nil {
		t.Fatal(err)
	}
	room, err = m.Get(ctx, room.ID)
	if err != nil || room.ClosedAt == nil || !room.ClosedAt.After(old) {
		t.Fatal("new close time was not recorded", err)
	}
}
