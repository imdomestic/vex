//go:build integration

package archive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/testfixture"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
)

func TestRealTankQueueContinuesAndRetriesIndependently(t *testing.T) {
	realArchiveQueue(t, true)
}

func TestRealLocalQueueContinuesAndRetriesIndependently(t *testing.T) {
	realArchiveQueue(t, false)
}

func realArchiveQueue(t *testing.T, remote bool) {
	t.Helper()
	tank, identity, knownHosts := os.Getenv("VEX_IT_TANK"), os.Getenv("VEX_IT_IDENTITY"), os.Getenv("VEX_IT_KNOWN_HOSTS")
	if remote && (tank == "" || identity == "" || knownHosts == "") {
		t.Skip("requires credentials for the selected archive backend")
	}
	if remote {
		_, remoteRoot, err := target(tank)
		if err != nil || !strings.HasPrefix(remoteRoot, "/srv/archive/vex/it-") || strings.Contains(strings.TrimPrefix(remoteRoot, "/srv/archive/vex/it-"), "/") {
			t.Fatal("isolated Vex tank test directory required", err)
		}
	}
	root := t.TempDir()
	world := testfixture.WorldDirectory(t, "world-26.3.tar.gz", filepath.Join(root, "fixture"))
	source := filepath.Join(world, "level.dat")
	if err := os.MkdirAll(filepath.Join(root, ".work"), 0700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "queue.db")
	db, err := openArchiveDatabase(dbPath, "queue-test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if db != nil {
			db.Close()
		}
	}()
	cfg := config.ProjectConfig{ID: "queue-test", DataDir: root, Archive: config.EffectiveArchive{Archive: config.Archive{Backend: "local", Local: config.LocalArchive{Directory: filepath.Join(root, "archive")}}}}
	if remote {
		cfg.Archive.Archive = config.Archive{Backend: "rsync-ssh", RsyncSSH: config.RsyncSSH{Target: tank, IdentityFile: identity, KnownHostsFile: knownHosts, BWLimit: "5M"}}
	}
	m, err := New(cfg, db.Store)
	if err != nil {
		t.Fatal(err)
	}
	copyArchiveFile(t, source, filepath.Join(root, "second.dat"))
	digest, size, err := worldarchive.Digest(source)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	now := time.Now().UTC()
	for _, name := range []string{"first", "second"} {
		task := model.Transfer{Project: cfg.ID, ID: name, LocalPath: name + ".dat", RemotePath: "projects/" + cfg.ID + "/" + name + ".dat", SHA256: digest, Size: size, Status: "queued", CreatedAt: now, UpdatedAt: now}
		if err = db.Put(ctx, "transfer", task.ID, task); err != nil {
			t.Fatal(err)
		}
	}
	if err = m.Step(ctx); err == nil {
		t.Fatal("missing local file did not report failure")
	}
	var first, second model.Transfer
	if err = db.Get(ctx, "transfer", "first", &first); err != nil {
		t.Fatal(err)
	}
	if err = db.Get(ctx, "transfer", "second", &second); err != nil {
		t.Fatal(err)
	}
	if first.Status != "failed" || first.Attempt != 1 || second.Status != "verified" || second.VerifiedAt == nil {
		t.Fatalf("queue states: first=%s/%d second=%s", first.Status, first.Attempt, second.Status)
	}
	if err = m.Storage.Verify(ctx, second.RemotePath, digest, size); err != nil {
		t.Fatal(err)
	}
	copyArchiveFile(t, source, filepath.Join(root, "first.dat"))
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openArchiveDatabase(dbPath, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	m, err = New(cfg, db.Store)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Step(ctx); err != nil {
		t.Fatal(err)
	}
	var waiting model.Transfer
	if err = db.Get(ctx, "transfer", "first", &waiting); err != nil {
		t.Fatal(err)
	}
	if waiting.Status != "failed" || waiting.Attempt != 1 {
		t.Fatal("retry delay was not preserved across database reopen")
	}
	timer := time.NewTimer(time.Until(first.UpdatedAt.Add(time.Minute + time.Millisecond)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
	if err = m.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.Get(ctx, "transfer", "first", &first); err != nil {
		t.Fatal(err)
	}
	if err = db.Get(ctx, "transfer", "second", &second); err != nil {
		t.Fatal(err)
	}
	if first.Status != "verified" || first.Attempt != 2 || second.Attempt != 1 {
		t.Fatal("independent retry attempts did not match")
	}
	if err = m.Storage.Verify(ctx, first.RemotePath, digest, size); err != nil {
		t.Fatal(err)
	}
	t.Logf("archive backend=%s verified keys %s and %s; failed transfer did not block later task; retry delay survived SQLite reopen", cfg.Archive.Backend, first.RemotePath, second.RemotePath)
}
