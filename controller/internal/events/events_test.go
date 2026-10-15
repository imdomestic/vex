package events

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
)

func TestReconnectStartsWithCurrentSync(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	room := model.Room{ID: "main-fabric", BuildID: "fabric-123456789012", Generation: 2, State: "starting", Players: []model.Player{}}
	if err = db.Put(ctx, "room", room.ID, room); err != nil {
		t.Fatal(err)
	}
	bus := New(db)
	first, ch, cancel, err := bus.Subscribe(ctx, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != "sync" {
		t.Fatal(first.Name)
	}
	room.State = "ready"
	if err = db.Put(ctx, "room", room.ID, room); err != nil {
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
	first, _, cancel, err = bus.Subscribe(ctx, room.ID)
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
