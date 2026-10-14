package build

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/gitmirror"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
)

func repositoryCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "user.name=Chorus Integration", "-c", "user.email=chorus@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil { t.Fatalf("git %v: %v: %s", args, err, output) }
	return strings.TrimSpace(string(output))
}

func realManager(t *testing.T) (*Manager, []string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	repositoryCommand(t, root, "init", "--initial-branch=main", source)
	commits := []string{}
	for _, content := range []string{"initial content", "second content", "third content"} {
		if err := os.WriteFile(filepath.Join(source, "data.txt"), []byte(content), 0600); err != nil { t.Fatal(err) }
		repositoryCommand(t, source, "add", "data.txt")
		repositoryCommand(t, source, "commit", "-m", content)
		commits = append(commits, repositoryCommand(t, source, "rev-parse", "HEAD"))
	}
	mirror, err := gitmirror.New(source, filepath.Join(root, "mirror.git"))
	if err != nil { t.Fatal(err) }
	if err = mirror.Refresh(context.Background()); err != nil { t.Fatal(err) }
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { if err := db.Close(); err != nil { t.Error(err) } })
	cfg := config.Config{
		Server: config.Server{DataDir: filepath.Join(root, "data")},
		Loaders: map[string]config.Loader{"fabric": {Enabled: true}},
		Build: config.Build{CacheSize: 1, Timeout: "1m", JDKHome: root, LockFile: filepath.Join(root, "build.lock")},
	}
	if err = os.WriteFile(cfg.Build.LockFile, nil, 0444); err != nil { t.Fatal(err) }
	manager, err := New(cfg, db, mirror)
	if err != nil { t.Fatal(err) }
	return manager, commits
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var failure *model.Error
	if !errors.As(err, &failure) || failure.Code != code { t.Fatalf("expected %s, received %v", code, err) }
}

func TestRequestSerializesAttemptsAndResetsMetadata(t *testing.T) {
	m, commits := realManager(t)
	ctx := context.Background()
	var wait sync.WaitGroup
	created := make(chan bool, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, fresh, err := m.Request(ctx, commits[0], "fabric", false)
			if err != nil { t.Error(err); return }
			if value.Attempt != 1 || value.Status != "queued" { t.Errorf("unexpected attempt %+v", value) }
			created <- fresh
		}()
	}
	wait.Wait()
	close(created)
	count := 0
	for fresh := range created { if fresh { count++ } }
	if count != 1 { t.Fatalf("created %d concurrent attempts", count) }
	value, _, err := m.Request(ctx, commits[0], "fabric", true)
	if err != nil || value.Attempt != 1 { t.Fatalf("queued retry: %+v %v", value, err) }
	log, err := m.OpenLog(ctx, value.ID)
	if err != nil { t.Fatal(err) }
	contents, err := io.ReadAll(log)
	log.Close()
	if err != nil || len(contents) != 0 { t.Fatalf("queued log: %q %v", contents, err) }
	version, loader, java := "26.3", "0.19.5", 25
	value.Status, value.Minecraft, value.LoaderVersion, value.Java = "failed", &version, &loader, &java
	value.LastError = model.Failure("build_failed", "recorded build failure")
	if err = m.db.Put(ctx, "build", value.ID, value); err != nil { t.Fatal(err) }
	failed, fresh, err := m.Request(ctx, commits[0], "fabric", false)
	if err != nil || fresh || failed.Status != "failed" || failed.Attempt != 1 { t.Fatalf("failed request: %+v %v %v", failed, fresh, err) }
	next, fresh, err := m.Request(ctx, commits[0], "fabric", true)
	if err != nil || !fresh || next.Attempt != 2 || next.Minecraft != nil || next.LoaderVersion != nil || next.Java != nil || next.LastError != nil || len(next.Files) != 0 || len(next.ModIDs) != 0 {
		t.Fatalf("force metadata reset: %+v %v %v", next, fresh, err)
	}
	_, _, err = m.Request(ctx, "--all", "fabric", false)
	requireCode(t, err, "invalid_request")
	_, _, err = m.Request(ctx, strings.Repeat("0", 40), "fabric", false)
	requireCode(t, err, "not_found")
	_, _, err = m.Request(ctx, commits[0], "neoforge", false)
	requireCode(t, err, "invalid_request")
}

func TestReadyReferencesAndLRUKeepPermanentAttempts(t *testing.T) {
	m, commits := realManager(t)
	ctx := context.Background()
	values := []model.Build{}
	for index, sha := range commits {
		value, _, err := m.Request(ctx, sha, "fabric", false)
		if err != nil { t.Fatal(err) }
		value.Status = "ready"
		if err = m.db.Put(ctx, "build", value.ID, value); err != nil { t.Fatal(err) }
		if err = m.db.Put(ctx, "build_access", value.ID, accessRecord{At: time.Unix(int64(index), 0)}); err != nil { t.Fatal(err) }
		values = append(values, value)
	}
	for _, kind := range []string{"branch", "commit", "asset"} {
		room := model.Room{ID: "held-room", Kind: kind, State: "stopped", BuildID: values[0].ID}
		if err := m.db.Put(ctx, "room", room.ID, room); err != nil { t.Fatal(err) }
		_, _, err := m.Request(ctx, commits[0], "fabric", true)
		requireCode(t, err, "conflict")
	}
	if err := m.prune(ctx); err != nil { t.Fatal(err) }
	if _, err := m.Get(ctx, values[0].ID); err != nil { t.Fatal("referenced build was evicted", err) }
	for _, value := range values[1:] {
		_, err := m.Get(ctx, value.ID)
		requireCode(t, err, "not_found")
		if _, err = os.Stat(m.attemptDir(value)); !errors.Is(err, os.ErrNotExist) { t.Fatalf("evicted files remain: %v", err) }
	}
	value, fresh, err := m.Request(ctx, commits[1], "fabric", false)
	if err != nil || !fresh || value.Attempt != 2 { t.Fatalf("evicted attempt counter: %+v %v %v", value, fresh, err) }
	if err = m.db.Put(ctx, "room", "held-room", model.Room{ID: "held-room", Kind: "asset", State: "closed", BuildID: values[0].ID}); err != nil { t.Fatal(err) }
	value, fresh, err = m.Request(ctx, commits[0], "fabric", true)
	if err != nil || !fresh || value.Attempt != 2 { t.Fatalf("closed room prevents rebuild: %+v %v %v", value, fresh, err) }
}

func TestWorkerRecordsRealCheckoutFailureAndReleasesLock(t *testing.T) {
	m, commits := realManager(t)
	ctx := context.Background()
	value, _, err := m.Request(ctx, commits[0], "fabric", false)
	if err != nil { t.Fatal(err) }
	if err = Worker(ctx, m.cfg, m.db, m.mirror, value.ID); err == nil { t.Fatal("commit without gradle.properties was accepted") }
	failed, err := m.Get(ctx, value.ID)
	if err != nil || failed.Status != "failed" || failed.LastError == nil || failed.LastError.Code != "build_failed" || failed.FinishedAt == nil || failed.StartedAt == nil { t.Fatalf("worker failure: %+v %v", failed, err) }
	entries, err := os.ReadDir(filepath.Join(m.cfg.Server.DataDir, "worktrees"))
	if err != nil || len(entries) != 0 { t.Fatalf("worktree cleanup: %v %v", entries, err) }
	first, err := os.Open(m.cfg.Build.LockFile)
	if err != nil { t.Fatal(err) }
	defer first.Close()
	if err = acquireLock(ctx, first); err != nil { t.Fatal(err) }
	second, err := os.Open(m.cfg.Build.LockFile)
	if err != nil { t.Fatal(err) }
	defer second.Close()
	deadline, cancel := context.WithTimeout(ctx, 30 * time.Millisecond)
	defer cancel()
	if err = acquireLock(deadline, second); !errors.Is(err, context.DeadlineExceeded) { t.Fatalf("concurrent lock acquisition: %v", err) }
	if err = unix.Flock(int(first.Fd()), unix.LOCK_UN); err != nil { t.Fatal(err) }
	if err = acquireLock(ctx, second); err != nil { t.Fatal(err) }
	if err = unix.Flock(int(second.Fd()), unix.LOCK_UN); err != nil { t.Fatal(err) }
}
