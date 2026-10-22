//go:build integration

package archive

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/testfixture"
	"github.com/imdomestic/vex/controller/internal/testproject"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
	"github.com/imdomestic/vex/controller/internal/worlds"
)

func realArchiveWorld(t *testing.T, source, destination string) {
	t.Helper()
	output, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewWriterLevel(output, gzip.NoCompression)
	if err != nil {
		t.Fatal(err)
	}
	bundle := tar.NewWriter(gz)
	err = filepath.WalkDir(source, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("world contains a link or special file")
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, filename)
		if err != nil {
			return err
		}
		header.Name = "world"
		if relative != "." {
			header.Name += "/" + filepath.ToSlash(relative)
		}
		if err = bundle.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		input, err := os.Open(filename)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(bundle, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = bundle.Close(); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err = output.Close(); err != nil {
		t.Fatal(err)
	}
	if err = worldarchive.Validate(destination, "26.3"); err != nil {
		t.Fatal(err)
	}
}

func copyArchiveFile(t *testing.T, source, destination string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		t.Fatal(copyErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func remoteArchiveSize(ctx context.Context, m *Manager, host, filename string) (int64, error) {
	command := "if test -f " + shellQuote(filename) + "; then stat -c %s -- " + shellQuote(filename) + "; else printf '0\\n'; fi"
	storage, ok := m.Storage.(SSHStore)
	if !ok {
		return 0, fmt.Errorf("partial transmission requires rsync-ssh storage")
	}
	arguments := append(storage.sshOptions(), host, command)
	output, err := exec.CommandContext(ctx, "ssh", arguments...).Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
}

func publishArchiveSignal(t *testing.T, directory, name string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(directory, name+".pending")
	if err = os.WriteFile(temporary, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(temporary, filepath.Join(directory, name)); err != nil {
		t.Fatal(err)
	}
}

func awaitArchiveSignal(t *testing.T, ctx context.Context, filename string) {
	t.Helper()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		info, err := os.Stat(filename)
		if err == nil && info.Size() > 0 {
			return
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}

type archiveDatabase struct {
	*store.Store
	owner *store.Store
}

func (db *archiveDatabase) Close() error { return db.owner.Close() }

func openArchiveDatabase(filename, project string) (*archiveDatabase, error) {
	owner, err := store.Open(filename)
	if err != nil {
		return nil, err
	}
	db, err := owner.WithProject(project)
	if err != nil {
		owner.Close()
		return nil, err
	}
	return &archiveDatabase{Store: db, owner: owner}, nil
}

func TestRealLocalSnapshotAndWorldRecovery(t *testing.T) {
	realArchiveLifecycle(t, false)
}

func TestRealTankInterruptedTransferSnapshotAndWorldRecovery(t *testing.T) {
	realArchiveLifecycle(t, true)
}

func realArchiveLifecycle(t *testing.T, remote bool) {
	t.Helper()
	scanner := os.Getenv("VEX_IT_SCANNER")
	tank, identity, knownHosts := os.Getenv("VEX_IT_TANK"), os.Getenv("VEX_IT_IDENTITY"), os.Getenv("VEX_IT_KNOWN_HOSTS")
	work, signals := os.Getenv("VEX_IT_WORK"), os.Getenv("VEX_IT_TAILSCALE_SIGNALS")
	if scanner == "" || work == "" || remote && (tank == "" || identity == "" || knownHosts == "") {
		t.Skip("requires the Vex world scanner, ignored workspace and selected archive backend")
	}
	if !filepath.IsAbs(work) || !strings.Contains(filepath.Clean(work), "/.work/") {
		t.Fatal("integration requires an absolute ignored workspace")
	}
	var host, remoteRoot string
	var err error
	if remote {
		host, remoteRoot, err = target(tank)
		if err != nil || !strings.HasPrefix(remoteRoot, "/srv/archive/vex/it-") || strings.Contains(strings.TrimPrefix(remoteRoot, "/srv/archive/vex/it-"), "/") {
			t.Fatal("tank destination must be an isolated Vex it directory", err)
		}
	} else if signals != "" {
		t.Fatal("network interruption signals require rsync-ssh storage")
	}
	if signals != "" {
		if !filepath.IsAbs(signals) || !strings.Contains(filepath.Clean(signals), "/.work/") {
			t.Fatal("network signals must remain in an ignored integration run directory")
		}
		if err = os.MkdirAll(signals, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(filepath.Join(signals, "transfer-ready.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("network signal directory must be unused")
		}
	}
	if err = os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(work, "archive-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("preserved test files: local=%s", root)
	if remote {
		t.Logf("dedicated remote archive: %s", tank)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	if err = worldarchive.Space(root); err != nil {
		t.Fatal(err)
	}
	world := testfixture.WorldDirectory(t, "world-26.3-large.tar.gz", filepath.Join(root, "source"))
	source := filepath.Join(root, "fixture.tar.gz")
	realArchiveWorld(t, world, source)
	digest, size, err := worldarchive.Digest(source)
	if err != nil {
		t.Fatal(err)
	}
	if remote && size < 512*1024 {
		t.Fatal("real world is too small to observe interrupted transmission")
	}
	cfg := config.ProjectConfig{ID: "archive-test", DataDir: filepath.Join(root, "data", "projects", "archive-test"), Minecraft: config.ProjectMinecraft{Target: config.Target{Version: "26.3"}}, Loaders: map[string]config.Loader{"fabric": {Enabled: true}}, Worlds: config.EffectiveWorlds{Worlds: config.Worlds{ScannerFile: scanner}}, Archive: config.EffectiveArchive{Archive: config.Archive{Backend: "local", Local: config.LocalArchive{Directory: filepath.Join(root, "archive")}}}}
	if remote {
		cfg.Archive.Archive = config.Archive{Backend: "rsync-ssh", RsyncSSH: config.RsyncSSH{Target: tank, BWLimit: "64K", IdentityFile: identity, KnownHostsFile: knownHosts}}
	}
	if err = os.MkdirAll(filepath.Join(cfg.DataDir, ".work"), 0700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, "state.db")
	db, err := openArchiveDatabase(database, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if db != nil {
			db.Close()
		}
	}()
	m, err := New(cfg, db.Store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	document, commit := testproject.Policy(t, filepath.Join(root, "project"))
	policy := worlds.ManifestPolicy{SHA: commit.SHA, Document: document, Commit: &commit}
	snapshot := model.Snapshot{Project: cfg.ID, ID: "fixture-capture", RoomID: "fixture-it", RoomGeneration: 1, BuildID: "fabric-" + commit.SHA[:12], Minecraft: "26.3", CapturedAt: now, CreatedAt: now, SHA256: digest, Size: size, LocalAvailable: true}
	snapshot.ManifestSHA, snapshot.WorldExtraRootEntries = commit.SHA, append([]string{}, document.World.ExtraRootEntries...)
	snapshot.Commit, snapshot.Manifest = &commit, &document
	relative := path.Join("snapshots", snapshot.RoomID, snapshot.ID+".tar.gz")
	snapshot.Path = relative
	local := filepath.Join(cfg.DataDir, filepath.FromSlash(relative))
	copyArchiveFile(t, source, local)
	err = db.Update(ctx, func(tx *store.Tx) error {
		if err := QueueSnapshot(ctx, tx, cfg.DataDir, snapshot); err != nil {
			return err
		}
		return tx.Put(ctx, "snapshot", snapshot.ID, snapshot)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.evictSnapshots(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(local); err != nil {
		t.Fatal("unverified snapshot was evicted", err)
	}
	var queued model.Transfer
	worldID := model.TransferID("snapshot", snapshot.ID, "", "world")
	if err = db.Get(ctx, "transfer", worldID, &queued); err != nil || queued.Status != "queued" || queued.Attempt != 0 {
		t.Fatalf("archive queue was not persisted: %+v %v", queued, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openArchiveDatabase(database, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	m, err = New(cfg, db.Store)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Get(ctx, "transfer", worldID, &queued); err != nil || queued.Status != "queued" {
		t.Fatal("queued transfer did not survive SQLite reopen", err)
	}
	expectedAttempt := 1
	if remote {
		expectedAttempt = 2
		transferCtx, stopTransfer := context.WithCancel(ctx)
		defer stopTransfer()
		finished := make(chan error, 1)
		go func() { finished <- m.Step(transferCtx) }()
		remoteWorld := path.Join(remoteRoot, "projects", cfg.ID, relative)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		var partial int64
	observeTransfer:
		for {
			select {
			case err = <-finished:
				t.Fatalf("transfer completed before interruption: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-ticker.C:
				partial, err = remoteArchiveSize(ctx, m, host, remoteWorld)
				if err != nil {
					t.Fatal(err)
				}
				if partial > 0 && partial < size {
					if signals == "" {
						stopTransfer()
					} else {
						publishArchiveSignal(t, signals, "transfer-ready.json", map[string]any{"partial": partial, "total": size, "tankRoot": remoteRoot, "transferID": worldID})
						t.Logf("actual partial transfer observed; external Tailscale interruption may proceed: %s", signals)
					}
					break observeTransfer
				}
			}
		}
		if err = <-finished; err == nil {
			t.Fatal("interrupted transfer reported success")
		}
		if err = db.Get(ctx, "transfer", worldID, &queued); err != nil {
			t.Fatal(err)
		}
		if queued.Status != "failed" || queued.Attempt != 1 || queued.VerifiedAt != nil || queued.LastError == nil {
			t.Fatalf("interruption was not persisted: %+v", queued)
		}
		if err = m.evictSnapshots(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(local); err != nil {
			t.Fatal("interrupted snapshot was evicted", err)
		}
		if signals != "" {
			if transferCtx.Err() != nil {
				t.Fatal("external network test must fail through the actual SSH transport", transferCtx.Err())
			}
			publishArchiveSignal(t, signals, "transfer-failed.json", map[string]any{"status": queued.Status, "attempt": queued.Attempt, "verifiedAt": queued.VerifiedAt, "localAvailable": true})
			awaitArchiveSignal(t, ctx, filepath.Join(signals, "restored"))
		}
		partial, err = remoteArchiveSize(ctx, m, host, remoteWorld)
		if err != nil || partial <= 0 || partial >= size {
			t.Fatalf("partial file was not retained: %d/%d %v", partial, size, err)
		}
		if signals == "" {
			t.Logf("cancelled only test transfer: retained partial=%d total=%d attempt=%d", partial, size, queued.Attempt)
		} else {
			t.Logf("actual Tailscale interruption persisted failure; restored network retained partial=%d total=%d attempt=%d", partial, size, queued.Attempt)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
		db, err = openArchiveDatabase(database, cfg.ID)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Archive.RsyncSSH.BWLimit = "16M"
		m, err = New(cfg, db.Store)
		if err != nil {
			t.Fatal(err)
		}
		var persisted model.Transfer
		if err = db.Get(ctx, "transfer", worldID, &persisted); err != nil || persisted.Status != "failed" || persisted.Attempt != 1 {
			t.Fatal("failed transfer did not survive SQLite reopen", err)
		}
		retry := time.NewTimer(time.Until(queued.UpdatedAt.Add(time.Minute + time.Millisecond)))
		defer retry.Stop()
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-retry.C:
		}
	}
	if err = m.Step(ctx); err != nil {
		t.Fatal(err)
	}
	var persisted model.Transfer
	if err = db.Get(ctx, "transfer", worldID, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Status != "verified" || persisted.Attempt != expectedAttempt || persisted.VerifiedAt == nil {
		t.Fatalf("resume failed: %+v", persisted)
	}
	if err = m.Storage.Verify(ctx, persisted.RemotePath, digest, size); err != nil {
		t.Fatal(err)
	}
	if err = m.Storage.Verify(ctx, persisted.RemotePath, strings.Repeat("0", 64), size); err == nil {
		t.Fatal("incorrect remote checksum accepted")
	}
	if err = m.Storage.Verify(ctx, persisted.RemotePath, digest, size+1); err == nil {
		t.Fatal("incorrect remote size accepted")
	}
	if err = db.Get(ctx, "snapshot", snapshot.ID, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.TankVerifiedAt == nil || snapshot.LocalAvailable {
		t.Fatalf("verified snapshot retention did not evict: %+v", snapshot)
	}
	if _, err = os.Stat(local); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired verified snapshot remains local: %v", err)
	}
	cfg.Archive.KeepPerRoom, cfg.Archive.KeepDays = 5, 14
	m, err = New(cfg, db.Store)
	if err != nil {
		t.Fatal(err)
	}
	restored, accepted, err := m.RequestSnapshotRestore(ctx, snapshot.ID)
	if err != nil || !accepted || restored.Restore == nil || restored.Restore.Attempt != 1 {
		t.Fatalf("restore request: %+v %t %v", restored, accepted, err)
	}
	repeated, accepted, err := m.RequestSnapshotRestore(ctx, snapshot.ID)
	if err != nil || !accepted || repeated.Restore.Attempt != 1 {
		t.Fatal("restore did not reuse queued attempt", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openArchiveDatabase(database, cfg.ID)
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
	if err = db.Get(ctx, "snapshot", snapshot.ID, &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.LocalAvailable || restored.Restore.Status != "succeeded" || restored.Restore.Attempt != 1 {
		t.Fatalf("snapshot restore: %+v", restored)
	}
	actual, actualSize, err := worldarchive.Digest(local)
	if err != nil || actual != digest || actualSize != size {
		t.Fatal("restored snapshot changed", err)
	}
	t.Logf("snapshot archive verified; actual SQLite reopen, archive checksum/size, retention and restore passed")
	worldManager, err := worlds.New(cfg, db.Store)
	if err != nil {
		t.Fatal(err)
	}
	m.Worlds = worldManager
	metadata := model.WorldMetadata{ID: "fixture", Title: "Vex 固定测试世界", Minecraft: "26.3", Requires: model.Requirements{Loaders: []string{"fabric"}, Namespaces: []string{}}, Source: model.Source{Scale: 1}}
	revision, created, err := worldManager.Publish(ctx, metadata, source, policy)
	if err != nil || !created || revision.Rev != "1" {
		t.Fatalf("real world publish: %+v %t %v", revision, created, err)
	}
	_, _, err = worldManager.SetState(ctx, "fixture", "archived")
	var failure *model.Error
	if !errors.As(err, &failure) || failure.Code != "archive_failed" {
		t.Fatalf("unverified world archive accepted: %v", err)
	}
	if err = worldManager.EvictArchived(ctx); err != nil {
		t.Fatal(err)
	}
	worldLocal := filepath.Join(cfg.DataDir, worlds.WorldPath("fixture", "1"))
	if _, err = os.Stat(worldLocal); err != nil {
		t.Fatal("unverified world was evicted", err)
	}
	if err = m.Step(ctx); err != nil {
		t.Fatal(err)
	}
	archived, _, err := worldManager.SetState(ctx, "fixture", "archived")
	if err != nil || archived.State != "archived" {
		t.Fatalf("verified world archive: %+v %v", archived, err)
	}
	opened, err := worldManager.OpenWorld(ctx, "fixture", "1")
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if err = m.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(worldLocal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verified archived world not evicted: %v", err)
	}
	hash := sha256.New()
	count, err := io.Copy(hash, opened)
	if err != nil || count != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		t.Fatal("open world download lost bytes during local eviction", err)
	}
	requested, accepted, err := worldManager.RequestRestore(ctx, "fixture")
	if err != nil || !accepted || requested.State != "archived" || requested.Restore.Attempt != 1 {
		t.Fatalf("world restore request: %+v %t %v", requested, accepted, err)
	}
	requested, accepted, err = worldManager.RequestRestore(ctx, "fixture")
	if err != nil || !accepted || requested.Restore.Attempt != 1 {
		t.Fatal("world restore attempt was duplicated", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openArchiveDatabase(database, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	m, err = New(cfg, db.Store)
	if err != nil {
		t.Fatal(err)
	}
	m.Worlds, err = worlds.New(cfg, db.Store)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Step(ctx); err != nil {
		t.Fatal(err)
	}
	active, err := m.Worlds.Get(ctx, "fixture")
	if err != nil || active.State != "active" || active.Restore.Status != "succeeded" || active.Restore.Attempt != 1 {
		t.Fatalf("world restore: %+v %v", active, err)
	}
	actual, actualSize, err = worldarchive.Digest(worldLocal)
	if err != nil || actual != digest || actualSize != size {
		t.Fatal("restored world differs from published content", err)
	}
	transfers, err := store.All[model.Transfer](ctx, db, "transfer")
	if err != nil || len(transfers) != 4 {
		t.Fatalf("expected four actual world/metadata transfers: %d %v", len(transfers), err)
	}
	for _, task := range transfers {
		if task.Status != "verified" || task.VerifiedAt == nil {
			t.Fatalf("task is not verified: %+v", task)
		}
		if err = m.Storage.Verify(ctx, task.RemotePath, task.SHA256, task.Size); err != nil {
			t.Fatal(err)
		}
		t.Logf("preserved archive object: backend=%s key=%s sha256=%s bytes=%d", cfg.Archive.Backend, task.RemotePath, task.SHA256, task.Size)
	}
	if _, err = os.Stat(filepath.Join(world, "level.dat")); err != nil {
		t.Fatal("source world changed", err)
	}
	t.Log("real world publish, verified archive, open download, local eviction and persisted restore passed")
}
