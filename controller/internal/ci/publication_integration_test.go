//go:build integration

package ci

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v88/github"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/githubapp"
	"github.com/imdomestic/vex/controller/internal/store"
)

func TestRealPreviewPublicationAndMaintainerApprovals(t *testing.T) {
	if os.Getenv("VEX_IT_GITHUB_MUTATIONS") != "1" {
		t.Skip("requires explicit authorization for the actual isolated example PR")
	}
	number, err := strconv.Atoi(os.Getenv("VEX_IT_GITHUB_PR"))
	if err != nil || number <= 0 {
		t.Fatal("actual preview PR number is required")
	}
	appID, err := strconv.ParseInt(os.Getenv("VEX_IT_GITHUB_APP_ID"), 10, 64)
	if err != nil || appID <= 0 {
		t.Fatal("installed GitHub App ID is required")
	}
	key, work := os.Getenv("VEX_IT_GITHUB_APP_KEY"), os.Getenv("VEX_IT_WORK")
	if key == "" || !filepath.IsAbs(work) || !strings.Contains(filepath.Clean(work), "/.work/") {
		t.Fatal("actual App key path and ignored evidence directory are required")
	}
	app, err := githubapp.New(config.GitHubApp{AppID: appID, PrivateKeyFile: key})
	if err != nil {
		t.Fatal("actual GitHub App initialization failed")
	}
	owner, repo := githubRepository(t, "VEX_IT_GITHUB_REPOSITORY")
	head := os.Getenv("VEX_IT_GITHUB_PR_HEAD")
	if head == "" {
		t.Fatal("VEX_IT_GITHUB_PR_HEAD must name the head branch of the authorized preview PR")
	}
	installation, err := app.Repository(t.Context(), owner+"/"+repo)
	if err != nil {
		t.Fatal(apiFailure("actual installation access", err))
	}
	client, _, err := newConditionalClient(installation)
	if err != nil {
		t.Fatal(err)
	}
	actual, _, err := client.PullRequests.Get(t.Context(), owner, repo, number)
	if err != nil {
		t.Fatal(apiFailure("read isolated preview PR", err))
	}
	if actual.GetState() != "open" || actual.GetHead().GetRef() != head || actual.GetHead().GetRepo().GetFullName() != owner+"/"+repo {
		t.Fatal("refusing mutation outside the explicitly owned preview PR")
	}
	if err = os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(work, "ci-publication-")
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(directory, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	scoped, err := db.WithProject("example")
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: config.ProjectConfig{ID: "example", GitHub: config.ProjectGitHub{Repository: owner + "/" + repo}, CI: config.ProjectCI{CommentLanguage: "zh-CN", PackLinkTTL: "168h", PreviewAccess: "members"}, Loaders: map[string]config.Loader{"fabric": {Enabled: true}}}, db: scoped, app: app}
	pull := m.pullValue(actual)
	if err = m.publish(t.Context(), client, owner, repo, pull, nil); err != nil {
		t.Fatal(err)
	}
	var publication publication
	if err = scoped.Get(t.Context(), "ci_publication", strconv.Itoa(number), &publication); err != nil || publication.CommentID <= 0 || publication.CheckID <= 0 {
		t.Fatal("actual publication identities were not persisted", err)
	}
	comment, _, err := client.Issues.GetComment(t.Context(), owner, repo, publication.CommentID)
	if err != nil || !strings.HasPrefix(comment.GetBody(), "<!-- vex:preview project=example -->\n") || !strings.Contains(comment.GetBody(), "等待 CI 产物") {
		t.Fatal("actual Chinese preview comment differs from the pending artifact state", apiFailure("read preview comment", err))
	}
	check, _, err := client.Checks.GetCheckRun(t.Context(), owner, repo, publication.CheckID)
	if err != nil || check.GetName() != "vex/preview" || check.GetHeadSHA() != pull.HeadSHA || check.GetStatus() != "queued" || check.GetOutput().GetSummary() != comment.GetBody() {
		t.Fatal("actual check identity, status or summary differs from the preview", apiFailure("read preview check", err))
	}
	if err = m.publish(t.Context(), client, owner, repo, pull, nil); err != nil {
		t.Fatal(err)
	}
	count := 0
	for value, err := range client.Issues.ListCommentsIter(t.Context(), owner, repo, number, &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}) {
		if err != nil {
			t.Fatal(apiFailure("count actual preview comments", err))
		}
		if value.GetUser().GetLogin() == app.BotLogin() && strings.HasPrefix(value.GetBody(), "<!-- vex:preview project=example -->\n") {
			count++
			if value.GetID() != publication.CommentID || !value.GetUpdatedAt().Equal(comment.GetUpdatedAt()) {
				t.Fatal("identical publication rewrote or replaced the actual comment")
			}
		}
	}
	if count != 1 {
		t.Fatal("actual PR does not contain exactly one owned preview comment")
	}
	cutoff := time.Now().UTC().Truncate(time.Second)
	approved, err := m.approve(t.Context(), client, owner, repo, pull, cutoff)
	if err != nil || approved {
		t.Fatal("a new approval window was authorized before a maintainer action", err)
	}
	gh := func(arguments ...string) {
		t.Helper()
		command := exec.CommandContext(t.Context(), "gh", arguments...)
		command.Stdout, command.Stderr = io.Discard, io.Discard
		if err := command.Run(); err != nil {
			t.Fatal("authorized GitHub CLI operation failed")
		}
	}
	gh("pr", "comment", strconv.Itoa(number), "--repo", owner+"/"+repo, "--body", "/vex preview")
	approved, err = m.approve(t.Context(), client, owner, repo, pull, cutoff)
	if err != nil || !approved {
		t.Fatal("actual maintainer comment did not pass repository permission verification", err)
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	case <-timer.C:
	}
	cutoff = time.Now().UTC().Truncate(time.Second)
	labels, _, err := client.Issues.ListLabels(t.Context(), owner, repo, &github.ListOptions{PerPage: 100})
	if err != nil {
		t.Fatal(apiFailure("read preview labels", err))
	}
	exists := false
	for _, label := range labels {
		exists = exists || label.GetName() == "vex-preview"
	}
	if !exists {
		gh("label", "create", "vex-preview", "--repo", owner+"/"+repo, "--color", "2DA44E", "--description", "Approve Vex preview for the current pull request head")
	}
	gh("pr", "edit", strconv.Itoa(number), "--repo", owner+"/"+repo, "--add-label", "vex-preview")
	approved, err = m.approve(t.Context(), client, owner, repo, pull, cutoff)
	if err != nil || !approved {
		t.Fatal("actual maintainer label event did not pass repository permission verification", err)
	}
	result := map[string]any{"repository": owner + "/" + repo, "pull": number, "head": pull.HeadSHA, "commentId": publication.CommentID, "checkId": publication.CheckID, "commentCount": count, "status": check.GetStatus(), "commentApproval": true, "labelApproval": true}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil || os.WriteFile(filepath.Join(directory, "result.json"), encoded, 0600) != nil {
		t.Fatal("cannot persist publication evidence")
	}
	t.Logf("actual PR #%d: one idempotent Chinese comment, head-bound vex/preview queued check, maintainer comment and label permissions verified; evidence=%s", number, directory)
}
