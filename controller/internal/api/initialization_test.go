package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/projectstate"
	"github.com/imdomestic/vex/controller/internal/rooms"
)

func TestProjectInitializationIsolatesUnavailableHistoricalCommit(t *testing.T) {
	environment := newSecurityEnvironment(t)
	project := environment.projects["alpha"]
	project.Cfg.Runtimes = map[string]config.JavaRuntime{"java25": {}}
	builder, err := build.New(project.Cfg, project.DB, project.Git)
	if err != nil {
		t.Fatal(err)
	}
	project.Builds, project.Rooms.Builds = builder, builder
	project.State = projectstate.New(project.Builds, project.Rooms, project.Git)
	source := project.Cfg.Git.Remote
	const manifest = `schema = 1
[minecraft]
version = "26.3"
java = 25
[loaders.fabric]
loader_version = "0.19.5"
build = ["./gradlew", "build", "--no-daemon"]
agent = true
[[loaders.fabric.files]]
glob = "build/libs/*.jar"
exclude = ["*-sources.jar", "*-dev*.jar", "*-javadoc.jar"]
side = "both"
[rooms]
memory = "4G"
`
	if err = os.WriteFile(filepath.Join(source, "vex.toml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	securityGit(t, source, "add", "vex.toml")
	securityGit(t, source, "commit", "-m", "publish project manifest")
	sha := securityGit(t, source, "rev-parse", "HEAD")
	minecraft, loader, java := "26.3", "0.19.5", 25
	legacy := model.Build{Project: "alpha", ID: rooms.BuildID(sha, "fabric"), SHA: sha, Loader: "fabric", Status: "ready", Minecraft: &minecraft, LoaderVersion: &loader, Java: &java}
	if err = project.DB.Put(t.Context(), "build", legacy.ID, legacy); err != nil {
		t.Fatal(err)
	}
	if err = project.State.Initialize(t.Context()); err == nil {
		t.Fatal("unavailable historical commit accepted")
	}
	reader, writer := environment.tokens["reader"], environment.tokens["writer"]
	for _, request := range []struct{ path, token string }{
		{"/v2/health", ""},
		{"/v2/projects/alpha/rooms", reader},
		{"/v2/projects/alpha/builds", reader},
	} {
		status, _ := environment.request(t, "GET", request.path, request.token, "")
		if status != 200 {
			t.Fatalf("initializing project prevented read %s: %d", request.path, status)
		}
	}
	request := `{"kind":"commit","sha":"` + environment.sha + `","loader":"fabric","seed":"empty"}`
	environment.failure(t, "POST", "/v2/projects/alpha/rooms", writer, request, 503, "project_initializing")
	environment.failure(t, "GET", "/v2/projects/alpha/builds/"+legacy.ID+"/pack.mrpack", reader, "", 503, "project_initializing")
	environment.failure(t, "POST", "/v2/projects/alpha/rooms", reader, request, 403, "forbidden")
	status, _ := environment.request(t, "POST", "/v2/projects/no-eula/rooms", writer, request)
	if status != 201 {
		t.Fatalf("initializing project blocked independent project: %d", status)
	}
	waiting, cancelWaiting := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancelWaiting()
	if err = project.State.Wait(waiting); err == nil {
		t.Fatal("background work was released before metadata initialization")
	}
	// 第一次监督执行会读取真实 remote，失败原因仍由监督器记录。
	if err = project.State.Run(t.Context()); err == nil || !strings.Contains(err.Error(), legacy.ID) {
		t.Fatal("initialization did not identify the unavailable build", err)
	}
	if err = project.State.Initialize(t.Context()); err != nil {
		t.Fatal("project did not recover after fetching the actual commit", err)
	}
	if err = project.State.Check(); err != nil {
		t.Fatal(err)
	}
	status, _ = environment.request(t, "POST", "/v2/projects/alpha/rooms", writer, request)
	if status != 201 {
		t.Fatalf("recovered project refused write: %d", status)
	}
	if err = project.State.After(project.Builds.InitializeRecords)(t.Context()); err != nil {
		t.Fatal("ready project prevented background work", err)
	}
	completed, cancelCompleted := context.WithCancel(t.Context())
	cancelCompleted()
	if err = project.State.Run(completed); err != context.Canceled {
		t.Fatal("initialized supervision did not stop on cancellation", err)
	}
}
