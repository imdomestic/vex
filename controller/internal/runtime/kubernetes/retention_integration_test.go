//go:build integration

package kubernetes_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/slp"
	"github.com/imdomestic/vex/controller/internal/store"
)

func TestRealClosedRoomPruningPreservesGenerationThroughTankArchive(t *testing.T) {
	h := newLifecycleHarness(t, "retention", true)
	var roomID string
	for generation := int64(1); generation <= 2; generation++ {
		room := h.startCommit()
		if room.Generation != generation || roomID != "" && roomID != room.ID {
			t.Fatal("recreated room did not preserve permanent generation")
		}
		roomID = room.ID
		response, err := slp.Probe(h.ctx, h.cfg.Server.RouterAddress, room.Host, room.Port)
		if err != nil || !slp.Matches(response, room) {
			t.Fatal("actual routed SLP identity mismatch", err)
		}
		room = h.stop(room.ID)
		if room.ClosedAt == nil {
			t.Fatal("normal closure omitted closedAt")
		}
		if err = h.manager.Archive.Step(h.ctx); err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("%s-g%d", roomID, generation)
		var snapshot model.Snapshot
		if err = h.db.Get(h.ctx, "snapshot", id, &snapshot); err != nil || snapshot.TankVerifiedAt == nil || snapshot.RoomGeneration != generation || snapshot.BuildID != h.manifest.ID || snapshot.Size == 0 {
			t.Fatal("actual snapshot did not complete tank verification", err)
		}
		tasks, err := store.All[model.Transfer](h.ctx, h.db, "transfer")
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range tasks {
			if task.Status != "verified" || task.VerifiedAt == nil {
				t.Fatal("actual transfer remains unverified")
			}
			t.Logf("preserved tank object=%s/%s sha256=%s size=%d", h.cfg.Archive.RsyncSSH.Target, task.RemotePath, task.SHA256, task.Size)
		}
		if generation == 1 {
			old := time.Now().UTC().Add(-31 * 24 * time.Hour)
			room.ClosedAt, snapshot.CapturedAt = &old, old
			if err = h.db.Update(h.ctx, func(tx *store.Tx) error {
				if err := tx.Put(h.ctx, "room", roomID, room); err != nil {
					return err
				}
				return tx.Put(h.ctx, "snapshot", snapshot.ID, snapshot)
			}); err != nil {
				t.Fatal(err)
			}
			if err = h.manager.PruneClosed(h.ctx); err != nil {
				t.Fatal(err)
			}
			var absent json.RawMessage
			for _, kind := range []string{"room", "room_runtime"} {
				if err = h.db.Get(h.ctx, kind, roomID, &absent); !errors.Is(err, store.ErrNotFound) {
					t.Fatal("expired room data remains", kind, err)
				}
			}
			var tombstone struct {
				Generation int64 `json:"generation"`
			}
			if err = h.db.Get(h.ctx, "room_tombstone", roomID, &tombstone); err != nil || tombstone.Generation != 1 {
				t.Fatal("permanent generation tombstone missing", err)
			}
			var preserved model.Snapshot
			if err = h.db.Get(h.ctx, "snapshot", id, &preserved); err != nil || preserved.SHA256 != snapshot.SHA256 || preserved.TankVerifiedAt == nil {
				t.Fatal("verified snapshot record changed during room retention", err)
			}
		}
	}
	snapshots, err := store.All[model.Snapshot](h.ctx, h.db, "snapshot")
	if err != nil || len(snapshots) != 2 || snapshots[0].ID == snapshots[1].ID {
		t.Fatal("two independent real snapshots are required", err)
	}
	t.Logf("actual generation archives retained: %s and %s", snapshots[0].ID, snapshots[1].ID)
}
