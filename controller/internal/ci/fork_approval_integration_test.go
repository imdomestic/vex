//go:build integration

package ci

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/githubapp"
	"github.com/imdomestic/vex/controller/internal/gitmirror"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/rooms"
	"github.com/imdomestic/vex/controller/internal/store"
)

func TestRealPersonalForkRequiresMaintainerApproval(t *testing.T) {
	if os.Getenv("VEX_IT_GITHUB_MUTATIONS") != "1" {
		t.Skip("requires the authorized personal-fork acceptance PR")
	}
	work := os.Getenv("VEX_IT_WORK")
	if !filepath.IsAbs(work) || !strings.Contains(filepath.Clean(work), "/.work/") {
		t.Fatal("an absolute ignored evidence directory is required")
	}
	number, err := strconv.Atoi(os.Getenv("VEX_IT_GITHUB_PR"))
	if err != nil || number <= 0 {
		t.Fatal("the actual personal-fork PR number is required")
	}
	runID, err := strconv.ParseInt(os.Getenv("VEX_IT_GITHUB_RUN"), 10, 64)
	if err != nil || runID <= 0 {
		t.Fatal("the actual successful fork workflow run is required")
	}
	resolved, err := config.LoadDaemon(config.Options{Args: []string{"--config", os.Getenv("VEX_IT_CONFIG")}, Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	app, err := githubapp.New(resolved.Config.GitHub.App)
	if err != nil {
		t.Fatal(err)
	}
	owner, repository := githubRepository(t, "VEX_IT_GITHUB_REPOSITORY")
	forkOwner, forkRepository := githubRepository(t, "VEX_IT_GITHUB_FORK")
	fork := forkOwner + "/" + forkRepository
	const workflow = ".github/workflows/vex-fork-it.yml"
	client, err := app.Repository(t.Context(), owner+"/"+repository)
	if err != nil {
		t.Fatal(err)
	}
	actual, _, err := client.PullRequests.Get(t.Context(), owner, repository, number)
	if err != nil {
		t.Fatal(githubapp.SafeError("read actual fork PR", err))
	}
	if actual.GetState() != "open" || actual.GetHead().GetRepo().GetFullName() != fork || !actual.GetHead().GetRepo().GetFork() || actual.GetHead().GetRef() != "test/vex-fork-approval" {
		t.Fatal("the PR differs from the authorized personal-fork branch")
	}
	run, _, err := client.Actions.GetWorkflowRunByID(t.Context(), owner, repository, runID)
	if err != nil {
		t.Fatal(githubapp.SafeError("read actual fork workflow", err))
	}
	if run.GetPath() != workflow || run.GetHeadSHA() != actual.GetHead().GetSHA() || run.GetHeadRepository().GetFullName() != fork || run.GetEvent() != "pull_request" || run.GetConclusion() != "success" || run.GetStatus() != "completed" {
		t.Fatal("the workflow identity differs from the actual fork PR")
	}
	if err = os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(work, "fork-approval-")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := resolved.Config.ForProject("example")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ID, cfg.DataDir = "fork-approval", filepath.Join(directory, "project")
	cfg.Build.Source = "ci"
	cfg.CI.Workflow, cfg.CI.ForkPreviews, cfg.CI.PRPreviews = workflow, "approval", true
	// 验收只调用数据库登记流程，未启动房间调和器及 Kubernetes 后端。
	cfg.Wake.Enabled = false
	db, err := store.Open(filepath.Join(directory, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	scoped, err := db.WithProject(cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	mirror, err := gitmirror.New("https://github.com/"+owner+"/"+repository+".git", filepath.Join(directory, "git.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = mirror.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	builder, err := build.New(cfg, scoped, mirror)
	if err != nil {
		t.Fatal(err)
	}
	manager := rooms.New(cfg, scoped, mirror, builder, nil, nil, nil, nil)
	controller := New(cfg, scoped, app, builder, mirror, manager, nil, nil)
	pull := controller.pullValue(actual)
	approved, err := controller.approve(t.Context(), client, owner, repository, pull, run.GetCreatedAt().Time)
	if err != nil || approved {
		t.Fatal("the actual fork already has maintainer approval", err)
	}
	accepted, err := controller.acceptRun(t.Context(), client, owner, repository, run, map[int]model.Pull{number: pull})
	if accepted || !errors.Is(err, errApprovalPending) {
		t.Fatal("the actual unapproved artifact passed the production acceptance guard", err)
	}
	if err = manager.SyncPull(t.Context(), pull, nil); err != nil {
		t.Fatal(err)
	}
	if err = scoped.Get(t.Context(), "pull", strconv.Itoa(number), &pull); err != nil || !pull.Fork || pull.Approved || len(pull.Builds) != 0 || len(pull.Rooms) != 0 {
		t.Fatal("unapproved fork was accepted", err)
	}
	values, err := builder.List(t.Context())
	if err != nil || len(values) != 0 {
		t.Fatal("unapproved fork created build records", err)
	}
	var published struct {
		CommentID int64 `json:"commentId"`
		CheckID   int64 `json:"checkId"`
	}
	if err = controller.publish(t.Context(), client, owner, repository, pull, nil); err != nil {
		t.Fatal(err)
	}
	if err = scoped.Get(t.Context(), "ci_publication", strconv.Itoa(number), &published); err != nil {
		t.Fatal(err)
	}
	comment, _, err := client.Issues.GetComment(t.Context(), owner, repository, published.CommentID)
	if err != nil || !strings.Contains(comment.GetBody(), "等待批准") {
		t.Fatal("actual fork comment did not require approval")
	}
	check, _, err := client.Checks.GetCheckRun(t.Context(), owner, repository, published.CheckID)
	if err != nil || check.GetStatus() != "queued" || check.GetHeadSHA() != pull.HeadSHA {
		t.Fatal("actual unapproved fork check did not remain queued at the authoritative head")
	}
	signal := func(name string, value any) {
		t.Helper()
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil || os.WriteFile(filepath.Join(work, name+".pending"), data, 0600) != nil {
			t.Fatal("cannot persist fork acceptance evidence")
		}
		if err = os.Rename(filepath.Join(work, name+".pending"), filepath.Join(work, name)); err != nil {
			t.Fatal(err)
		}
	}
	signal("fork-waiting.json", map[string]any{"pull": number, "run": runID, "head": pull.HeadSHA, "fork": pull.HeadRepository, "commentId": published.CommentID, "checkId": published.CheckID, "buildCount": 0, "roomCount": 0, "evidence": directory})
	t.Logf("actual fork PR #%d is waiting for a maintainer comment; buildCount=0 roomCount=0", number)
	waitFor := func(condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Minute)
		for !condition() {
			if time.Now().After(deadline) {
				t.Fatal("actual GitHub state did not reach the required acceptance phase")
			}
			select {
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			case <-time.After(5 * time.Second):
			}
		}
	}
	waitFor(func() bool {
		pull.Approved, err = controller.approve(t.Context(), client, owner, repository, pull, run.GetCreatedAt().Time)
		if err != nil {
			t.Fatal(err)
		}
		return pull.Approved
	})
	accepted, err = controller.acceptRun(t.Context(), client, owner, repository, run, map[int]model.Pull{number: pull})
	if err != nil || !accepted {
		t.Fatal("actual maintainer approval did not accept the current fork artifact", err)
	}
	if err = controller.importRun(t.Context(), client, owner, repository, run); err != nil {
		t.Fatal(err)
	}
	values, err = builder.List(t.Context())
	if err != nil || len(values) != 1 {
		t.Fatal("actual approved artifact did not produce one build", err)
	}
	if err = manager.SyncPull(t.Context(), pull, values); err != nil {
		t.Fatal(err)
	}
	if err = scoped.Get(t.Context(), "pull", strconv.Itoa(number), &pull); err != nil || len(pull.Builds) != 1 || len(pull.Rooms) != 1 {
		t.Fatal("approved fork registration was not persisted", err)
	}
	value, err := builder.Get(t.Context(), pull.Builds["fabric"])
	if err != nil || value.Status != "ready" || value.SHA != run.GetHeadSHA() || value.Source.RunID != runID || value.Source.Event != "pull_request" {
		t.Fatal("approved fork did not produce an authoritative ready build", err)
	}
	room, err := manager.Get(t.Context(), pull.Rooms["fabric"])
	if err != nil || room.State != "stopped" || room.Generation != 0 || room.PR == nil || !room.PR.FromFork || room.BuildID != value.ID {
		t.Fatal("approved fork did not register a stopped database-only preview", err)
	}
	result := map[string]any{"pull": number, "run": runID, "head": pull.HeadSHA, "fork": pull.HeadRepository, "workflowPullRequestCount": len(run.PullRequests), "approvalRequired": true, "approved": true, "buildId": value.ID, "source": value.Source, "roomState": room.State, "generation": room.Generation, "unauthorizedComment": "requires another actual GitHub account without repository write permission", "evidence": directory}
	signal("fork-ready.json", result)
	t.Logf("approved fork PR #%d imported actual artifact; preview remains stopped generation0", number)
	waitFor(func() bool {
		actual, _, err = client.PullRequests.Get(t.Context(), owner, repository, number)
		if err != nil {
			t.Fatal(githubapp.SafeError("read actual fork closure", err))
		}
		return actual.GetState() == "closed"
	})
	pull = controller.pullValue(actual)
	if err = manager.SyncPull(t.Context(), pull, values); err != nil {
		t.Fatal(err)
	}
	if err = scoped.Get(t.Context(), "pull", strconv.Itoa(number), &pull); err != nil {
		t.Fatal(err)
	}
	if err = controller.publish(t.Context(), client, owner, repository, pull, values); err != nil {
		t.Fatal(err)
	}
	room, err = manager.Get(t.Context(), room.ID)
	if err != nil || room.State != "closed" || room.Generation != 0 {
		t.Fatal("closing the actual fork PR did not close its unused database preview", err)
	}
	comment, _, err = client.Issues.GetComment(t.Context(), owner, repository, published.CommentID)
	if err != nil || !strings.Contains(comment.GetBody(), "已关闭") || strings.Contains(comment.GetBody(), "/pack.mrpack?") {
		t.Fatal("closed actual fork comment retained a download link or omitted closure")
	}
	check, _, err = client.Checks.GetCheckRun(t.Context(), owner, repository, published.CheckID)
	if err != nil || check.GetStatus() != "completed" || check.GetConclusion() != "neutral" {
		t.Fatal("the unused actual fork preview did not close its check")
	}
	result["closed"] = true
	signal("fork-complete.json", result)
}
