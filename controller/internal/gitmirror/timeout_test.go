package gitmirror

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestRefreshDeadlineIncludesConcurrentRealFetch(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	git(t, root, "init", "--initial-branch=main", source)
	sha := commit(t, source, "data.txt", "真实 Git daemon 超时验收")
	remote := filepath.Join(root, "source.git")
	git(t, root, "clone", "--bare", source, remote)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(root, "git-daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	daemon := exec.Command("git", "daemon", "--reuseaddr", "--listen=127.0.0.1", "--port="+strconv.Itoa(port), "--base-path="+root, "--export-all", "--strict-paths", remote)
	daemon.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	daemon.Stdout, daemon.Stderr = logFile, logFile
	if err = daemon.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		syscall.Kill(-daemon.Process.Pid, syscall.SIGCONT)
		syscall.Kill(-daemon.Process.Pid, syscall.SIGKILL)
		daemon.Wait()
	}()
	mirror, err := New("git://127.0.0.1:"+strconv.Itoa(port)+"/source.git", filepath.Join(root, "mirror.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = mirror.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		attempt, cancel := context.WithTimeout(t.Context(), time.Second)
		err = mirror.Refresh(attempt)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("actual Git daemon did not become available:", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = syscall.Kill(-daemon.Process.Pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	background, cancelBackground := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancelBackground()
	finished := make(chan error, 1)
	go func() { finished <- mirror.Refresh(background) }()
	deadline = time.Now().Add(time.Second)
	for len(mirror.refreshMu.token) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("background fetch did not acquire its lock")
		}
		time.Sleep(time.Millisecond)
	}
	request, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = mirror.Refresh(request)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("concurrent refresh exceeded request deadline: %v (%s)", err, time.Since(started))
	}
	select {
	case err := <-finished:
		t.Fatal("background fetch unexpectedly completed while its actual Git daemon was suspended:", err)
	default:
	}
	refs, err := mirror.Branches(t.Context())
	if err != nil || len(refs) != 1 || refs[0].Head != sha {
		t.Fatalf("cached refs unavailable during a blocked fetch: %+v %v", refs, err)
	}
	cancelBackground()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("fetch cancellation:", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Git fetch process group did not terminate after cancellation")
	}
	if err = syscall.Kill(-daemon.Process.Pid, syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	if err = mirror.Refresh(t.Context()); err != nil {
		t.Fatal("Git refresh did not recover after the daemon resumed:", err)
	}
}

func TestMirrorReadLockRespectsDeadline(t *testing.T) {
	mirror, err := New(filepath.Join(t.TempDir(), "source"), filepath.Join(t.TempDir(), "mirror.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = mirror.mu.Lock(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer mirror.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err = mirror.Branches(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("local ref lock ignored deadline:", err)
	}
}
