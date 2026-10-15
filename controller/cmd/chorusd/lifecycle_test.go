package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/api"
	"github.com/imdomestic/chorus-testbed/controller/internal/auth"
	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/events"
	"github.com/imdomestic/chorus-testbed/controller/internal/gitmirror"
	"github.com/imdomestic/chorus-testbed/controller/internal/rooms"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
	"github.com/pelletier/go-toml/v2"
)

func TestServiceCancellationClosesActualAPIEventStream(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(key)
	users, err := toml.Marshal(struct {
		Users map[string]string `toml:"users"`
	}{Users: map[string]string{"integration": token}})
	if err != nil {
		t.Fatal(err)
	}
	usersFile, keyFile := filepath.Join(root, "users.toml"), filepath.Join(root, "signing.key")
	if err = os.WriteFile(usersFile, users, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.Load(usersFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New(db)
	service := api.New(config.Config{}, db, nil, nil, nil, authenticator, bus)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	server := newHTTPServer(ctx, service.Handler())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	request, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("actual authenticated API did not establish an SSE stream")
	}
	reader := bufio.NewReader(response.Body)
	foundSync := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal("initial SSE sync:", err)
		}
		if line == "event: sync\n" {
			foundSync = true
		}
		if line == "\n" {
			break
		}
	}
	if !foundSync {
		t.Fatal("initial sync event was absent")
	}
	cancel()
	shutdown, done := context.WithTimeout(context.Background(), time.Second)
	defer done()
	if err = server.Shutdown(shutdown); err != nil {
		t.Fatal("open SSE connection prevented service shutdown:", err)
	}
	if _, err = reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatal("SSE connection did not finish cleanly on service cancellation:", err)
	}
	if err = <-served; !errors.Is(err, http.ErrServerClosed) {
		t.Fatal("HTTP service exit:", err)
	}
}

func TestPeriodicGitRefreshPublishesRealRemoteChanges(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-c", "user.name=Chorus Integration", "-c", "user.email=chorus@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--initial-branch=main", source)
	if err := os.WriteFile(filepath.Join(source, "content.txt"), []byte("初始内容"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", source, "add", "content.txt")
	git("-C", source, "commit", "-m", "初始内容")
	mirror, err := gitmirror.New(source, filepath.Join(root, "mirror.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = mirror.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bus := events.New(db)
	manager := rooms.New(config.Config{Loaders: map[string]config.Loader{"fabric": {Enabled: true}}}, db, mirror, nil, nil, nil, bus)
	_, changes, unsubscribe, err := bus.Subscribe(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- refreshGit(ctx, manager, 250*time.Millisecond) }()
	for _, expected := range []string{"branch_added", "branch_moved"} {
		select {
		case change := <-changes:
			if change.Name != expected {
				t.Fatalf("periodic branch event: %s, expected %s", change.Name, expected)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("periodic Git refresh did not publish", expected)
		}
		if expected == "branch_added" {
			if err = os.WriteFile(filepath.Join(source, "content.txt"), []byte("定时刷新新增内容"), 0600); err != nil {
				t.Fatal(err)
			}
			git("-C", source, "commit", "-am", "定时刷新新增内容")
		}
	}
	refs, err := mirror.Branches(t.Context())
	if err != nil || len(refs) != 1 || refs[0].Head != git("-C", source, "rev-parse", "HEAD") {
		t.Fatal("periodic mirror is not at the actual remote HEAD:", err)
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("Git loop cancellation:", err)
		}
	case <-time.After(time.Second):
		t.Fatal("periodic Git loop ignored service cancellation")
	}
}
