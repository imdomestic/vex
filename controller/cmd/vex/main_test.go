package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/pflag"
)

func cleanClientEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "VEX_") {
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.Setenv(key, value); err != nil {
					t.Error(err)
				}
			})
		}
	}
}

func clientConfigFile(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "client.toml")
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func configuredFlags(t *testing.T, path string) *pflag.FlagSet {
	t.Helper()
	flags := globals()
	if err := flags.Parse([]string{"--config", path}); err != nil {
		t.Fatal(err)
	}
	return flags
}

func TestConfigCommandsUseSharedFourLayerResolution(t *testing.T) {
	cleanClientEnvironment(t)
	file := clientConfigFile(t, "url = 'http://127.0.0.1:8791'\nproject = 'file-project'\ntimeout = '1m'\n")
	t.Setenv("VEX_PROJECT", "environment-project")
	t.Setenv("VEX_TIMEOUT", "2m")
	args := []string{"config", "show", "--sources", "--config", file, "-p", "command-project"}
	var output bytes.Buffer
	if err := run(context.Background(), args, &output); err != nil {
		t.Fatal(err)
	}
	var value config.Client
	if err := toml.Unmarshal(output.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value.Project != "command-project" || value.Timeout != "2m" || value.URL != "http://127.0.0.1:8791" {
		t.Fatal("CLI configuration precedence differs from shared loader")
	}
	for _, source := range []string{"source project = command", "source timeout = environment", "source url = file", "source token_file = default"} {
		if !strings.Contains(output.String(), source) {
			t.Fatalf("missing provenance %s", source)
		}
	}
	output.Reset()
	if err := run(context.Background(), []string{"--config", file, "config", "check"}, &output); err != nil || output.String() != "configuration valid\n" {
		t.Fatalf("configuration check failed: %v", err)
	}
	if err := run(context.Background(), []string{"--config", file, "--token", "not-a-supported-option", "config", "check"}, &output); err == nil {
		t.Fatal("direct token argument accepted")
	}
	output.Reset()
	if err := run(context.Background(), []string{"--help"}, &output); err != nil || !strings.Contains(output.String(), "vex") {
		t.Fatal("help did not return successfully")
	}
}

func TestResetWorldRequiresTypedRoomIDBeforeAnyRequest(t *testing.T) {
	cleanClientEnvironment(t)
	// 服务器地址不可连接：确认不一致时命令必须在发送请求之前结束。
	file := clientConfigFile(t, "url = 'http://127.0.0.1:1'\nproject = 'example-mod'\n")
	var output bytes.Buffer
	err := runWithInput(context.Background(), []string{"--config", file, "room", "reset-world", "main-fabric"}, strings.NewReader("main-fabrc\n"), &output)
	if err == nil || !strings.Contains(err.Error(), "没有执行重置") || !strings.Contains(output.String(), "请输入房间 ID") {
		t.Fatalf("mismatched confirmation: %v %q", err, output.String())
	}
	output.Reset()
	err = runWithInput(context.Background(), []string{"--config", file, "room", "reset-world", "main-fabric"}, strings.NewReader(""), &output)
	if err == nil || !strings.Contains(err.Error(), "没有执行重置") {
		t.Fatalf("empty confirmation: %v", err)
	}
}

func TestProjectCommandPlans(t *testing.T) {
	cleanClientEnvironment(t)
	file := clientConfigFile(t, "project = 'example-mod'\n")
	cases := []struct {
		args   []string
		method string
		path   string
	}{
		{[]string{"git", "refresh"}, http.MethodPost, "/v2/projects/example-mod/git/refresh"},
		{[]string{"git", "commits", "--branch", "feature/blocks", "--limit", "5"}, http.MethodGet, "/v2/projects/example-mod/commits?branch=feature%2Fblocks&limit=5"},
		{[]string{"builds", "create", "--commit", strings.Repeat("a", 40), "--loader", "fabric", "-p", "another-mod"}, http.MethodPost, "/v2/projects/another-mod/builds"},
		{[]string{"rooms", "start", "main-fabric", "--seed", "empty"}, http.MethodPost, "/v2/projects/example-mod/rooms/main-fabric/start"},
		{[]string{"rooms", "label", "main-fabric", "--label", "Build verification"}, http.MethodPatch, "/v2/projects/example-mod/rooms/main-fabric"},
		{[]string{"rooms", "delete", "main-fabric"}, http.MethodDelete, "/v2/projects/example-mod/rooms/main-fabric"},
		{[]string{"snapshots", "restore", "main-fabric-g1"}, http.MethodPost, "/v2/projects/example-mod/snapshots/main-fabric-g1/restore"},
		{[]string{"world", "list"}, http.MethodGet, "/v2/projects/example-mod/worlds"},
		{[]string{"world", "show", "spawn"}, http.MethodGet, "/v2/projects/example-mod/worlds/spawn"},
		{[]string{"world", "archive", "spawn"}, http.MethodPatch, "/v2/projects/example-mod/worlds/spawn"},
		{[]string{"world", "restore", "spawn"}, http.MethodPost, "/v2/projects/example-mod/worlds/spawn/restore"},
	}
	for _, item := range cases {
		t.Run(strings.Join(item.args[:2], "-"), func(t *testing.T) {
			plan, _, err := planCommand(context.Background(), configuredFlags(t, file), item.args)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Method != item.method || plan.Path != item.path {
				t.Fatalf("command selected %s %s", plan.Method, plan.Path)
			}
		})
	}
	plan, _, err := planCommand(context.Background(), configuredFlags(t, file), []string{"rooms", "create", "--commit", strings.Repeat("b", 40), "--loader", "fabric"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Body.(map[string]any)["seed"] != "" {
		t.Fatal("CLI overrode project default seed without a seed argument")
	}
	plan, _, err = planCommand(context.Background(), configuredFlags(t, file), []string{"rooms", "reset-world", "main-fabric", "--seed", "empty"})
	if err != nil || plan.Method != http.MethodPost || plan.Path != "/v2/projects/example-mod/rooms/main-fabric/reset-world" || plan.Confirm != "main-fabric" || plan.Body.(map[string]string)["seed"] != "empty" || plan.Body.(map[string]string)["confirm"] != "" {
		t.Fatalf("reset-world plan: %+v %v", plan, err)
	}
	for _, args := range [][]string{{"builds", "create", "--commit", strings.Repeat("a", 40)}, {"rooms", "start"}, {"git", "commits", "--limit", "0"}, {"world", "show", "bad--id"}, {"snapshots", "reset-world", "main-fabric-g1"}} {
		if _, _, err := planCommand(context.Background(), configuredFlags(t, file), args); err == nil {
			t.Fatalf("invalid command accepted: %s", args[0])
		}
	}
}
