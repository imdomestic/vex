package events

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

func TestReconnectStartsWithCurrentSync(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	project, err := db.WithProject("example")
	if err != nil {
		t.Fatal(err)
	}
	room := model.Room{Project: "example", ID: "main-fabric", BuildID: "fabric-123456789012", Generation: 2, State: "starting", Players: []model.Player{}}
	if err = project.Put(ctx, "room", room.ID, room); err != nil {
		t.Fatal(err)
	}
	bus := New(db, "example")
	first, ch, cancel, err := bus.Subscribe(ctx, []string{"example"}, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != "sync" {
		t.Fatal(first.Name)
	}
	room.State = "ready"
	if err = project.Put(ctx, "room", room.ID, room); err != nil {
		t.Fatal(err)
	}
	if err = bus.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if event := <-ch; event.Name != "room" {
		t.Fatal(event.Name)
	}
	if event := <-ch; event.Name != "ready" {
		t.Fatal(event.Name)
	}
	cancel()
	first, _, cancel, err = bus.Subscribe(ctx, []string{"example"}, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	var sync model.Sync
	if err = json.Unmarshal(first.Data, &sync); err != nil {
		t.Fatal(err)
	}
	if first.Name != "sync" || len(sync.Rooms) != 1 || sync.Rooms[0].State != "ready" || sync.Rooms[0].Generation != 2 {
		t.Fatal("reconnect did not carry current ready level")
	}
}

func TestProjectIsolationAndDeletedRoomDoesNotInterruptPolling(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	alpha, err := db.WithProject("alpha")
	if err != nil {
		t.Fatal(err)
	}
	beta, err := db.WithProject("beta")
	if err != nil {
		t.Fatal(err)
	}
	room := model.Room{Project: "alpha", ID: "main-fabric", BuildID: "fabric-123456789012", State: "starting"}
	if err = alpha.Put(ctx, "room", room.ID, room); err != nil {
		t.Fatal(err)
	}
	other := room
	other.Project = "beta"
	if err = beta.Put(ctx, "room", other.ID, other); err != nil {
		t.Fatal(err)
	}
	bus := New(db, "alpha", "beta")
	first, ch, cancel, err := bus.Subscribe(ctx, []string{"alpha"}, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	var snapshot model.Sync
	if err = json.Unmarshal(first.Data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Rooms) != 1 || snapshot.Rooms[0].Project != "alpha" {
		t.Fatal("sync leaked another project")
	}
	if err = bus.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if event := <-ch; event.Project != "alpha" || event.Name != "room" {
		t.Fatal(event.Name, event.Project)
	}
	if err = alpha.Delete(ctx, "room", room.ID); err != nil {
		t.Fatal(err)
	}
	build := model.Build{Project: "alpha", ID: room.BuildID, Status: "ready"}
	if err = alpha.Put(ctx, "build", build.ID, build); err != nil {
		t.Fatal(err)
	}
	if err = bus.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	gotDeletion := false
	for len(ch) > 0 {
		event := <-ch
		if event.Project != "alpha" {
			t.Fatal("event leaked another project")
		}
		if event.Name == "room_deleted" {
			gotDeletion = true
		}
	}
	if !gotDeletion {
		t.Fatal("room deletion was not published")
	}
	if err = alpha.Delete(ctx, "build", build.ID); err != nil {
		t.Fatal(err)
	}
	if err = bus.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if len(bus.last) != 1 {
		t.Fatal("deleted records remain in event state")
	}
}
