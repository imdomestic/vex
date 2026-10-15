//go:build integration

package archive

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
	"github.com/imdomestic/chorus-testbed/controller/internal/worldarchive"
)

func TestRealTankQueueContinuesAndRetriesIndependently(t *testing.T) {
	tank, identity, knownHosts := os.Getenv("CHORUS_IT_TANK"), os.Getenv("CHORUS_IT_IDENTITY"), os.Getenv("CHORUS_IT_KNOWN_HOSTS")
	world := os.Getenv("CHORUS_IT_WORLD")
	if tank == "" || identity == "" || knownHosts == "" || world == "" {
		t.Skip("requires real tank test directory, identity, known_hosts and plaza world")
	}
	host, remoteRoot, err := target(tank)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(remoteRoot, "/srv/archive/chorus/it-") || strings.Contains(strings.TrimPrefix(remoteRoot, "/srv/archive/chorus/it-"), "/") {
		t.Fatal("isolated tank test directory required")
	}
	source, err := filepath.EvalSymlinks(filepath.Join(world, "level.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(source, "/home/linwhite/d2-assets/sensitive/") {
		t.Fatal("sensitive source is forbidden")
	}
	root := t.TempDir()
	if err = os.MkdirAll(filepath.Join(root, ".work"), 0700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "queue.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if db != nil {
			db.Close()
		}
	}()
	cfg := config.Config{Server: config.Server{DataDir: root}, Archive: config.Archive{Enabled: true, TankTarget: tank, IdentityFile: identity, KnownHostsFile: knownHosts, TankBWLimit: "5M"}}
	m, err := New(cfg, db)
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
		task := model.Transfer{ID: name, LocalPath: name + ".dat", RemotePath: name + ".dat", SHA256: digest, Size: size, Status: "queued", CreatedAt: now, UpdatedAt: now}
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
	if err = m.checkRemote(ctx, host, path.Join(remoteRoot, second.RemotePath), digest, size); err != nil {
		t.Fatal(err)
	}
	copyArchiveFile(t, source, filepath.Join(root, "first.dat"))
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	m, err = New(cfg, db)
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
	if err = m.checkRemote(ctx, host, path.Join(remoteRoot, first.RemotePath), digest, size); err != nil {
		t.Fatal(err)
	}
	t.Logf("preserved tank files: %s/first.dat %s/second.dat; SHA-256 verified; failed transfer did not block later task; retry delay survived SQLite reopen", tank, tank)
}
