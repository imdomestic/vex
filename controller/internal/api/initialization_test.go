package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/projectstate"
	"github.com/imdomestic/vex/controller/internal/rooms"
)

func TestProjectInitializationWaitsForMirrorAndListsRemovedCommits(t *testing.T) {
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
	// 镜像还没有取得该提交，远端也无法访问：不能确定提交是否已被删除，项目保持补录状态。
	if err = os.Rename(source, source+"-offline"); err != nil {
		t.Fatal(err)
	}
	if err = project.State.Initialize(t.Context()); err == nil {
		t.Fatal("initialization completed without reaching the remote that may still hold the commit")
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
		t.Fatal("background work was released before record backfill")
	}
	if err = os.Rename(source+"-offline", source); err != nil {
		t.Fatal(err)
	}
	// 远端重新可用时补录读取实际提交，并开放写操作。
	if err = project.State.Initialize(t.Context()); err != nil {
		t.Fatal("project did not recover after fetching the actual commit", err)
	}
	if err = project.State.Check(); err != nil {
		t.Fatal(err)
	}
	var restored model.Build
	if err = project.DB.Get(t.Context(), "build", legacy.ID, &restored); err != nil || restored.ClientFingerprint == "" || restored.Commit == nil || restored.Commit.Subject != "publish project manifest" || restored.Manifest == nil {
		t.Fatal("backfill did not complete the historical build", err)
	}
	status, _ = environment.request(t, "POST", "/v2/projects/alpha/rooms", writer, request)
	if status != 201 {
		t.Fatalf("recovered project refused write: %d", status)
	}
	// 远端已经删除的提交：刷新成功后仍无法读取，记录列入补录结果，项目照常开放。
	securityGit(t, source, "checkout", "-b", "feat/removed")
	if err = os.WriteFile(filepath.Join(source, "removed.txt"), []byte("removed"), 0600); err != nil {
		t.Fatal(err)
	}
	securityGit(t, source, "add", "removed.txt")
	securityGit(t, source, "commit", "-m", "commit removed before the mirror fetched it")
	removed := securityGit(t, source, "rev-parse", "HEAD")
	securityGit(t, source, "checkout", "main")
	securityGit(t, source, "branch", "-D", "feat/removed")
	orphan := legacy
	orphan.ID, orphan.SHA, orphan.Commit, orphan.Manifest, orphan.ClientFingerprint = rooms.BuildID(removed, "fabric"), removed, nil, nil, ""
	if err = project.DB.Put(t.Context(), "build", orphan.ID, orphan); err != nil {
		t.Fatal(err)
	}
	project.State = projectstate.New(project.Builds, project.Rooms, project.Git)
	if err = project.State.Initialize(t.Context()); err != nil {
		t.Fatal("a commit removed from the remote blocked the project", err)
	}
	var result model.RecordBackfill
	if err = project.DB.Get(t.Context(), "record_backfill", "status", &result); err != nil || len(result.Issues) != 1 || result.Issues[0].ID != orphan.ID || result.Issues[0].Reason != "commit_unavailable" {
		t.Fatal("removed commit was not listed in the backfill result", result, err)
	}
	environment.failure(t, "POST", "/v2/projects/alpha/rooms", writer, `{"kind":"commit","sha":"`+removed+`","loader":"fabric","seed":"empty"}`, 409, "commit_unavailable")
	completed, cancelCompleted := context.WithCancel(t.Context())
	cancelCompleted()
	if err = project.State.Run(completed); err != context.Canceled {
		t.Fatal("initialized supervision did not stop on cancellation", err)
	}
}
