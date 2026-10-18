//go:build integration

package rooms

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/testfixture"
	"github.com/imdomestic/vex/controller/internal/worlds"
)

func TestRealWorldDestinationDistinguishesAvailableAndRegisteredRoom(t *testing.T) {
	archive, metadataPath, manifestPath, scanner := os.Getenv("VEX_IT_WORLD_ARCHIVE"), os.Getenv("VEX_IT_WORLD_METADATA"), os.Getenv("VEX_IT_BUILD"), os.Getenv("VEX_IT_SCANNER")
	if archive == "" || metadataPath == "" || manifestPath == "" || scanner == "" {
		t.Skip("requires real published plaza metadata/archive, ready build manifest and scanner")
	}
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	var metadata model.WorldMetadata
	if err = json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var build model.Build
	if err = json.Unmarshal(manifestBytes, &build); err != nil {
		t.Fatal(err)
	}
	if build.Status != "ready" || build.Minecraft == nil {
		t.Fatal("requires a real ready build")
	}
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db, err = db.WithProject("world-test")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.ProjectConfig{ID: "world-test", DataDir: filepath.Join(root, "data"), Minecraft: config.ProjectMinecraft{Target: config.Target{Version: *build.Minecraft}}, Worlds: config.EffectiveWorlds{Worlds: config.Worlds{ScannerFile: scanner}}}
	cfg.Loaders = map[string]config.Loader{build.Loader: {Enabled: true}}
	worldManager, err := worlds.New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	document, manifestSHA := testfixture.Manifest(t)
	if _, _, err = worldManager.Publish(ctx, metadata, archive, worlds.ManifestPolicy{SHA: manifestSHA, Document: document}); err != nil {
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
