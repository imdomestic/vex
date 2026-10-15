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

	"github.com/imdomestic/chorus-testbed/controller/internal/assets"
	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
	"github.com/imdomestic/chorus-testbed/controller/internal/worldarchive"
)

func TestRealAssetTankVerificationWaitsForPackAndBackfills(t *testing.T) {
	tank, identity, knownHosts := os.Getenv("CHORUS_IT_TANK"), os.Getenv("CHORUS_IT_IDENTITY"), os.Getenv("CHORUS_IT_KNOWN_HOSTS")
	configFile, source, metadataFile, packFile := os.Getenv("CHORUS_IT_ROOMS_CONFIG"), os.Getenv("CHORUS_IT_ASSET_ARCHIVE"), os.Getenv("CHORUS_IT_ASSET_METADATA"), os.Getenv("CHORUS_IT_PACK")
	if tank == "" || identity == "" || knownHosts == "" || configFile == "" || source == "" || metadataFile == "" || packFile == "" {
		t.Skip("requires real tank identity, configuration, world archive, metadata and resource pack")
	}
	_, remoteRoot, err := target(tank)
	if err != nil || !strings.HasPrefix(remoteRoot, "/srv/archive/chorus/it-") || strings.Contains(strings.TrimPrefix(remoteRoot, "/srv/archive/chorus/it-"), "/") {
		t.Fatal("isolated tank test directory required", err)
	}
	for _, filename := range []string{source, metadataFile, packFile} {
		resolved, err := filepath.EvalSymlinks(filename)
		if err != nil || resolved == "/home/linwhite/d2-assets/sensitive" || strings.HasPrefix(resolved, "/home/linwhite/d2-assets/sensitive/") {
			t.Fatal("invalid or forbidden source", err)
		}
	}
	cfg, err := config.Load(configFile)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	cfg.Server.DataDir = root
	cfg.Archive.Enabled, cfg.Archive.TankTarget = true, tank
	cfg.Archive.IdentityFile, cfg.Archive.KnownHostsFile, cfg.Archive.TankBWLimit = identity, knownHosts, "5M"
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assetManager, err := assets.New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	m.Assets = assetManager
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	pack, _, err := assetManager.PublishPack(ctx, "round3-verification", packFile)
	if err != nil {
		t.Fatal(err)
	}
	metadataBytes, err := os.ReadFile(metadataFile)
	if err != nil {
		t.Fatal(err)
	}
	var metadata model.AssetMetadata
	if err = json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatal(err)
	}
	metadata.ID = "verified-plaza"
	metadata.ResourcePack = &model.PackRef{ID: pack.ID, Rev: pack.Rev}
	revision, _, err := assetManager.Publish(ctx, metadata, source)
	if err != nil || revision.TankVerifiedAt != nil {
		t.Fatal("initial asset publication failed", err)
	}
	metadataPath := filepath.Join(root, assets.MetadataPath(revision.ID, revision.Rev))
	originalDigest, originalSize, err := worldarchive.Digest(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	localPack := filepath.Join(root, assets.PackPath(pack.ID, pack.Rev))
	if err = os.Rename(localPack, localPack+".held"); err != nil {
		t.Fatal(err)
	}
	if err = m.Step(ctx); err == nil {
		t.Fatal("missing actual resource pack file did not fail its transfer")
	}
	revision, err = assetManager.Revision(ctx, revision.ID, revision.Rev)
	if err != nil || revision.TankVerifiedAt != nil {
		t.Fatal("asset was verified before the referenced pack", err)
	}
	for _, part := range []string{"world", "metadata"} {
		var task model.Transfer
		if err = db.Get(ctx, "transfer", model.TransferID("asset", revision.ID, revision.Rev, part), &task); err != nil || task.Status != "verified" || task.VerifiedAt == nil {
			t.Fatal("actual world or metadata transfer did not complete", err)
		}
	}
	if err = os.Rename(localPack+".held", localPack); err != nil {
		t.Fatal(err)
	}
	_, _, err = assetManager.SetState(ctx, revision.ID, "archived")
	var pending *model.Error
	if !errors.As(err, &pending) || pending.Code != "archive_failed" {
		t.Fatal("explicit retry did not remain pending for the pack", err)
	}
	if err = m.Step(ctx); err != nil {
		t.Fatal(err)
	}
	revision, err = assetManager.Revision(ctx, revision.ID, revision.Rev)
	if err != nil || revision.TankVerifiedAt == nil {
		t.Fatal("asset did not acquire tank verification after all real transfers", err)
	}
	tasks, err := store.All[model.Transfer](ctx, db, "transfer")
	if err != nil || len(tasks) != 4 {
		t.Fatal("expected world, asset metadata, pack ZIP and pack metadata", err)
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
		t.Fatal("asset verification time differs from the final transfer")
	}
	revision.TankVerifiedAt = nil
	if err = db.Put(ctx, "asset_revision", revision.ID+":"+revision.Rev, revision); err != nil {
		t.Fatal(err)
	}
	if err = m.Step(ctx); err != nil {
		t.Fatal(err)
	}
	revision, err = assetManager.Revision(ctx, revision.ID, revision.Rev)
	if err != nil || revision.TankVerifiedAt == nil || !revision.TankVerifiedAt.Equal(latest) {
		t.Fatal("existing verified transfer records were not backfilled", err)
	}
	if err = os.Remove(metadataPath); err != nil {
		t.Fatal(err)
	}
	again, created, err := assetManager.Publish(ctx, metadata, source)
	if err != nil || created || again.TankVerifiedAt == nil || !again.TankVerifiedAt.Equal(latest) {
		t.Fatal("idempotent publication changed verified revision", err)
	}
	digest, size, err := worldarchive.Digest(metadataPath)
	if err != nil || digest != originalDigest || size != originalSize {
		t.Fatal("verification changed immutable revision metadata", err)
	}
	t.Logf("real world, metadata and pack verified; incomplete pack blocked publication time; existing records backfilled; immutable metadata preserved; tank files retained under %s", tank)
}
