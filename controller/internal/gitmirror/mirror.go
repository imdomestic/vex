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
	"syscall"
	"time"

	"github.com/imdomestic/vex/controller/internal/model"
	"golang.org/x/sys/unix"
)

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

type Ref struct {
	Name string
	Head string
}

type Mirror struct {
	remote      string
	dir         string
	credentials Credentials
	mu          contextMutex
	refreshMu   contextMutex
}

func New(remote, mirrorDir string) (*Mirror, error) {
	return NewWithCredentials(remote, mirrorDir, Credentials{})
}

type Credentials struct {
	SSHIdentityFile string
	KnownHostsFile  string
	HTTPSTokenFile  string
	HTTPSUsername   string
	HelperBinary    string
}

func NewWithCredentials(remote, mirrorDir string, credentials Credentials) (*Mirror, error) {
	if remote == "" || !filepath.IsAbs(mirrorDir) {
		return nil, fmt.Errorf("git remote and absolute mirror directory are required")
	}
	if credentials.SSHIdentityFile != "" && (credentials.KnownHostsFile == "" || credentials.HTTPSTokenFile != "") {
		return nil, fmt.Errorf("SSH credentials require known_hosts and exclude HTTPS credentials")
	}
	for _, path := range []string{credentials.SSHIdentityFile, credentials.KnownHostsFile, credentials.HTTPSTokenFile, credentials.HelperBinary} {
		if path != "" && !filepath.IsAbs(path) {
			return nil, fmt.Errorf("git credential paths must be absolute")
		}
	}
	if credentials.HTTPSTokenFile != "" && credentials.HelperBinary == "" {
		return nil, fmt.Errorf("HTTPS credentials require the vexd credential helper")
	}
	return &Mirror{remote: remote, dir: filepath.Clean(mirrorDir), credentials: credentials, mu: newContextMutex(), refreshMu: newContextMutex()}, nil
}

func (m *Mirror) Initialize(ctx context.Context) error {
	if err := m.mu.Lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if _, err := os.Stat(m.dir); err == nil {
		return m.validate(ctx)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.dir), 0750); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(m.dir), ".mirror-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if _, err = m.command(ctx, "", "init", "--bare", "--", stage); err != nil {
		return err
	}
	if _, err = m.command(ctx, "", "--git-dir="+stage, "remote", "add", "--mirror=fetch", "origin", m.remote); err != nil {
		return err
	}
	return os.Rename(stage, m.dir)
}

func (m *Mirror) validate(ctx context.Context) error {
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
	return nil
}

func (m *Mirror) Refresh(ctx context.Context) error {
	if err := m.refreshMu.Lock(ctx); err != nil {
		return err
	}
	defer m.refreshMu.Unlock()
	if err := m.mu.Lock(ctx); err != nil {
		return err
	}
	locked := true
	defer func() {
		if locked {
			m.mu.Unlock()
		}
	}()
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
		if _, err = m.command(ctx, "", "clone", "--mirror", "--", m.remote, stage); err != nil {
			return err
		}
		return os.Rename(stage, m.dir)
	}
	if err != nil {
		return err
	}
	if err = m.validate(ctx); err != nil {
		return err
	}
	m.mu.Unlock()
	locked = false
	_, err = m.git(ctx, "fetch", "--prune", "--force", "origin")
	return err
}

func (m *Mirror) Branches(ctx context.Context) ([]Ref, error) {
	if err := m.mu.Lock(ctx); err != nil {
		return nil, err
	}
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
	if err := m.mu.Lock(ctx); err != nil {
		return err
	}
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
		if !errors.As(err, &failure) {
			return err
		}
		return model.Failure("not_found", "commit is absent from the local git mirror; a successful remote refresh is required before building it")
	}
	if strings.TrimSpace(kind) != "commit" {
		return model.Failure("not_found", "object is not a commit")
	}
	return nil
}

func (m *Mirror) Commits(ctx context.Context, branch, before string, limit int) ([]model.Commit, error) {
	if err := m.mu.Lock(ctx); err != nil {
		return nil, err
	}
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
	if err := m.mu.Lock(ctx); err != nil {
		return err
	}
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
	if _, err := command(ctx, "", "clone", "--no-hardlinks", "--no-checkout", "--", m.dir, destination); err != nil {
		return err
	}
	_, err := command(ctx, destination, "checkout", "--detach", sha)
	return err
}

func (m *Mirror) RemoveCheckout(ctx context.Context, destination string) error {
	if err := m.mu.Lock(ctx); err != nil {
		return err
	}
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
	expected, err := filepath.EvalSymlinks(filepath.Join(destination, ".git"))
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("checkout must have independent Git metadata")
	}
	origin, err := command(ctx, destination, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	if strings.TrimSpace(origin) != m.dir {
		return fmt.Errorf("checkout belongs to a different repository")
	}
	if err := filepath.WalkDir(destination, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if err := unix.Access(path, unix.W_OK|unix.X_OK); err != nil {
				return fmt.Errorf("checkout directory cannot be removed: %w", err)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return os.RemoveAll(destination)
}

func (m *Mirror) git(ctx context.Context, args ...string) (string, error) {
	return m.command(ctx, "", append([]string{"--git-dir=" + m.dir}, args...)...)
}

func command(ctx context.Context, directory string, args ...string) (string, error) {
	return (&Mirror{}).command(ctx, directory, args...)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func cleanEnvironment() []string {
	environment := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") || key == "SSH_AUTH_SOCK" || key == "SSH_ASKPASS" || key == "SSH_ASKPASS_REQUIRE" {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
}

func (m *Mirror) command(ctx context.Context, directory string, args ...string) (string, error) {
	settings := []string{"-c", "core.hooksPath=/dev/null", "-c", "credential.helper="}
	credentials := m.credentials
	if credentials.HTTPSTokenFile != "" {
		helper := "!" + shellQuote(credentials.HelperBinary) + " git-credential --token-file " + shellQuote(credentials.HTTPSTokenFile) + " --username " + shellQuote(credentials.HTTPSUsername)
		settings = append(settings, "-c", "credential.helper="+helper)
	}
	command := exec.CommandContext(ctx, "git", append(settings, args...)...)
	command.Dir = directory
	command.Env = cleanEnvironment()
	ssh := "ssh -F /dev/null -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes"
	if credentials.SSHIdentityFile != "" {
		ssh += " -i " + shellQuote(credentials.SSHIdentityFile) + " -o UserKnownHostsFile=" + shellQuote(credentials.KnownHostsFile)
	} else {
		ssh += " -o IdentityFile=none -o UserKnownHostsFile=/dev/null"
	}
	command.Env = append(command.Env, "GIT_SSH_COMMAND="+ssh)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = 250 * time.Millisecond
	output, err := command.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("git operation failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}
