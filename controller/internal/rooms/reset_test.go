package rooms

import (
	"errors"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/model"
)

func TestResetWorldRejectsUnmetPreconditionsBeforeTouchingTheWorld(t *testing.T) {
	m, _, sha := freshnessManager(t)
	room, err := m.Get(t.Context(), "main-fabric")
	if err != nil {
		t.Fatal(err)
	}
	room.Generation, room.State, room.Phase, room.Label = 3, "stopped", "stopped", "testing"
	rt := Runtime{Generation: 3, WorldExists: true}
	if err = m.save(t.Context(), room, rt); err != nil {
		t.Fatal(err)
	}
	commit, _, err := m.Create(t.Context(), "commit", sha, "fabric", "empty", "")
	if err != nil {
		t.Fatal(err)
	}
	reject := func(id, confirm, code string) {
		t.Helper()
		_, err := m.ResetWorld(t.Context(), id, confirm, "")
		var failure *model.Error
		if !errors.As(err, &failure) || failure.Code != code {
			t.Fatalf("reset %s with %q: %v, expected %s", id, confirm, err, code)
		}
	}
	reject("main-fabric", "main-fabri", "invalid_request")
	reject(commit.ID, commit.ID, "conflict")
	reject("main-fabric", "main-fabric", "conflict")
	room.Label = "finished"
	room.State = "ready"
	if err = m.save(t.Context(), room, rt); err != nil {
		t.Fatal(err)
	}
	reject("main-fabric", "main-fabric", "conflict")
	room.State = "closed"
	if err = m.save(t.Context(), room, Runtime{Generation: 3}); err != nil {
		t.Fatal(err)
	}
	reject("main-fabric", "main-fabric", "conflict")
	if err = m.save(t.Context(), room, rt); err != nil {
		t.Fatal(err)
	}
	// 只有较早 generation 的快照通过校验，最新 generation 的快照尚未经过 tank 校验。
	now := time.Now().UTC()
	for _, snapshot := range []model.Snapshot{
		{Project: m.Cfg.ID, ID: "main-fabric-g2", RoomID: "main-fabric", RoomGeneration: 2, TankVerifiedAt: &now},
		{Project: m.Cfg.ID, ID: "main-fabric-g3", RoomID: "main-fabric", RoomGeneration: 3},
	} {
		if err = m.DB.Put(t.Context(), "snapshot", snapshot.ID, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	reject("main-fabric", "main-fabric", "conflict")
	unchanged, err := m.runtime(t.Context(), "main-fabric")
	if err != nil || !unchanged.WorldExists {
		t.Fatal("rejected reset changed the retained world state", err)
	}
}
