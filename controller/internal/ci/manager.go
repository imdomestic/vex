package ci

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v88/github"
	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/events"
	"github.com/imdomestic/vex/controller/internal/githubapp"
	"github.com/imdomestic/vex/controller/internal/gitmirror"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/rooms"
	"github.com/imdomestic/vex/controller/internal/store"
)

type Manager struct {
	mu        sync.Mutex
	cfg       config.ProjectConfig
	db        *store.Store
	app       *githubapp.App
	builds    *build.Manager
	git       *gitmirror.Mirror
	rooms     *rooms.Manager
	auth      *auth.Auth
	events    *events.Bus
	refresh   chan struct{}
	client    *github.Client
	transport *conditionalTransport
	nextPoll  time.Time
}

type runRecord struct {
	Done           bool      `json:"done"`
	Attempt        int       `json:"attempt"`
	Next           time.Time `json:"next"`
	Error          string    `json:"error,omitempty"`
	Status         string    `json:"status"`
	HeadSHA        string    `json:"headSha"`
	HeadRepository string    `json:"headRepository"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type headRecord struct {
	SHA        string    `json:"sha"`
	Repository string    `json:"repository"`
	ObservedAt time.Time `json:"observedAt"`
	NotBefore  time.Time `json:"notBefore"`
}

var errApprovalPending = errors.New("fork preview is waiting for maintainer approval")

func New(cfg config.ProjectConfig, db *store.Store, app *githubapp.App, builds *build.Manager, git *gitmirror.Mirror, rooms *rooms.Manager, auth *auth.Auth, events *events.Bus) *Manager {
	return &Manager{cfg: cfg, db: db, app: app, builds: builds, git: git, rooms: rooms, auth: auth, events: events, refresh: make(chan struct{}, 1)}
}

func (m *Manager) RequestRefresh() {
	select {
	case m.refresh <- struct{}{}:
	default:
	}
}

func (m *Manager) Run(ctx context.Context) error {
	ticker := time.NewTicker(config.Duration(m.cfg.CI.PollInterval))
	defer ticker.Stop()
	for {
		if err := m.Poll(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var remote *apiError
			if !errors.As(err, &remote) {
				return err
			}
			retryAt := remote.retryAt
			if m.transport != nil {
				if until := m.transport.blocked(); until.After(retryAt) {
					retryAt = until
				}
			}
			slog.Warn("CI GitHub polling deferred", "project", m.cfg.ID, "error", remote, "retryAt", retryAt)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-m.refresh:
		}
	}
}

func (m *Manager) List(ctx context.Context) ([]model.Pull, error) {
	pulls, err := store.All[model.Pull](ctx, m.db, "pull")
	if err != nil {
		return nil, err
	}
	open := []model.Pull{}
	for _, pull := range pulls {
		if pull.State == "open" {
			if pull.Rooms == nil {
				pull.Rooms = map[string]string{}
			}
			if pull.Builds == nil {
				pull.Builds = map[string]string{}
			}
			open = append(open, pull)
		}
	}
	sort.Slice(open, func(i, j int) bool { return open[i].Number > open[j].Number })
	return open, nil
}

func runKey(id int64, attempt int) string {
	return strconv.FormatInt(id, 10) + ":" + strconv.Itoa(attempt)
}

func matchingPulls(run *github.WorkflowRun, pulls map[int]model.Pull) []int {
	matched := []int{}
	for number, pull := range pulls {
		if pull.State == "open" && pull.HeadSHA == run.GetHeadSHA() && strings.EqualFold(pull.HeadRepository, run.GetHeadRepository().GetFullName()) {
			matched = append(matched, number)
		}
	}
	sort.Ints(matched)
	return matched
}

func (m *Manager) Poll(ctx context.Context) (result error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer func() {
		var remote *apiError
		if errors.As(result, &remote) && remote.retryAt.After(m.nextPoll) {
			m.nextPoll = remote.retryAt
		}
	}()
	if time.Now().Before(m.nextPoll) || m.transport != nil && time.Now().Before(m.transport.blocked()) {
		return nil
	}
	if m.client == nil {
		client, err := m.app.Repository(ctx, m.cfg.GitHub.Repository)
		if err != nil {
			return apiFailure("installation access", err)
		}
		m.client, m.transport, err = newConditionalClient(client)
		if err != nil {
			return err
		}
	}
	client := m.client
	parts := strings.Split(m.cfg.GitHub.Repository, "/")
	owner, repo := parts[0], parts[1]
	pulls := map[int]model.Pull{}
	var err error
	if m.cfg.CI.PRPreviews {
		pulls, err = m.readPulls(ctx, client, owner, repo)
		if err != nil {
			return err
		}
	}
	options := &github.ListWorkflowRunsOptions{Status: "success", ListOptions: github.ListOptions{PerPage: 100}}
	runs := []*github.WorkflowRun{}
	for run, listErr := range client.Actions.ListWorkflowRunsByFileNameIter(ctx, owner, repo, filepath.Base(m.cfg.CI.Workflow), options) {
		if listErr != nil {
			return apiFailure("list successful workflows", listErr)
		}
		if run.GetPath() == m.cfg.CI.Workflow && strings.EqualFold(run.GetRepository().GetFullName(), m.cfg.GitHub.Repository) && run.GetStatus() == "completed" && run.GetConclusion() == "success" {
			runs = append(runs, run)
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].GetID() == runs[j].GetID() {
			return runs[i].GetRunAttempt() < runs[j].GetRunAttempt()
		}
		return runs[i].GetID() < runs[j].GetID()
	})
	for number, pull := range pulls {
		if pull.State != "open" || !pull.Fork || pull.Approved || m.cfg.CI.ForkPreviews != "approval" {
			continue
		}
		var created time.Time
		for _, run := range runs {
			if run.GetHeadSHA() == pull.HeadSHA && strings.EqualFold(run.GetHeadRepository().GetFullName(), pull.HeadRepository) && (created.IsZero() || run.GetCreatedAt().Before(created)) {
				created = run.GetCreatedAt().Time
			}
		}
		if created.IsZero() {
			continue
		}
		var head headRecord
		if err = m.db.Get(ctx, "ci_head", strconv.Itoa(number), &head); err != nil {
			return err
		}
		if head.NotBefore.After(created) {
			created = head.NotBefore
		}
		pull.Approved, err = m.approve(ctx, client, owner, repo, pull, created)
		if err != nil {
			return err
		}
		pulls[number] = pull
	}
	imported := false
	for _, run := range runs {
		key := runKey(run.GetID(), run.GetRunAttempt())
		var record runRecord
		if err = m.db.Get(ctx, "ci_run", key, &record); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if record.Done || record.Next.After(time.Now()) {
			continue
		}
		record.HeadSHA, record.HeadRepository = run.GetHeadSHA(), run.GetHeadRepository().GetFullName()
		accepted, acceptErr := m.acceptRun(ctx, client, owner, repo, run, pulls)
		if errors.Is(acceptErr, errApprovalPending) || !accepted && acceptErr == nil {
			continue
		}
		var failure *model.Error
		if errors.As(acceptErr, &failure) && failure.Code == "commit_unavailable" {
			// 刷新后运行的提交仍不在仓库的现有历史中，例如项目改写了历史；这个运行不再导入，也不阻止其他运行。
			record.Done, record.Status, record.Error, record.UpdatedAt = true, "skipped", failure.Message, time.Now().UTC()
			if err = m.db.Put(ctx, "ci_run", key, record); err != nil {
				return err
			}
			continue
		}
		if acceptErr != nil {
			return acceptErr
		}
		record.Status, record.Error, record.UpdatedAt = "validating", "", time.Now().UTC()
		if err = m.db.Put(ctx, "ci_run", key, record); err != nil {
			return err
		}
		for _, number := range matchingPulls(run, pulls) {
			if pulls[number].Approved {
				if err = m.publish(ctx, client, owner, repo, pulls[number], nil); err != nil {
					return err
				}
			}
		}
		imported = true
		var deferred error
		if err = m.importRun(ctx, client, owner, repo, run); err != nil {
			record.Attempt++
			record.Next = time.Now().UTC().Add(min(time.Minute*time.Duration(1<<min(record.Attempt-1, 6)), time.Hour))
			record.Status, record.Error = "failed", "CI artifact download, integrity or manifest verification failed"
			var remote *apiError
			var appRemote *githubapp.RemoteError
			if errors.As(err, &remote) || errors.As(err, &appRemote) {
				deferred = apiFailure("retrieve workflow artifact", err)
				record.Status, record.Error = "validating", "GitHub artifact retrieval deferred"
				if retry := deferred.(*apiError).retryAt; retry.After(record.Next) {
					record.Next = retry
				}
			} else {
				slog.Warn("CI artifact validation failed", "project", m.cfg.ID, "run", run.GetID(), "attempt", run.GetRunAttempt())
			}
		} else {
			record.Done, record.Status, record.Error = true, "ready", ""
		}
		record.UpdatedAt = time.Now().UTC()
		if err = m.db.Put(ctx, "ci_run", key, record); err != nil {
			return err
		}
		if deferred != nil {
			return deferred
		}
	}
	if imported && m.cfg.CI.PRPreviews {
		current, err := m.readPulls(ctx, client, owner, repo)
		if err != nil {
			return err
		}
		for number, pull := range current {
			previous := pulls[number]
			if sameOpenHead(previous, pull) {
				pull.Approved = previous.Approved
				current[number] = pull
			}
		}
		pulls = current
	}
	builds, err := m.builds.List(ctx)
	if err != nil {
		return err
	}
	for number, pull := range pulls {
		ready := []model.Build{}
		for _, value := range builds {
			matches, err := m.buildMatchesPull(ctx, value, pull)
			if err != nil {
				return err
			}
			if matches && value.Status == "ready" {
				ready = append(ready, value)
			}
		}
		if err = m.rooms.SyncPull(ctx, pull, ready); err != nil {
			return err
		}
		if err = m.db.Get(ctx, "pull", strconv.Itoa(number), &pull); err != nil {
			return err
		}
		if err = m.publish(ctx, client, owner, repo, pull, builds); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) readPulls(ctx context.Context, client *github.Client, owner, repo string) (map[int]model.Pull, error) {
	result := map[int]model.Pull{}
	for pull, err := range client.PullRequests.ListIter(ctx, owner, repo, &github.PullRequestListOptions{State: "open", ListOptions: github.ListOptions{PerPage: 100}}) {
		if err != nil {
			return nil, apiFailure("list pull requests", err)
		}
		result[pull.GetNumber()] = m.pullValue(pull)
	}
	stored, err := store.All[model.Pull](ctx, m.db, "pull")
	if err != nil {
		return nil, err
	}
	for _, previous := range stored {
		if current, exists := result[previous.Number]; exists {
			current.Rooms, current.Builds = previous.Rooms, previous.Builds
			if previous.HeadSHA == current.HeadSHA && previous.HeadRepository == current.HeadRepository {
				current.Approved = previous.Approved
			}
			result[previous.Number] = current
		} else if previous.State == "open" {
			pull, _, err := client.PullRequests.Get(ctx, owner, repo, previous.Number)
			if err != nil {
				return nil, apiFailure("read closed pull request", err)
			}
			current := m.pullValue(pull)
			current.Rooms, current.Builds = previous.Rooms, previous.Builds
			result[current.Number] = current
		} else {
			result[previous.Number] = previous
		}
	}
	for number, pull := range result {
		if pull.State != "open" {
			continue
		}
		key := strconv.Itoa(number)
		var previous headRecord
		if err = m.db.Get(ctx, "ci_head", key, &previous); err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if previous.SHA != pull.HeadSHA || previous.Repository != pull.HeadRepository {
			previous.NotBefore = previous.ObservedAt
		}
		previous.SHA, previous.Repository, previous.ObservedAt = pull.HeadSHA, pull.HeadRepository, time.Now().UTC()
		if err = m.db.Put(ctx, "ci_head", key, previous); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (m *Manager) pullValue(pull *github.PullRequest) model.Pull {
	head := pull.GetHead()
	fork := !strings.EqualFold(head.GetRepo().GetFullName(), m.cfg.GitHub.Repository)
	return model.Pull{Project: m.cfg.ID, Number: pull.GetNumber(), Title: pull.GetTitle(), URL: pull.GetHTMLURL(), Author: pull.GetUser().GetLogin(), State: pull.GetState(), Draft: pull.GetDraft(), HeadSHA: head.GetSHA(), HeadRepository: head.GetRepo().GetFullName(), BaseBranch: pull.GetBase().GetRef(), Fork: fork, Approved: !fork || m.cfg.CI.ForkPreviews == "auto", Rooms: map[string]string{}, Builds: map[string]string{}, UpdatedAt: pull.GetUpdatedAt().Time}
}

func sameOpenHead(left, right model.Pull) bool {
	return left.State == "open" && right.State == "open" && left.Number == right.Number && left.HeadSHA == right.HeadSHA && strings.EqualFold(left.HeadRepository, right.HeadRepository)
}

func (m *Manager) buildMatchesPull(ctx context.Context, value model.Build, pull model.Pull) (bool, error) {
	if value.Project != m.cfg.ID || value.SHA != pull.HeadSHA || value.Source.Kind != "ci" {
		return false, nil
	}
	var record runRecord
	err := m.db.Get(ctx, "ci_run", runKey(value.Source.RunID, value.Source.RunAttempt), &record)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil && record.HeadSHA == pull.HeadSHA && strings.EqualFold(record.HeadRepository, pull.HeadRepository), err
}

func (m *Manager) acceptRun(ctx context.Context, client *github.Client, owner, repo string, run *github.WorkflowRun, pulls map[int]model.Pull) (bool, error) {
	if run.GetConclusion() != "success" || run.GetStatus() != "completed" {
		return false, nil
	}
	switch run.GetEvent() {
	case "pull_request":
		if !m.cfg.CI.PRPreviews {
			return false, nil
		}
		for _, number := range matchingPulls(run, pulls) {
			pull := pulls[number]
			if pull.Fork && (m.cfg.CI.ForkPreviews == "off" || !pull.Approved) {
				continue
			}
			current, _, err := client.PullRequests.Get(ctx, owner, repo, number)
			if err != nil {
				return false, apiFailure("verify current preview head", err)
			}
			if current.GetState() != "open" || current.GetHead().GetSHA() != pull.HeadSHA || !strings.EqualFold(current.GetHead().GetRepo().GetFullName(), pull.HeadRepository) {
				continue
			}
			if err = m.git.FetchPull(ctx, number, run.GetHeadSHA()); err != nil {
				return false, err
			}
			return true, nil
		}
		return false, errApprovalPending
	case "push", "workflow_dispatch":
		if !strings.EqualFold(run.GetHeadRepository().GetFullName(), m.cfg.GitHub.Repository) {
			return false, nil
		}
		if err := m.git.VerifyCommit(ctx, run.GetHeadSHA()); err != nil {
			if err = m.git.Refresh(ctx); err != nil {
				return false, err
			}
			if err = m.git.VerifyCommit(ctx, run.GetHeadSHA()); err != nil {
				return false, err
			}
		}
		return true, nil
	default:
		return false, nil
	}
}

func (m *Manager) importRun(ctx context.Context, client *github.Client, owner, repo string, run *github.WorkflowRun) error {
	var selected *github.Artifact
	for artifact, err := range client.Actions.ListWorkflowRunArtifactsIter(ctx, owner, repo, run.GetID(), &github.ListOptions{PerPage: 100}) {
		if err != nil {
			return apiFailure("list run artifacts", err)
		}
		if artifact.GetName() == m.cfg.CI.Artifact && !artifact.GetExpired() {
			if selected != nil {
				return fmt.Errorf("workflow contains multiple matching artifacts")
			}
			selected = artifact
		}
	}
	if selected == nil {
		return fmt.Errorf("completed workflow has no configured artifact")
	}
	if selected.GetWorkflowRun().GetHeadSHA() != run.GetHeadSHA() || selected.GetWorkflowRun().GetID() != run.GetID() {
		return fmt.Errorf("artifact workflow identity differs")
	}
	filename, err := githubapp.Download(ctx, client, owner, repo, selected, filepath.Join(m.cfg.DataDir, ".work", "ci"))
	if err != nil {
		return err
	}
	defer os.Remove(filename)
	_, _, err = m.builds.ImportCI(ctx, build.CIImport{SHA: run.GetHeadSHA(), RunID: run.GetID(), RunAttempt: run.GetRunAttempt(), Repository: m.cfg.GitHub.Repository, HeadRepository: run.GetHeadRepository().GetFullName(), Workflow: m.cfg.CI.Workflow, Event: run.GetEvent(), HTMLURL: run.GetHTMLURL(), BundlePath: filename})
	return err
}
