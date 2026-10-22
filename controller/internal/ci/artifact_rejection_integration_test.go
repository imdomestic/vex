//go:build integration

package ci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/githubapp"
	"github.com/imdomestic/vex/controller/internal/gitmirror"
	"github.com/imdomestic/vex/controller/internal/store"
)

func TestRealGitHubArtifactsRejectedAndChecksFail(t *testing.T) {
	if os.Getenv("VEX_IT_GITHUB_MUTATIONS") != "1" {
		t.Skip("requires the explicitly authorized artifact rejection PR")
	}
	work := os.Getenv("VEX_IT_WORK")
	if !filepath.IsAbs(work) || !strings.Contains(filepath.Clean(work), "/.work/") {
		t.Fatal("an absolute ignored evidence directory is required")
	}
	number, err := strconv.Atoi(os.Getenv("VEX_IT_GITHUB_PR"))
	if err != nil || number <= 0 {
		t.Fatal("an actual isolated PR number is required")
	}
	runID, err := strconv.ParseInt(os.Getenv("VEX_IT_GITHUB_RUN"), 10, 64)
	if err != nil || runID <= 0 {
		t.Fatal("an actual successful workflow run is required")
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
	client, err := app.Repository(t.Context(), owner+"/"+repository)
	if err != nil {
		t.Fatal(err)
	}
	actual, _, err := client.PullRequests.Get(t.Context(), owner, repository, number)
	if err != nil {
		t.Fatal(githubapp.SafeError("read isolated artifact PR", err))
	}
	if actual.GetState() != "open" || actual.GetHead().GetRef() != "test/vex-invalid-artifacts" || actual.GetHead().GetRepo().GetFullName() != owner+"/"+repository {
		t.Fatal("the PR differs from the authorized artifact rejection branch")
	}
	run, _, err := client.Actions.GetWorkflowRunByID(t.Context(), owner, repository, runID)
	if err != nil {
		t.Fatal(githubapp.SafeError("read isolated artifact run", err))
	}
	if run.GetPath() != ".github/workflows/vex-invalid.yml" || run.GetHeadSHA() != actual.GetHead().GetSHA() || run.GetEvent() != "push" || run.GetStatus() != "completed" || run.GetConclusion() != "success" {
		t.Fatal("the real run has an unexpected identity or conclusion")
	}
	if err = os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(work, "github-artifacts-")
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
	results := []map[string]any{}
	for _, item := range []struct {
		name string
		want string
	}{
		{"bad-path", "unsafe"},
		{"bad-size", "size limit"},
		{"bad-hash", "checksum"},
		{"bad-metadata", "mod"},
	} {
		t.Run(item.name, func(t *testing.T) {
			cfg, err := resolved.Config.ForProject("example")
			if err != nil {
				t.Fatal(err)
			}
			cfg.ID, cfg.DataDir = item.name, filepath.Join(directory, item.name)
			cfg.Build.Source = "ci"
			cfg.CI.Workflow, cfg.CI.Artifact = run.GetPath(), item.name+".zip"
			// Poll 读取真实 push run，随后独立调用正式发布路径检查对应 PR。
			cfg.CI.PRPreviews = false
			db, err := store.Open(filepath.Join(cfg.DataDir, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			scoped, err := db.WithProject(cfg.ID)
			if err != nil {
				t.Fatal(err)
			}
			builder, err := build.New(cfg, scoped, mirror)
			if err != nil {
				t.Fatal(err)
			}
			manager := New(cfg, scoped, app, builder, mirror, nil, nil, nil)
			if err = manager.Poll(t.Context()); err != nil {
				t.Fatal(err)
			}
			var record runRecord
			if err = scoped.Get(t.Context(), "ci_run", runKey(runID, run.GetRunAttempt()), &record); err != nil {
				t.Fatal(err)
			}
			if record.Done || record.Status != "failed" || record.Attempt != 1 || record.HeadSHA != run.GetHeadSHA() || record.HeadRepository != owner+"/"+repository {
				t.Fatal("actual CI processing did not persist the rejected authoritative run")
			}
			values, err := builder.List(t.Context())
			if err != nil || len(values) != 0 {
				t.Fatal("rejected real artifacts produced build records", err)
			}
			// 再次走真实下载和导入，核对具体拒绝原因，防止把网络故障计入结果。
			cause := manager.importRun(t.Context(), client, owner, repository, run)
			if cause == nil || !strings.Contains(cause.Error(), item.want) {
				t.Fatalf("real artifact rejected at an unexpected boundary: %v", cause)
			}
			pull := manager.pullValue(actual)
			if err = manager.publish(t.Context(), client, owner, repository, pull, values); err != nil {
				t.Fatal(err)
			}
			var published publication
			if err = scoped.Get(t.Context(), "ci_publication", strconv.Itoa(number), &published); err != nil || published.CheckID == 0 {
				t.Fatal("real failure check identity was not persisted", err)
			}
			check, _, err := client.Checks.GetCheckRun(t.Context(), owner, repository, published.CheckID)
			if err != nil {
				t.Fatal(githubapp.SafeError("read real failure check", err))
			}
			if check.GetStatus() != "completed" || check.GetConclusion() != "failure" || check.GetHeadSHA() != run.GetHeadSHA() || check.GetExternalID() != cfg.ID+"/pr/"+strconv.Itoa(number) {
				t.Fatal("actual GitHub failure check differs from the rejected artifact identity")
			}
			results = append(results, map[string]any{"artifact": cfg.CI.Artifact, "run": runID, "head": run.GetHeadSHA(), "error": cause.Error(), "buildCount": len(values), "checkId": check.GetID(), "conclusion": check.GetConclusion()})
			t.Logf("actual artifact=%s run=%d check=%d conclusion=failure buildCount=0", cfg.CI.Artifact, runID, check.GetID())
		})
	}
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil || os.WriteFile(filepath.Join(directory, "results.json"), data, 0600) != nil {
		t.Fatal("cannot persist actual artifact rejection evidence")
	}
	t.Logf("actual GitHub artifact evidence=%s", directory)
}
