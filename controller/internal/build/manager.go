package build

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
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/gitmirror"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
)

var buildIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*-[0-9a-f]{12}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Manager struct {
	cfg    config.Config
	db     *store.Store
	mirror *gitmirror.Mirror
}

type accessRecord struct {
	At time.Time `json:"at"`
}

func New(cfg config.Config, db *store.Store, mirror *gitmirror.Mirror) (*Manager, error) {
	if !filepath.IsAbs(cfg.Server.DataDir) || db == nil || mirror == nil {
		return nil, fmt.Errorf("build manager requires an absolute data directory, database and mirror")
	}
	for _, dir := range []string{"builds", "deps", "worktrees", ".work", "gradle-home"} {
		if err := os.MkdirAll(filepath.Join(cfg.Server.DataDir, dir), 0750); err != nil {
			return nil, err
		}
	}
	return &Manager{cfg: cfg, db: db, mirror: mirror}, nil
}

func (m *Manager) Request(ctx context.Context, sha, loader string, force bool) (model.Build, bool, error) {
	var result model.Build
	selected, ok := m.cfg.Loaders[loader]
	if !ok || !selected.Enabled {
		return result, false, model.Failure("invalid_request", "loader is disabled")
	}
	if err := m.mirror.VerifyCommit(ctx, sha); err != nil {
		return result, false, err
	}
	id := loader + "-" + sha[:12]
	created := false
	err := m.db.Update(ctx, func(tx *store.Tx) error {
		err := tx.Get(ctx, "build", id, &result)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err == nil {
			if result.SHA != sha {
				return model.Failure("conflict", "build ID collides with another full commit SHA")
			}
			if result.Status == "queued" || result.Status == "building" || !force {
				if result.Status == "ready" {
					return tx.Put(ctx, "build_access", id, accessRecord{At: time.Now().UTC()})
				}
				return nil
			}
			if result.Status == "ready" {
				rooms, err := store.All[model.Room](ctx, tx, "room")
				if err != nil {
					return err
				}
				for _, room := range rooms {
					if room.BuildID == id && room.State != "closed" {
						return model.Failure("conflict", "build is referenced by a registered room")
					}
				}
			}
		}
		attempt, err := tx.Next(ctx, "build_attempt:"+id)
		if err != nil {
			return err
		}
		result = model.Build{
			ID: id, SHA: sha, Loader: loader, Status: "queued", Attempt: attempt,
			QueuedAt: time.Now().UTC(), ModIDs: []string{}, Files: []model.BuildFile{},
			Log: "/v1/builds/" + id + "/log",
		}
		if err = os.MkdirAll(m.attemptDir(result), 0750); err != nil {
			return err
		}
		log, err := os.OpenFile(filepath.Join(m.attemptDir(result), "build.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
		if err != nil {
			return err
		}
		if err = log.Close(); err != nil {
			return err
		}
		if err = tx.Put(ctx, "build", id, result); err != nil {
			return err
		}
		created = true
		return tx.Put(ctx, "build_access", id, accessRecord{At: result.QueuedAt})
	})
	return result, created, err
}

func (m *Manager) Get(ctx context.Context, id string) (model.Build, error) {
	var value model.Build
	if !buildIDPattern.MatchString(id) {
		return value, model.Failure("not_found", "build does not exist")
	}
	if err := m.db.Get(ctx, "build", id, &value); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return value, model.Failure("not_found", "build does not exist")
		}
		return value, err
	}
	return value, nil
}

func (m *Manager) List(ctx context.Context) ([]model.Build, error) {
	values, err := store.All[model.Build](ctx, m.db, "build")
	if err != nil {
		return nil, err
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].QueuedAt.Equal(values[j].QueuedAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].QueuedAt.After(values[j].QueuedAt)
	})
	return values, nil
}

func (m *Manager) OpenFile(ctx context.Context, id, name string) (*os.File, error) {
	var file *os.File
	err := m.db.Update(ctx, func(tx *store.Tx) error {
		var value model.Build
		if err := tx.Get(ctx, "build", id, &value); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return model.Failure("not_found", "build does not exist")
			}
			return err
		}
		if value.Status != "ready" {
			return model.Failure("conflict", "build is not ready")
		}
		for _, entry := range value.Files {
			if entry.Name != name {
				continue
			}
			var err error
			if strings.HasPrefix(entry.URL, "/v1/deps/") {
				file, err = m.OpenDependency(ctx, entry.SHA256)
			} else {
				file, err = os.Open(filepath.Join(m.attemptDir(value), name))
			}
			if err != nil {
				return err
			}
			return tx.Put(ctx, "build_access", id, accessRecord{At: time.Now().UTC()})
		}
		return model.Failure("not_found", "build file does not exist")
	})
	if err != nil && file != nil {
		file.Close()
		file = nil
	}
	return file, err
}

func (m *Manager) OpenLog(ctx context.Context, id string) (*os.File, error) {
	value, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(filepath.Join(m.attemptDir(value), "build.log"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, model.Failure("not_found", "build log is not available yet")
	}
	return file, err
}

func (m *Manager) OpenDependency(ctx context.Context, digest string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !digestPattern.MatchString(digest) {
		return nil, model.Failure("not_found", "dependency does not exist")
	}
	file, err := os.Open(filepath.Join(m.cfg.Server.DataDir, "deps", digest+".jar"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, model.Failure("not_found", "dependency does not exist")
	}
	return file, err
}

func (m *Manager) attemptDir(value model.Build) string {
	return filepath.Join(m.cfg.Server.DataDir, "builds", value.ID, fmt.Sprintf("attempt-%d", value.Attempt))
}

func (m *Manager) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := m.step(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *Manager) step(ctx context.Context) error {
	if err := m.prune(ctx); err != nil {
		return err
	}
	values, err := m.List(ctx)
	if err != nil {
		return err
	}
	for i := len(values) - 1; i >= 0; i-- {
		value := values[i]
		if value.Status != "queued" && value.Status != "building" {
			continue
		}
		active, err := m.unitActive(ctx, value)
		if err != nil {
			return err
		}
		if active {
			return nil
		}
		if value.Status == "building" {
			if err = m.fail(ctx, value, fmt.Errorf("build worker terminated before publishing artifacts")); err != nil {
				return err
			}
			continue
		}
		if err = m.launch(ctx, value); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			current, readErr := m.Get(ctx, value.ID)
			if readErr != nil {
				return readErr
			}
			if current.Status == "queued" || current.Status == "building" {
				return m.fail(ctx, current, err)
			}
		}
		return nil
	}
	return nil
}

func (m *Manager) unitName(value model.Build) string {
	if m.cfg.Build.SystemdUser {
		return fmt.Sprintf("chorus-build-%s-%d.service", value.ID, value.Attempt)
	}
	return "chorus-build@" + value.ID + ".service"
}

func (m *Manager) unitActive(ctx context.Context, value model.Build) (bool, error) {
	args := []string{}
	if m.cfg.Build.SystemdUser {
		args = append(args, "--user")
	}
	args = append(args, "show", "--property=ActiveState", "--property=SubState", m.unitName(value))
	output, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	state, substate := "", ""
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if key == "ActiveState" {
			state = value
		}
		if key == "SubState" {
			substate = value
		}
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 4 && state == "inactive" {
			return false, nil
		}
		return false, fmt.Errorf("inspect build service: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return (state == "active" && substate != "exited") || state == "activating" || state == "deactivating" || state == "reloading", nil
}

func (m *Manager) launch(ctx context.Context, value model.Build) error {
	var command *exec.Cmd
	if m.cfg.Build.SystemdUser {
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		args := []string{"--user", "--quiet", "--collect", "--wait", "--service-type=exec", "--unit=" + m.unitName(value),
			"--property=MemoryHigh=5G", "--property=MemoryMax=6G", "--property=CPUQuota=200%", "--property=KillMode=control-group",
			"--property=WorkingDirectory=" + m.cfg.Server.DataDir,
			"--setenv=TMPDIR=" + filepath.Join(m.cfg.Server.DataDir, ".work"),
			"--setenv=SQLITE_TMPDIR=" + filepath.Join(m.cfg.Server.DataDir, ".work"),
			binary, "build-worker", "--config", m.cfg.Path, "--id", value.ID}
		command = exec.CommandContext(ctx, "systemd-run", args...)
	} else {
		if err := os.MkdirAll(filepath.Join(m.cfg.Server.DataDir, "work", value.ID, ".work"), 0750); err != nil {
			return err
		}
		load, err := m.unitProperty(ctx, value, "LoadState")
		if err != nil {
			return err
		}
		state, err := m.unitProperty(ctx, value, "ActiveState")
		if err != nil {
			return err
		}
		operations, err := prepareOperations(load, state)
		if err != nil {
			return err
		}
		for _, operation := range operations {
			output, err := exec.CommandContext(ctx, "systemctl", operation, m.unitName(value)).CombinedOutput()
			if err != nil {
				return fmt.Errorf("prepare build service: %w: %s", err, strings.TrimSpace(string(output)))
			}
		}
		command = exec.CommandContext(ctx, "systemctl", "start", m.unitName(value))
	}
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("start build service: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (m *Manager) unitProperty(ctx context.Context, value model.Build, property string) (string, error) {
	output, err := exec.CommandContext(ctx, "systemctl", "show", "--value", "--property="+property, m.unitName(value)).CombinedOutput()
	state := strings.TrimSpace(string(output))
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 4 && (state == "not-found" || state == "inactive") {
			return state, nil
		}
		return "", fmt.Errorf("inspect build service %s: %w: %s", property, err, state)
	}
	return state, nil
}

func prepareOperations(load, state string) ([]string, error) {
	if load == "not-found" && state == "inactive" {
		return nil, nil
	}
	if load != "loaded" {
		return nil, fmt.Errorf("build service cannot start with LoadState=%s", load)
	}
	switch state {
	case "inactive":
		return nil, nil
	case "failed":
		return []string{"reset-failed"}, nil
	case "active":
		return []string{"stop"}, nil
	default:
		return nil, fmt.Errorf("build service cannot start with ActiveState=%s", state)
	}
}

func (m *Manager) fail(ctx context.Context, value model.Build, cause error) error {
	return m.db.Update(ctx, func(tx *store.Tx) error {
		var current model.Build
		if err := tx.Get(ctx, "build", value.ID, &current); err != nil {
			return err
		}
		if current.Attempt != value.Attempt || current.Status == "ready" || current.Status == "failed" {
			return nil
		}
		now := time.Now().UTC()
		current.Status = "failed"
		current.FinishedAt = &now
		if current.StartedAt != nil {
			duration := now.Sub(*current.StartedAt).Seconds()
			current.DurationSec = &duration
		}
		current.LastError = model.Failure("build_failed", cause.Error())
		return tx.Put(ctx, "build", current.ID, current)
	})
}

func (m *Manager) prune(ctx context.Context) error {
	err := m.db.Update(ctx, func(tx *store.Tx) error {
		values, err := store.All[model.Build](ctx, tx, "build")
		if err != nil {
			return err
		}
		rooms, err := store.All[model.Room](ctx, tx, "room")
		if err != nil {
			return err
		}
		protected := map[string]bool{}
		for _, room := range rooms {
			if room.State != "closed" {
				protected[room.BuildID] = true
			}
		}
		ready := []model.Build{}
		times := map[string]time.Time{}
		failedCutoff := time.Now().UTC().Add(-7 * 24 * time.Hour)
		for _, value := range values {
			if value.Status == "failed" && value.FinishedAt != nil && !value.FinishedAt.After(failedCutoff) {
				if err = tx.Delete(ctx, "build", value.ID); err != nil {
					return err
				}
				if err = tx.Delete(ctx, "build_access", value.ID); err != nil {
					return err
				}
				continue
			}
			if value.Status != "ready" {
				continue
			}
			ready = append(ready, value)
			access := accessRecord{At: value.QueuedAt}
			if err = tx.Get(ctx, "build_access", value.ID, &access); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			times[value.ID] = access.At
		}
		sort.Slice(ready, func(i, j int) bool { return times[ready[i].ID].Before(times[ready[j].ID]) })
		remaining := len(ready)
		for _, value := range ready {
			if remaining <= m.cfg.Build.CacheSize {
				break
			}
			if protected[value.ID] {
				continue
			}
			if err = tx.Delete(ctx, "build", value.ID); err != nil {
				return err
			}
			if err = tx.Delete(ctx, "build_access", value.ID); err != nil {
				return err
			}
			remaining--
		}
		return nil
	})
	if err != nil {
		return err
	}
	return m.db.Update(ctx, func(tx *store.Tx) error {
		entries, err := os.ReadDir(filepath.Join(m.cfg.Server.DataDir, "builds"))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() || !buildIDPattern.MatchString(entry.Name()) {
				continue
			}
			var value model.Build
			err = tx.Get(ctx, "build", entry.Name(), &value)
			if errors.Is(err, store.ErrNotFound) {
				if err = os.RemoveAll(filepath.Join(m.cfg.Server.DataDir, "builds", entry.Name())); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}
		return nil
	})
}
