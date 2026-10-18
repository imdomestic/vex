package ci

import (
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v88/github"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/githubapp"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

func TestMultiplePullsUseAuthoritativeHeadRepository(t *testing.T) {
	sha := strings.Repeat("a", 40)
	run := &github.WorkflowRun{HeadSHA: &sha, HeadRepository: &github.Repository{FullName: github.Ptr("owner/source")}}
	pulls := map[int]model.Pull{
		1: {Number: 1, State: "open", HeadSHA: sha, HeadRepository: "owner/source"},
		2: {Number: 2, State: "open", HeadSHA: sha, HeadRepository: "owner/source"},
		3: {Number: 3, State: "open", HeadSHA: sha, HeadRepository: "another/source"},
		4: {Number: 4, State: "closed", HeadSHA: sha, HeadRepository: "owner/source"},
	}
	if matched := matchingPulls(run, pulls); !slices.Equal(matched, []int{1, 2}) {
		t.Fatal("authoritative run head did not match all open pulls in its source repository")
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "ci.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	scoped, err := db.WithProject("example")
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: config.ProjectConfig{ID: "example"}, db: scoped}
	value := model.Build{Project: "example", SHA: sha, Source: model.BuildSource{Kind: "ci", RunID: 123, RunAttempt: 1, PRNumber: 99}}
	if err = scoped.Put(t.Context(), "ci_run", runKey(123, 1), runRecord{HeadSHA: sha, HeadRepository: "owner/source"}); err != nil {
		t.Fatal(err)
	}
	for number, pull := range pulls {
		matches, err := m.buildMatchesPull(t.Context(), value, pull)
		if err != nil || matches != (number != 3) {
			t.Fatal("persisted run provenance incorrectly used a single PR number", err)
		}
		pull.Project = "example"
		if err = scoped.Put(t.Context(), "pull", string(rune('0'+number)), pull); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := m.List(t.Context())
	if err != nil || len(listed) != 3 {
		t.Fatal("pull listing did not retain exactly the open pulls", err)
	}
	for _, pull := range listed {
		if pull.Rooms == nil || pull.Builds == nil {
			t.Fatal("pull listing emitted null object maps")
		}
	}
}

func TestPreviewChecksAndClosedSnapshotText(t *testing.T) {
	view := previewView{pull: model.Pull{Number: 7, HeadSHA: strings.Repeat("a", 40), State: "open", Approved: true}, validation: "validating"}
	if status, conclusion := previewCheck(view, true); status != "in_progress" || conclusion != "" {
		t.Fatal("accepted artifact must remain in progress during validation")
	}
	view.validation = "failed"
	if status, conclusion := previewCheck(view, true); status != "completed" || conclusion != "failure" {
		t.Fatal("rejected artifact must complete with failure")
	}
	view.validation = "ready"
	view.rows = []previewRow{{loader: "fabric", build: &model.Build{ID: "fabric-current", Status: "ready"}, room: &model.Room{ID: "pr7-fabric", BuildID: "fabric-old", State: "ready"}}}
	if status, _ := previewCheck(view, true); status != "in_progress" {
		t.Fatal("running previous head incorrectly completed the current head check")
	}
	view.rows[0].room.State = "stopped"
	if status, conclusion := previewCheck(view, true); status != "completed" || conclusion != "success" {
		t.Fatal("stopped preview with a ready current head and wake route was not enterable")
	}
	if status, _ := previewCheck(view, false); status != "in_progress" {
		t.Fatal("stopped preview without wake was reported enterable")
	}
	view.pull.State, view.rows[0].room.State, view.rows[0].snapshot = "closed", "archiving", "pr7-fabric-g2"
	if status, _ := previewCheck(view, true); status != "in_progress" {
		t.Fatal("PR closure skipped the actual archive lifecycle")
	}
	view.rows[0].room.State = "closed"
	for _, language := range []string{"en", "zh-CN"} {
		text := renderPreview("example", "members", language, view)
		if !strings.HasPrefix(text, "<!-- vex:preview project=example -->\n") || !strings.Contains(text, "pr7-fabric-g2") || language == "zh-CN" && !strings.Contains(text, "已关闭") {
			t.Fatal("closed preview comment omitted its exact marker or final snapshot")
		}
	}
}

func TestGitHubRateHeadersRespectLongestRequiredDelay(t *testing.T) {
	now := time.Unix(2000000000, 0)
	response := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"120"}, "X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"2000000180"}}}
	if got := responseBackoff(response, now); !got.Equal(now.Add(3 * time.Minute)) {
		t.Fatal("rate reset shortened GitHub's required retry delay")
	}
	response.Header = http.Header{"Retry-After": {now.Add(4 * time.Minute).UTC().Format(http.TimeFormat)}}
	if got := responseBackoff(response, now); !got.Equal(now.Add(4 * time.Minute)) {
		t.Fatal("HTTP-date Retry-After was not respected")
	}
	response.Header = http.Header{}
	if got := responseBackoff(response, now); !got.Equal(now.Add(time.Minute)) {
		t.Fatal("secondary rate limit without explicit delay did not wait at least one minute")
	}
}

func TestInstallationBackoffPrecedesClientInitialization(t *testing.T) {
	until := time.Now().UTC().Add(2 * time.Minute)
	remote := &githubapp.RemoteError{Operation: "read installation", Status: http.StatusTooManyRequests, RetryAt: until}
	failure := apiFailure("installation access", remote).(*apiError)
	if failure.status != http.StatusTooManyRequests || failure.retryAt != until {
		t.Fatal("installation error lost its safe retry metadata")
	}
	m := &Manager{nextPoll: failure.retryAt}
	if err := m.Poll(t.Context()); err != nil || m.client != nil || m.transport != nil {
		t.Fatal("poll attempted client initialization before the required retry time", err)
	}
}

func TestPreviewApprovalIsBoundToAnOpenHeadAndRepository(t *testing.T) {
	previous := model.Pull{Number: 7, State: "open", HeadSHA: strings.Repeat("a", 40), HeadRepository: "owner/source", Approved: true}
	current := previous
	if !sameOpenHead(previous, current) {
		t.Fatal("unchanged current head lost its maintainer approval identity")
	}
	current.HeadSHA = strings.Repeat("b", 40)
	if sameOpenHead(previous, current) {
		t.Fatal("new head reused a previous approval")
	}
	current = previous
	current.HeadRepository = "another/source"
	if sameOpenHead(previous, current) {
		t.Fatal("different source repository reused a previous approval")
	}
	current = previous
	current.State = "closed"
	if sameOpenHead(previous, current) {
		t.Fatal("closed PR remained eligible for approved preview registration")
	}
}
