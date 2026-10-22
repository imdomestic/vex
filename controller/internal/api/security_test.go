package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/events"
	"github.com/imdomestic/vex/controller/internal/gitmirror"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/rooms"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/worlds"
)

type securityEnvironment struct {
	server   *httptest.Server
	auth     *auth.Auth
	projects map[string]*Server
	tokens   map[string]string
	sha      string
	older    string
}

func securityGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", append([]string{
		"-c", "user.name=Vex API Test", "-c", "user.email=api@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
	}, arguments...)...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func newSecurityEnvironment(t *testing.T) securityEnvironment {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	securityGit(t, root, "init", "--initial-branch=main", source)
	commits := []string{}
	for _, version := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(source, "README.md"), []byte(version+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		securityGit(t, source, "add", "README.md")
		securityGit(t, source, "commit", "-m", version)
		commits = append(commits, securityGit(t, source, "rev-parse", "HEAD"))
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "signing.key")
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	global := config.Config{
		Auth:     config.Auth{SigningKeyFile: keyPath},
		Users:    map[string]config.User{},
		Projects: map[string]config.Project{},
	}
	result := securityEnvironment{tokens: map[string]string{}, projects: map[string]*Server{}, sha: commits[1], older: commits[0]}
	for _, user := range []string{"reader", "writer", "outsider"} {
		if _, err := rand.Read(key); err != nil {
			t.Fatal(err)
		}
		result.tokens[user] = hex.EncodeToString(key)
		digest := sha256.Sum256([]byte(result.tokens[user]))
		global.Users[user] = config.User{TokenSHA256: hex.EncodeToString(digest[:])}
	}
	for _, project := range []string{"alpha", "beta", "no-eula", "untrusted"} {
		members := map[string]config.Role{}
		if project != "beta" {
			members["writer"] = config.Developer
		}
		if project == "alpha" {
			members["reader"] = config.Viewer
		}
		global.Projects[project] = config.Project{Members: members}
	}
	identity, err := auth.New(global)
	if err != nil {
		t.Fatal(err)
	}
	result.auth = identity
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	bus := events.New(db, "alpha", "beta", "no-eula", "untrusted")
	for _, project := range []string{"alpha", "beta", "no-eula", "untrusted"} {
		access, err := db.WithProject(project)
		if err != nil {
			t.Fatal(err)
		}
		cfg := config.ProjectConfig{
			ID: project, DataDir: filepath.Join(root, "data", project), Trust: "trusted",
			Server:    config.Server{Zone: "vex.invalid"},
			Git:       config.Git{Remote: source, DefaultBranch: "main"},
			Loaders:   map[string]config.Loader{"fabric": {Enabled: true}},
			Minecraft: config.ProjectMinecraft{Target: config.Target{Version: "26.3", Java: 25}, AcceptEULA: project != "no-eula"},
			Build:     config.EffectiveBuild{ProjectBuild: config.ProjectBuild{Source: "host"}},
			Rooms:     config.EffectiveRooms{DefaultSeed: "empty", JoinLease: "3m"},
			Members:   global.Projects[project].Members,
		}
		if project == "untrusted" {
			cfg.Trust = "untrusted"
		}
		mirror, err := gitmirror.New(source, filepath.Join(root, project+".git"))
		if err != nil {
			t.Fatal(err)
		}
		if err = mirror.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		builder, err := build.New(cfg, access, mirror)
		if err != nil {
			t.Fatal(err)
		}
		manager := rooms.New(cfg, access, mirror, builder, nil, nil, identity, bus)
		manager.Worlds, err = worlds.New(cfg, access)
		if err != nil {
			t.Fatal(err)
		}
		result.projects[project] = &Server{Cfg: cfg, DB: access, Git: mirror, Builds: builder, Rooms: manager}
	}
	result.server = httptest.NewServer(New(global, result.projects, identity, bus).Handler())
	t.Cleanup(result.server.Close)
	return result
}

func (e securityEnvironment) request(t *testing.T, method, path, token, body string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, e.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := e.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data
}

func (e securityEnvironment) failure(t *testing.T, method, path, token, body string, wantStatus int, wantCode string) {
	t.Helper()
	status, data := e.request(t, method, path, token, body)
	var response struct {
		Error model.Error `json:"error"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("%s %s 返回无效 JSON：%v", method, path, err)
	}
	if status != wantStatus || response.Error.Code != wantCode {
		t.Fatalf("%s %s 返回 %d %s，期望 %d %s", method, path, status, response.Error.Code, wantStatus, wantCode)
	}
}

func TestProjectAPIRolesAndVisibility(t *testing.T) {
	environment := newSecurityEnvironment(t)
	body := `{"sha":"` + environment.sha + `","loader":"fabric"}`
	environment.failure(t, "POST", "/v2/projects/alpha/builds", environment.tokens["reader"], body, 403, "forbidden")
	for _, path := range []string{"/v2/projects/beta", "/v2/projects/beta/builds", "/v2/projects/beta/rooms", "/v2/events?project=beta"} {
		environment.failure(t, "GET", path, environment.tokens["reader"], "", 404, "not_found")
	}
	environment.failure(t, "GET", "/v2/projects/alpha", environment.tokens["outsider"], "", 404, "not_found")
	environment.failure(t, "GET", "/v2/nodes", environment.tokens["reader"], "", 403, "forbidden")
	for _, user := range []string{"reader", "writer", "outsider"} {
		status, data := environment.request(t, "GET", "/v2/projects", environment.tokens[user], "")
		var projects []model.Project
		if err := json.Unmarshal(data, &projects); err != nil || status != 200 {
			t.Fatalf("项目列表返回状态 %d：%v", status, err)
		}
		ids := []string{}
		for _, project := range projects {
			ids = append(ids, project.ID)
		}
		want := map[string][]string{"reader": {"alpha"}, "writer": {"alpha", "no-eula", "untrusted"}, "outsider": {}}[user]
		if !slices.Equal(ids, want) {
			t.Fatalf("%s 可见项目为 %v，期望 %v", user, ids, want)
		}
	}
	builds, err := environment.projects["alpha"].Builds.List(t.Context())
	if err != nil || len(builds) != 0 {
		t.Fatalf("拒绝构建请求仍写入构建记录：%v", err)
	}
}

func TestRoomCredentialsCannotCrossProjectBuildOrSeed(t *testing.T) {
	environment := newSecurityEnvironment(t)
	project := environment.projects["alpha"]
	room, _, err := project.Rooms.Create(t.Context(), "commit", environment.older, "fabric", "empty", "")
	if err != nil {
		t.Fatal(err)
	}
	// 实际 start 只登记待执行构建和周期；本测试不运行构建器或房间调和循环。
	started, _, err := project.Rooms.Start(t.Context(), room.ID, nil, auth.Identity{User: "writer"})
	if err != nil {
		t.Fatal(err)
	}
	room = started.Room
	token, err := environment.auth.RoomToken("alpha", room.ID, room.Generation, room.BuildID, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	otherBuild, _, err := project.Builds.Request(t.Context(), environment.sha, "fabric", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/v2/projects/beta/builds/" + room.BuildID + "/files/mod.jar",
		"/v2/projects/alpha/builds/" + otherBuild.ID + "/files/mod.jar",
		"/v2/projects/alpha/worlds/spawn/revisions/1/download",
	} {
		environment.failure(t, "GET", path, token, "", 404, "not_found")
	}
	// 用户访问实际 queued 构建会到达文件状态检查，房间身份提前受到构建范围约束。
	environment.failure(t, "GET", "/v2/projects/alpha/builds/"+otherBuild.ID+"/files/mod.jar", environment.tokens["reader"], "", 409, "conflict")
	status, data := environment.request(t, "GET", "/v2/projects/alpha/rooms/"+room.ID, token, "")
	var own model.Room
	if err = json.Unmarshal(data, &own); err != nil || status != 200 || own.ID != room.ID || own.Generation != room.Generation {
		t.Fatalf("当前房间身份无法读取自身周期：HTTP %d，%v", status, err)
	}
	environment.failure(t, "GET", "/v2/projects", token, "", 404, "not_found")
	environment.failure(t, "GET", "/v2/projects/alpha/snapshots/unrelated/download", token, "", 403, "forbidden")
	stale, err := environment.auth.RoomToken("alpha", room.ID, room.Generation+1, room.BuildID, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	environment.failure(t, "GET", "/v2/projects/alpha/rooms/"+room.ID, stale, "", 401, "unauthorized")
}

func TestProjectPolicyRejectsUnacceptedEULAAndUntrustedHostBuild(t *testing.T) {
	environment := newSecurityEnvironment(t)
	project := environment.projects["no-eula"]
	room, _, err := project.Rooms.Create(t.Context(), "commit", environment.sha, "fabric", "empty", "")
	if err != nil {
		t.Fatal(err)
	}
	environment.failure(t, "POST", "/v2/projects/no-eula/rooms/"+room.ID+"/start", environment.tokens["writer"], "{}", 409, "eula_not_accepted")
	after, err := project.Rooms.Get(t.Context(), room.ID)
	if err != nil || after.Generation != 0 || after.State != "stopped" {
		t.Fatalf("未接受 EULA 仍修改启动周期：%v", err)
	}
	body := `{"sha":"` + environment.sha + `","loader":"fabric"}`
	environment.failure(t, "POST", "/v2/projects/untrusted/builds", environment.tokens["writer"], body, 409, "sandbox_unavailable")
	for _, name := range []string{"no-eula", "untrusted"} {
		builds, err := environment.projects[name].Builds.List(t.Context())
		if err != nil || len(builds) != 0 {
			t.Fatalf("%s 拒绝策略仍写入构建记录：%v", name, err)
		}
	}
}
