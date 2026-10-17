//go:build integration

package archive

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
	"github.com/imdomestic/vex/controller/internal/worlds"
)

func TestRealWorldTankVerificationWaitsForPackAndBackfills(t *testing.T) {
	realWorldVerification(t, true)
}

func TestRealWorldLocalVerificationWaitsForPackAndBackfills(t *testing.T) {
	realWorldVerification(t, false)
}

func realWorldVerification(t *testing.T, remote bool) {
	t.Helper()
	tank, identity, knownHosts := os.Getenv("VEX_IT_TANK"), os.Getenv("VEX_IT_IDENTITY"), os.Getenv("VEX_IT_KNOWN_HOSTS")
	scanner, source, metadataFile, packFile := os.Getenv("VEX_IT_SCANNER"), os.Getenv("VEX_IT_WORLD_ARCHIVE"), os.Getenv("VEX_IT_WORLD_METADATA"), os.Getenv("VEX_IT_PACK")
	if scanner == "" || source == "" || metadataFile == "" || packFile == "" || remote && (tank == "" || identity == "" || knownHosts == "") {
		t.Skip("requires actual scanner, world archive, metadata, pack and selected archive backend")
	}
	if remote {
		_, remoteRoot, err := target(tank)
		if err != nil || !strings.HasPrefix(remoteRoot, "/srv/archive/vex/it-") || strings.Contains(strings.TrimPrefix(remoteRoot, "/srv/archive/vex/it-"), "/") {
			t.Fatal("isolated Vex tank test directory required", err)
		}
	}
	for _, filename := range []string{source, metadataFile, packFile} {
		resolved, err := filepath.EvalSymlinks(filename)
		if err != nil || resolved == "/home/linwhite/d2-assets/sensitive" || strings.HasPrefix(resolved, "/home/linwhite/d2-assets/sensitive/") {
			t.Fatal("invalid or forbidden source", err)
		}
	}
	metadataBytes, err := os.ReadFile(metadataFile)
	if err != nil {
		t.Fatal(err)
	}
	var metadata model.WorldMetadata
	if err = json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	cfg := config.ProjectConfig{ID: "verification-test", DataDir: filepath.Join(root, "data", "projects", "verification-test"), Minecraft: config.ProjectMinecraft{Target: config.Target{Version: metadata.Minecraft}}, Loaders: map[string]config.Loader{}, Worlds: config.EffectiveWorlds{Worlds: config.Worlds{ScannerFile: scanner}}}
	for _, loader := range metadata.Requires.Loaders {
		cfg.Loaders[loader] = config.Loader{Enabled: true}
	}
	cfg.Archive.Archive = config.Archive{Backend: "local", Local: config.LocalArchive{Directory: filepath.Join(root, "archive")}}
	if remote {
		cfg.Archive.Archive = config.Archive{Backend: "rsync-ssh", RsyncSSH: config.RsyncSSH{Target: tank, IdentityFile: identity, KnownHostsFile: knownHosts, BWLimit: "5M"}}
	}
	db, err := openArchiveDatabase(filepath.Join(root, "state.db"), cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	worldManager, err := worlds.New(cfg, db.Store)
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(cfg, db.Store)
	if err != nil {
		t.Fatal(err)
	}
	m.Worlds = worldManager
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	pack, _, err := worldManager.PublishPack(ctx, "verification", packFile)
	if err != nil {
		t.Fatal(err)
	}
	metadata.ID = "verified-plaza"
	metadata.ResourcePack = &model.PackRef{ID: pack.ID, Rev: pack.Rev}
	revision, _, err := worldManager.Publish(ctx, metadata, source)
	if err != nil || revision.TankVerifiedAt != nil {
		t.Fatal("initial world publication failed", err)
	}
	metadataPath := filepath.Join(cfg.DataDir, worlds.MetadataPath(revision.ID, revision.Rev))
	originalDigest, originalSize, err := worldarchive.Digest(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	localPack := filepath.Join(cfg.DataDir, worlds.PackPath(pack.ID, pack.Rev))
	if err = os.Rename(localPack, localPack+".held"); err != nil {
		t.Fatal(err)
	}
	if err = m.Step(ctx); err == nil {
		t.Fatal("missing actual resource pack file did not fail its transfer")
	}
	revision, err = worldManager.Revision(ctx, revision.ID, revision.Rev)
	if err != nil || revision.TankVerifiedAt != nil {
		t.Fatal("world was verified before the referenced pack", err)
	}
	for _, part := range []string{"world", "metadata"} {
		var task model.Transfer
		if err = db.Get(ctx, "transfer", model.TransferID("world", revision.ID, revision.Rev, part), &task); err != nil || task.Status != "verified" || task.VerifiedAt == nil {
			t.Fatal("actual world or metadata transfer did not complete", err)
		}
	}
	if err = os.Rename(localPack+".held", localPack); err != nil {
		t.Fatal(err)
	}
	_, _, err = worldManager.SetState(ctx, revision.ID, "archived")
	var pending *model.Error
	if !errors.As(err, &pending) || pending.Code != "archive_failed" {
		t.Fatal("explicit retry did not remain pending for the pack", err)
	}
	if err = m.Step(ctx); err != nil {
		t.Fatal(err)
	}
	revision, err = worldManager.Revision(ctx, revision.ID, revision.Rev)
	if err != nil || revision.TankVerifiedAt == nil {
		t.Fatal("world did not acquire archive verification after all real transfers", err)
	}
	tasks, err := store.All[model.Transfer](ctx, db, "transfer")
	if err != nil || len(tasks) != 4 {
		t.Fatal("expected world, world metadata, pack ZIP and pack metadata", err)
	}
	var latest time.Time
	for _, task := range tasks {
		if task.Status != "verified" || task.VerifiedAt == nil {
			t.Fatal("a required archive transfer is incomplete")
		}
		if task.VerifiedAt.After(latest) {
			latest = *task.VerifiedAt
		}
	}
	if !revision.TankVerifiedAt.Equal(latest) {
		t.Fatal("world verification time differs from the final transfer")
	}
	revision.TankVerifiedAt = nil
	if err = db.Put(ctx, "world_revision", revision.ID+":"+revision.Rev, revision); err != nil {
		t.Fatal(err)
	}
	if err = m.Step(ctx); err != nil {
		t.Fatal(err)
	}
	revision, err = worldManager.Revision(ctx, revision.ID, revision.Rev)
	if err != nil || revision.TankVerifiedAt == nil || !revision.TankVerifiedAt.Equal(latest) {
		t.Fatal("existing verified transfer records were not backfilled", err)
	}
	if err = os.Remove(metadataPath); err != nil {
		t.Fatal(err)
	}
	again, created, err := worldManager.Publish(ctx, metadata, source)
	if err != nil || created || again.TankVerifiedAt == nil || !again.TankVerifiedAt.Equal(latest) {
		t.Fatal("idempotent publication changed verified revision", err)
	}
	digest, size, err := worldarchive.Digest(metadataPath)
	if err != nil || digest != originalDigest || size != originalSize {
		t.Fatal("verification changed immutable revision metadata", err)
	}
	t.Logf("real world, metadata and pack verified; incomplete pack blocked publication time; existing records backfilled; immutable metadata preserved; backend=%s", cfg.Archive.Backend)
}
