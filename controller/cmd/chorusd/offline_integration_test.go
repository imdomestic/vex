//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
	"github.com/pelletier/go-toml/v2"
)

func TestRealDaemonStartsWithUnavailableRemote(t *testing.T) {
	binary, configPath := os.Getenv("CHORUS_IT_BINARY"), os.Getenv("CHORUS_IT_CONFIG")
	if binary == "" || configPath == "" {
		t.Skip("requires real chorusd binary and encore configuration")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.Server.Listen = []string{address}
	cfg.Server.PublicBaseURL = "http://" + address
	cfg.Server.InternalBaseURL = cfg.Server.PublicBaseURL
	cfg.Server.DataDir = filepath.Join(root, "data")
	cfg.Git.Remote = "http://127.0.0.1:1/unreachable.git"
	cfg.Git.PollInterval = "1s"
	cfg.Rooms.Namespace = fmt.Sprintf("chorus-it-offline-%d", time.Now().UnixNano())
	if kubeconfig := os.Getenv("CHORUS_IT_KUBECONFIG"); kubeconfig != "" {
		cfg.Rooms.Kubeconfig = kubeconfig
	}
	cfg.Archive.Enabled = false
	cfg.Auth.UsersFile = filepath.Join(root, "users.toml")
	cfg.Auth.SigningKeyFile = filepath.Join(root, "signing.key")
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret)
	users, err := toml.Marshal(map[string]any{"users": map[string]string{"integration": token}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.Auth.UsersFile, users, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.Auth.SigningKeyFile, secret, 0600); err != nil {
		t.Fatal(err)
	}
	encoded, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(root, "config.toml")
	if err = os.WriteFile(filename, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(cfg.Server.DataDir, "state", "chorusd.db"))
	if err != nil {
		t.Fatal(err)
	}
	branch := "main"
	saved := model.Room{ID: "main-fabric", Kind: "branch", Branch: &branch, State: "stopped", Label: "testing", Host: "main-fabric." + cfg.Minecraft.Zone}
	if err = db.Put(context.Background(), "room", saved.ID, saved); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	logfile, err := os.Create(filepath.Join(root, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logfile.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "serve", "--config", filename)
	command.Stdout, command.Stderr = logfile, logfile
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- command.Wait() }()
	defer func() {
		if err := command.Process.Signal(syscall.SIGTERM); err != nil {
			t.Error(err)
		}
		select {
		case err := <-finished:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("daemon did not shut down")
			cancel()
		}
	}()
	client := &http.Client{Timeout: 2 * time.Second}
	request := func(method, path string, body []byte) (int, []byte, error) {
		r, err := http.NewRequestWithContext(ctx, method, cfg.Server.PublicBaseURL+path, bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		response, err := client.Do(r)
		if err != nil {
			return 0, nil, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		return response.StatusCode, data, err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, _, err := request("GET", "/v1/health", nil)
		if err == nil && status == 200 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("API did not start with unavailable remote")
		case <-ticker.C:
		}
	}
	status, data, err := request("GET", "/v1/branches", nil)
	if err != nil || status != 200 || strings.TrimSpace(string(data)) != "[]" {
		t.Fatalf("empty mirror branches: status=%d error=%v", status, err)
	}
	payload, err := json.Marshal(map[string]any{"sha": strings.Repeat("0", 40), "loader": "fabric"})
	if err != nil {
		t.Fatal(err)
	}
	status, data, err = request("POST", "/v1/builds", payload)
	if err != nil || status != 404 {
		t.Fatalf("missing commit build: status=%d error=%v", status, err)
	}
	var response struct {
		Error model.Error `json:"error"`
	}
	if err = json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != "not_found" || !strings.Contains(response.Error.Message, "remote refresh") {
		t.Fatal("build failure does not explain unavailable source")
	}
	for {
		logs, err := os.ReadFile(logfile.Name())
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Count(logs, []byte("retaining local mirror")) >= 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("git retry was not observed")
		case <-ticker.C:
		}
	}
	status, _, err = request("GET", "/v1/health", nil)
	if err != nil || status != 200 {
		t.Fatalf("API unavailable after repeated fetch failure: %d %v", status, err)
	}
	status, data, err = request("GET", "/v1/rooms/main-fabric", nil)
	if err != nil || status != 200 {
		t.Fatalf("saved room unavailable: %d %v", status, err)
	}
	var room model.Room
	if err = json.Unmarshal(data, &room); err != nil {
		t.Fatal(err)
	}
	if room.State != "stopped" || room.Label != "testing" {
		t.Fatalf("empty mirror changed registered branch: %s/%s", room.State, room.Label)
	}
	t.Log("actual daemon, actual k3s client, empty local Git mirror and unreachable remote: API remained available through repeated refresh failure")
}
