//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/testproject"
	"github.com/pelletier/go-toml/v2"
)

func TestRealDaemonStartsWithUnavailableRemote(t *testing.T) {
	binary, configPath := os.Getenv("VEX_IT_BINARY"), os.Getenv("VEX_IT_CONFIG")
	runID, work := os.Getenv("VEX_IT_RUN_ID"), os.Getenv("VEX_IT_WORK")
	if binary == "" || configPath == "" || runID == "" || work == "" {
		t.Skip("requires actual vexd, operator configuration and an isolated run ID")
	}
	if !filepath.IsAbs(work) || !strings.Contains(filepath.Clean(work), "/.work/") {
		t.Fatal("offline integration requires an absolute ignored workspace")
	}
	resolved, err := config.LoadDaemon(config.Options{Args: []string{"--config", configPath}, Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := resolved.Config
	// 项目定义由测试建立，不取自运维配置中的任何实际项目。
	project := testproject.Configure(t, &cfg, "http://127.0.0.1:1/unreachable.git", nil)
	project.Build, project.GitHub, project.CI = config.ProjectBuild{Source: "host", Timeout: "30m"}, config.ProjectGitHub{}, config.ProjectCI{}
	projectID := "vex-it-offline-" + runID
	if !config.ValidID(projectID) {
		t.Fatal("integration project ID is invalid")
	}
	if err = os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(work, "offline-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("preserved actual process diagnostics: %s", filepath.Join(root, "daemon.log"))
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
	cfg.Server.DataDir = filepath.Join(root, "data")
	project.Remote, project.Trust = "http://127.0.0.1:1/unreachable.git", "trusted"
	project.Git = config.ProjectGit{PollInterval: "1s"}
	project.Members = map[string]config.Role{"integration": config.Owner}
	cfg.Projects = map[string]config.Project{projectID: project}
	cfg.Build.LockFile = filepath.Join(root, "build.lock")
	if kubeconfig := os.Getenv("VEX_IT_KUBECONFIG"); kubeconfig != "" {
		cfg.Runtime.Kubernetes.KubeconfigFile = kubeconfig
	}
	cfg.Archive = config.Archive{Backend: "local", Local: config.LocalArchive{Directory: filepath.Join(root, "archive")}}
	cfg.Auth.SigningKeyFile = filepath.Join(root, "signing.key")
	// 测试进程使用自己的管理 socket，不触及生产服务的 /run/vex。
	cfg.Auth.AdminSocket = filepath.Join(root, "admin.sock")
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret)
	digest := sha256.Sum256([]byte(token))
	cfg.Users = map[string]config.User{"integration": {TokenSHA256: hex.EncodeToString(digest[:])}}
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
	owner, err := store.Open(filepath.Join(cfg.Server.DataDir, "state", "vexd.db"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := owner.WithProject(projectID)
	if err != nil {
		owner.Close()
		t.Fatal(err)
	}
	branch := "main"
	saved := model.Room{Project: projectID, ID: "main-fabric", Kind: "branch", Branch: &branch, State: "stopped", Label: "testing", Host: "main-fabric--" + projectID + "." + cfg.Server.Zone}
	if err = db.Put(context.Background(), "room", saved.ID, saved); err != nil {
		t.Fatal(err)
	}
	if err = owner.Close(); err != nil {
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
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "VEX_") {
			command.Env = append(command.Env, variable)
		}
	}
	command.Stdout, command.Stderr = logfile, logfile
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- command.Wait() }()
	processFinished := false
	defer func() {
		if processFinished {
			return
		}
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
		status, _, err := request("GET", "/v2/health", nil)
		if err == nil && status == 200 {
			break
		}
		select {
		case err := <-finished:
			processFinished = true
			t.Fatalf("actual daemon exited before health: %v; inspect %s", err, logfile.Name())
		case <-ctx.Done():
			t.Fatal("API did not start with unavailable remote")
		case <-ticker.C:
		}
	}
	prefix := "/v2/projects/" + projectID
	status, data, err := request("GET", prefix+"/branches", nil)
	if err != nil || status != 200 || strings.TrimSpace(string(data)) != "[]" {
		t.Fatalf("empty mirror branches: status=%d error=%v", status, err)
	}
	payload, err := json.Marshal(map[string]any{"sha": strings.Repeat("0", 40), "loader": "fabric"})
	if err != nil {
		t.Fatal(err)
	}
	status, data, err = request("POST", prefix+"/builds", payload)
	if err != nil || status != 409 {
		t.Fatalf("missing commit build: status=%d error=%v", status, err)
	}
	var response struct {
		Error model.Error `json:"error"`
	}
	if err = json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != "commit_unavailable" || !strings.Contains(response.Error.Message, "现有历史") {
		t.Fatal("build failure does not explain the unavailable commit")
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
	status, _, err = request("GET", "/v2/health", nil)
	if err != nil || status != 200 {
		t.Fatalf("API unavailable after repeated fetch failure: %d %v", status, err)
	}
	status, data, err = request("GET", prefix+"/rooms/main-fabric", nil)
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
	t.Logf("actual daemon, k3s client and unavailable remote preserved the project API; dedicated namespace requires cleanup: vex-p-%s", projectID)
}
