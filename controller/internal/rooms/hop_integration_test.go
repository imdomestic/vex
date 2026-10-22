//go:build integration

package rooms

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/testfixture"
	"github.com/imdomestic/vex/controller/internal/testproject"
	"github.com/imdomestic/vex/controller/internal/worlds"
)

func TestRealWorldDestinationDistinguishesAvailableAndRegisteredRoom(t *testing.T) {
	scanner := os.Getenv("VEX_IT_SCANNER")
	if scanner == "" {
		t.Skip("requires the Vex world scanner")
	}
	fixture := testproject.NewFixture(t, "world-test")
	build, cfg, db := fixture.Build, fixture.Config, fixture.DB
	cfg.Worlds = config.EffectiveWorlds{Worlds: config.Worlds{ScannerFile: scanner}}
	worldManager, err := worlds.New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	metadata := model.WorldMetadata{ID: "fixture", Title: "Vex 固定测试世界", Minecraft: *build.Minecraft, Requires: model.Requirements{Loaders: []string{build.Loader}, Mods: []string{}}, Source: model.Source{Scale: 1}}
	policy := worlds.ManifestPolicy{SHA: build.SHA, Document: *build.Manifest, Commit: build.Commit}
	if _, _, err = worldManager.Publish(ctx, metadata, testfixture.File(t, "world-26.3.tar.gz"), policy); err != nil {
		t.Fatal(err)
	}
	world, err := worldManager.Get(ctx, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	manager := &Manager{Cfg: cfg, DB: db, Worlds: worldManager}
	available, err := manager.worldDestination(ctx, world, build)
	if err != nil || available.RoomID != nil || available.State != "available" || available.BuildID != build.ID {
		t.Fatalf("unregistered world destination: %+v %v", available, err)
	}
	id, err := RoomID("world", build.SHA, build.Loader, world.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("listing created a world room: %v", err)
	}
	room := manager.newRoom(id, "world", build.SHA, build.Loader, "world:"+world.ID)
	room.World = &world.ID
	if err = db.Put(ctx, "room", id, room); err != nil {
		t.Fatal(err)
	}
	registered, err := manager.worldDestination(ctx, world, build)
	if err != nil || registered.RoomID == nil || *registered.RoomID != id || registered.State != "stopped" {
		t.Fatalf("registered world destination: %+v %v", registered, err)
	}
}
