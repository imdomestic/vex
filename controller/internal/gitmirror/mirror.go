package gitmirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/model"
)

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

type Ref struct {
	Name string
	Head string
}

type Mirror struct {
	remote string
	dir    string
	mu     sync.Mutex
}

func New(remote, mirrorDir string) (*Mirror, error) {
	if remote == "" || !filepath.IsAbs(mirrorDir) {
		return nil, fmt.Errorf("git remote and absolute mirror directory are required")
	}
	return &Mirror{remote: remote, dir: filepath.Clean(mirrorDir)}, nil
}

func (m *Mirror) Refresh(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := os.Stat(m.dir)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(filepath.Dir(m.dir), 0750); err != nil {
			return err
		}
		stage, err := os.MkdirTemp(filepath.Dir(m.dir), ".mirror-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(stage)
		if _, err = command(ctx, "", "clone", "--mirror", "--", m.remote, stage); err != nil {
			return err
		}
		return os.Rename(stage, m.dir)
	}
	if err != nil {
		return err
	}
	bare, err := m.git(ctx, "rev-parse", "--is-bare-repository")
	if err != nil {
		return err
	}
	if strings.TrimSpace(bare) != "true" {
		return fmt.Errorf("git mirror directory is not a bare repository")
	}
	remote, err := m.git(ctx, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	if strings.TrimSpace(remote) != m.remote {
		return fmt.Errorf("git mirror origin differs from configured remote")
	}
	_, err = m.git(ctx, "fetch", "--prune", "--force", "origin")
	return err
}

func (m *Mirror) Branches(ctx context.Context) ([]Ref, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.branches(ctx)
}

func (m *Mirror) branches(ctx context.Context) ([]Ref, error) {
	output, err := m.git(ctx, "for-each-ref", "--format=%(refname)%00%(objectname)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	refs := []Ref{}
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if line == "" {
			continue
		}
		name, sha, ok := strings.Cut(line, "\x00")
		if !ok || !strings.HasPrefix(name, "refs/heads/") || !fullSHA.MatchString(sha) {
			return nil, fmt.Errorf("invalid git for-each-ref output")
		}
		refs = append(refs, Ref{Name: strings.TrimPrefix(name, "refs/heads/"), Head: sha})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	return refs, nil
}

func (m *Mirror) VerifyCommit(ctx context.Context, sha string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.verifyCommit(ctx, sha)
}

func (m *Mirror) verifyCommit(ctx context.Context, sha string) error {
	if !fullSHA.MatchString(sha) {
		return model.Failure("invalid_request", "commit sha must contain 40 lowercase hexadecimal characters")
	}
	kind, err := m.git(ctx, "cat-file", "-t", sha)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var failure *exec.ExitError
		if !errors.As(err, &failure) { return err }
		return model.Failure("not_found", "commit is absent from the fetched repository")
	}
	if strings.TrimSpace(kind) != "commit" { return model.Failure("not_found", "object is not a commit") }
	return nil
}

func (m *Mirror) Commits(ctx context.Context, branch, before string, limit int) ([]model.Commit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit < 1 {
		return nil, model.Failure("invalid_request", "commit limit must be positive")
	}
	if before != "" && !fullSHA.MatchString(before) {
		return nil, model.Failure("invalid_request", "before must be a full commit sha")
	}
	refs, err := m.branches(ctx)
	if err != nil {
		return nil, err
	}
	found := false
	for _, ref := range refs {
		if ref.Name == branch {
			found = true
			break
		}
	}
	if !found {
		return nil, model.Failure("not_found", "branch is absent from the fetched repository")
	}
	output, err := m.git(ctx, "rev-list", "--topo-order", "refs/heads/"+branch, "--")
	if err != nil {
		return nil, err
	}
	shas := strings.Fields(output)
	start := 0
	if before != "" {
		start = -1
		for i, sha := range shas {
			if sha == before {
				start = i + 1
				break
			}
		}
		if start == -1 {
			return nil, model.Failure("not_found", "commit cursor is absent from this branch")
		}
	}
	end := len(shas)
	if limit < end-start {
		end = start + limit
	}
	names, err := m.refNames(ctx)
	if err != nil {
		return nil, err
	}
	commits := make([]model.Commit, 0, end-start)
	for _, sha := range shas[start:end] {
		if !fullSHA.MatchString(sha) {
			return nil, fmt.Errorf("invalid git rev-list output")
		}
		output, err = m.git(ctx, "show", "--no-patch", "--format=%H%x00%P%x00%s%x00%an%x00%aI", sha, "--")
		if err != nil {
			return nil, err
		}
		fields := strings.Split(strings.TrimSuffix(output, "\n"), "\x00")
		if len(fields) != 5 || fields[0] != sha {
			return nil, fmt.Errorf("invalid git show output")
		}
		when, err := time.Parse(time.RFC3339, fields[4])
		if err != nil {
			return nil, fmt.Errorf("git author date: %w", err)
		}
		parents := append([]string{}, strings.Fields(fields[1])...)
		commitRefs := append([]string{}, names[sha]...)
		commits = append(commits, model.Commit{SHA: sha, Parents: parents, Subject: fields[2], Author: fields[3], Time: when.UTC(), Refs: commitRefs, Builds: map[string]*model.BuildSummary{}})
	}
	return commits, nil
}

func (m *Mirror) refNames(ctx context.Context) (map[string][]string, error) {
	output, err := m.git(ctx, "for-each-ref", "--format=%(objectname)%00%(refname:short)%00%(*objectname)", "refs/heads/", "refs/tags/")
	if err != nil {
		return nil, err
	}
	names := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		if len(fields) != 3 {
			return nil, fmt.Errorf("invalid git ref metadata output")
		}
		sha := fields[0]
		if fields[2] != "" {
			sha = fields[2]
		}
		if !fullSHA.MatchString(sha) {
			return nil, fmt.Errorf("invalid git ref sha")
		}
		names[sha] = append(names[sha], fields[1])
	}
	for sha := range names {
		sort.Strings(names[sha])
	}
	return names, nil
}

func (m *Mirror) Checkout(ctx context.Context, sha, destination string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !filepath.IsAbs(destination) {
		return fmt.Errorf("worktree destination must be absolute")
	}
	if err := m.verifyCommit(ctx, sha); err != nil {
		return err
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("worktree destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		return err
	}
	_, err := m.git(ctx, "worktree", "add", "--detach", "--", destination, sha)
	return err
}

func (m *Mirror) RemoveCheckout(ctx context.Context, destination string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !filepath.IsAbs(destination) {
		return fmt.Errorf("worktree destination must be absolute")
	}
	common, err := command(ctx, destination, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	actual, err := filepath.EvalSymlinks(strings.TrimSpace(common))
	if err != nil {
		return err
	}
	expected, err := filepath.EvalSymlinks(m.dir)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("worktree belongs to a different repository")
	}
	_, err = m.git(ctx, "worktree", "remove", "--force", "--", destination)
	return err
}

func (m *Mirror) git(ctx context.Context, args ...string) (string, error) {
	return command(ctx, "", append([]string{"--git-dir=" + m.dir}, args...)...)
}

func command(ctx context.Context, directory string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git operation failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}
