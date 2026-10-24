package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/gitmirror"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

var buildIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*-[0-9a-f]{12}$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Manager struct {
	cfg    config.ProjectConfig
	db     *store.Store
	mirror *gitmirror.Mirror
}

type accessRecord struct {
	At time.Time `json:"at"`
}

func New(cfg config.ProjectConfig, db *store.Store, mirror *gitmirror.Mirror) (*Manager, error) {
	if !config.ValidID(cfg.ID) || !filepath.IsAbs(cfg.DataDir) || db == nil || db.Project() != cfg.ID || mirror == nil {
		return nil, fmt.Errorf("build manager requires an absolute data directory, database and mirror")
	}
	for _, dir := range []string{"builds", "deps", "work", ".work", "cache/gradle"} {
		if err := os.MkdirAll(filepath.Join(cfg.DataDir, dir), 0750); err != nil {
			return nil, err
		}
	}
	return &Manager{cfg: cfg, db: db, mirror: mirror}, nil
}

func (m *Manager) Request(ctx context.Context, sha, loader string, force bool) (model.Build, bool, error) {
	var result model.Build
	if m.cfg.Build.Source == "ci" && force {
		return result, false, model.Failure("conflict", "rerun the configured workflow in GitHub to rebuild a CI project")
	}
	if m.cfg.Build.Source != "ci" && (m.cfg.Trust != "trusted" || m.cfg.Build.Source != "host") {
		return result, false, model.Failure("sandbox_unavailable", "host builds require a trusted project")
	}
	selected, ok := m.cfg.Loaders[loader]
	if !ok || !selected.Enabled {
		return result, false, model.Failure("invalid_request", "loader is disabled")
	}
	if !commitPattern.MatchString(sha) {
		return result, false, model.Failure("invalid_request", "commit sha must contain 40 lowercase hexadecimal characters")
	}
	id := loader + "-" + sha[:12]
	// 已有记录可以直接使用时不读取 Git；只有新的构建尝试要求提交仍在仓库的现有历史中。
	var existing model.Build
	err := m.db.Get(ctx, "build", id, &existing)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return result, false, err
	}
	var commit *model.CommitInfo
	if err != nil || existing.SHA == sha && existing.Status != "queued" && existing.Status != "building" && force {
		if err = m.mirror.VerifyCommit(ctx, sha); err != nil {
			return result, false, err
		}
		info, err := m.mirror.CommitInfo(ctx, sha)
		if err != nil {
			return result, false, err
		}
		commit = &info
	}
	created := false
	err = m.db.Update(ctx, func(tx *store.Tx) error {
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
		if commit == nil {
			return model.Failure("conflict", "build record changed while it was requested; retry the request")
		}
		attempt, err := tx.Next(ctx, "build_attempt:"+id)
		if err != nil {
			return err
		}
		result = model.Build{
			Project: m.cfg.ID, Source: model.BuildSource{Kind: m.cfg.Build.Source}, ID: id, SHA: sha, Loader: loader, Status: "queued", Attempt: attempt,
			QueuedAt: time.Now().UTC(), ModIDs: []string{}, Files: []model.BuildFile{},
			Log: m.apiPrefix() + "/builds/" + id + "/log", Commit: commit,
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
			file, err = os.Open(filepath.Join(m.attemptDir(value), name))
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
	if value.Status == "building" {
		job, err := readJob(m.jobPath(value))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil && job.Attempt == value.Attempt {
			return os.Open(filepath.Join(m.workDir(value), "build.log"))
		}
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
	file, err := os.Open(filepath.Join(m.cfg.DataDir, "deps", digest+".jar"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, model.Failure("not_found", "dependency does not exist")
	}
	return file, err
}

func (m *Manager) attemptDir(value model.Build) string {
	return filepath.Join(m.cfg.DataDir, "builds", value.ID, fmt.Sprintf("attempt-%d", value.Attempt))
}

func (m *Manager) apiPrefix() string { return "/v2/projects/" + m.cfg.ID }

func (m *Manager) workDir(value model.Build) string {
	return filepath.Join(m.cfg.DataDir, "work", value.ID, fmt.Sprintf("attempt-%d", value.Attempt))
}

func (m *Manager) jobPath(value model.Build) string {
	return filepath.Join(m.cfg.Server.DataDir, "jobs", m.cfg.ID+"--"+value.ID+".json")
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
		if value.Source.Kind == "ci" {
			continue
		}
		if value.Status != "queued" && value.Status != "building" {
			continue
		}
		if value.Status == "queued" {
			active, err := m.anyUnitActive(ctx)
			if err != nil || active {
				return err
			}
		}
		if err = m.Process(ctx, value.ID); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var stopErr *workerStopError
			if errors.As(err, &stopErr) {
				return err
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
	return "vex-build@" + m.cfg.ID + "--" + value.ID + ".service"
}

func (m *Manager) anyUnitActive(ctx context.Context) (bool, error) {
	args := []string{"list-units", "--all", "--type=service", "--output=json", "vex-build@*.service"}
	output, err := exec.CommandContext(ctx, "systemctl", args...).Output()
	if err != nil {
		return false, fmt.Errorf("inspect global build units: %w", err)
	}
	var units []struct {
		Active string `json:"active"`
	}
	if err = json.Unmarshal(output, &units); err != nil {
		return false, fmt.Errorf("decode global build unit states: %w", err)
	}
	for _, unit := range units {
		if unit.Active == "active" || unit.Active == "activating" || unit.Active == "deactivating" || unit.Active == "reloading" {
			return true, nil
		}
	}
	return false, nil
}

func (m *Manager) unitActive(ctx context.Context, value model.Build) (bool, error) {
	args := []string{"show", "--property=ActiveState", "--property=SubState", m.unitName(value)}
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

func (m *Manager) launch(ctx context.Context, value model.Build, resume bool) error {
	if resume {
		return m.waitUnit(ctx, value)
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
		if err = m.unitOperation(ctx, value, operation); err != nil {
			return err
		}
	}
	command := exec.CommandContext(ctx, "systemctl", "start", m.unitName(value))
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("start build service: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return m.waitUnit(ctx, value)
}

func (m *Manager) unitProperty(ctx context.Context, value model.Build, property string) (string, error) {
	args := []string{"show", "--value", "--property=" + property, m.unitName(value)}
	output, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
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

func (m *Manager) unitOperation(ctx context.Context, value model.Build, operation string) error {
	args := []string{operation, m.unitName(value)}
	if err := exec.CommandContext(ctx, "systemctl", args...).Run(); err != nil {
		return fmt.Errorf("build service %s: %w", operation, err)
	}
	return nil
}

func (m *Manager) waitUnit(ctx context.Context, value model.Build) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		active, err := m.unitActive(ctx, value)
		if err != nil {
			return err
		}
		if !active {
			result, err := m.unitProperty(ctx, value, "Result")
			if err != nil {
				return err
			}
			status, err := m.unitProperty(ctx, value, "ExecMainStatus")
			if err != nil {
				return err
			}
			if result != "success" || status != "0" {
				return fmt.Errorf("Gradle worker failed: result=%s status=%s", result, status)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
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
		entries, err := os.ReadDir(filepath.Join(m.cfg.DataDir, "builds"))
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
				if err = os.RemoveAll(filepath.Join(m.cfg.DataDir, "builds", entry.Name())); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}
		return nil
	})
}
