package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
	"github.com/magiconair/properties"
	"golang.org/x/sys/unix"
)

var buildSlot = make(chan struct{}, 1)

func (m *Manager) Process(ctx context.Context, id string) error {
	select {
	case buildSlot <- struct{}{}:
		defer func() { <-buildSlot }()
	case <-ctx.Done():
		return ctx.Err()
	}
	value, err := m.Get(ctx, id)
	if err != nil {
		return err
	}
	if value.Status != "queued" && value.Status != "building" {
		return nil
	}
	if m.cfg.Trust != "trusted" || m.cfg.Build.Source != "host" {
		return m.fail(ctx, value, model.Failure("sandbox_unavailable", "host builds require a trusted project"))
	}
	timeout, err := time.ParseDuration(m.cfg.Build.Timeout)
	if err != nil || timeout <= 0 {
		return fmt.Errorf("build timeout must be positive")
	}
	buildCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err = m.execute(buildCtx, value)
	if err != nil {
		writeCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer done()
		if value.Status == "building" {
			err = errors.Join(err, m.unitOperation(writeCtx, value, "stop"))
		}
		if writeErr := m.fail(writeCtx, value, err); writeErr != nil {
			return errors.Join(err, writeErr)
		}
	}
	return err
}

func acquireLock(ctx context.Context, file *os.File) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *Manager) execute(ctx context.Context, value model.Build) (resultErr error) {
	if value.Status == "queued" {
		if err := worldarchive.Space(m.cfg.DataDir); err != nil {
			return err
		}
	}
	loader, exists := m.cfg.Loaders[value.Loader]
	if !exists || !loader.Enabled {
		return model.Failure("invalid_request", "loader is disabled")
	}
	if !filepath.IsAbs(m.cfg.Build.JDKHome) {
		return fmt.Errorf("build jdk_home must be absolute")
	}
	resume := value.Status == "building"
	if !resume {
		now := time.Now().UTC()
		value.Status, value.StartedAt = "building", &now
	}
	if err := m.db.Update(ctx, func(tx *store.Tx) error {
		var current model.Build
		if err := tx.Get(ctx, "build", value.ID, &current); err != nil {
			return err
		}
		if current.Attempt != value.Attempt || (current.Status != "queued" && current.Status != "building") {
			return model.Failure("conflict", "build attempt has changed")
		}
		return tx.Put(ctx, "build", value.ID, value)
	}); err != nil {
		return err
	}
	outputDir := m.attemptDir(value)
	if err := os.MkdirAll(outputDir, 0750); err != nil {
		return err
	}
	previous, err := os.ReadDir(filepath.Dir(outputDir))
	if err != nil {
		return err
	}
	for _, entry := range previous {
		if !entry.IsDir() || entry.Name() == filepath.Base(outputDir) {
			continue
		}
		var attempt int
		if _, err = fmt.Sscanf(entry.Name(), "attempt-%d", &attempt); err != nil || entry.Name() != fmt.Sprintf("attempt-%d", attempt) || attempt >= value.Attempt {
			continue
		}
		if err = os.RemoveAll(filepath.Join(filepath.Dir(outputDir), entry.Name())); err != nil {
			return err
		}
	}
	log, err := os.OpenFile(filepath.Join(outputDir, "build.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0640)
	if err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			_, _ = fmt.Fprintln(log, resultErr)
		}
		if err := log.Close(); resultErr == nil {
			resultErr = err
		}
	}()
	worktree := filepath.Join(m.workDir(value), "source")
	if !resume {
		if err = m.mirror.Checkout(ctx, value.SHA, worktree); err != nil {
			return err
		}
	}
	unitTouched := resume
	workerLogCopied := false
	defer func() {
		cleanupCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer done()
		if unitTouched {
			if err := m.unitOperation(cleanupCtx, value, "stop"); err != nil {
				resultErr = errors.Join(resultErr, err)
				active, inspectErr := m.unitActive(cleanupCtx, value)
				if inspectErr != nil || active {
					resultErr = errors.Join(resultErr, inspectErr)
					return
				}
			}
			if !workerLogCopied {
				workerLog, err := os.Open(filepath.Join(m.workDir(value), "build.log"))
				if err == nil {
					_, copyErr := io.Copy(log, workerLog)
					resultErr = errors.Join(resultErr, copyErr, workerLog.Close())
				} else if !errors.Is(err, os.ErrNotExist) {
					resultErr = errors.Join(resultErr, err)
				}
			}
		}
		if err := m.mirror.RemoveCheckout(cleanupCtx, worktree); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	props, err := properties.LoadFile(filepath.Join(worktree, "gradle.properties"), properties.UTF8)
	if err != nil {
		return err
	}
	minecraft, ok := props.Get("minecraft_version")
	if !ok || minecraft == "" {
		return fmt.Errorf("gradle.properties requires minecraft_version")
	}
	loaderVersion, ok := props.Get(loader.LoaderVersionProperty)
	if !ok || loaderVersion == "" {
		return fmt.Errorf("gradle.properties requires %s", loader.LoaderVersionProperty)
	}
	javaValue, ok := props.Get("java_version")
	if !ok {
		return fmt.Errorf("gradle.properties requires java_version")
	}
	java, err := strconv.Atoi(javaValue)
	if err != nil {
		return err
	}
	if minecraft != m.cfg.Minecraft.Version || java != m.cfg.Minecraft.Java {
		return fmt.Errorf("commit targets Minecraft %s / Java %d, expected %s / %d", minecraft, java, m.cfg.Minecraft.Version, m.cfg.Minecraft.Java)
	}
	value.Minecraft, value.LoaderVersion, value.Java = &minecraft, &loaderVersion, &java
	if err = m.saveAttempt(ctx, value); err != nil {
		return err
	}
	if !resume {
		if err = m.prepareJob(value, loader, worktree); err != nil {
			return err
		}
	} else {
		job, err := readJob(m.jobPath(value))
		if err != nil || job.Attempt != value.Attempt || job.Project != m.cfg.ID || job.BuildID != value.ID {
			return fmt.Errorf("interrupted build has no matching worker job")
		}
	}
	unitTouched = true
	if err = m.launch(ctx, value, resume); err != nil {
		return err
	}
	if err = m.unitOperation(ctx, value, "stop"); err != nil {
		return err
	}
	workerLog, err := os.Open(filepath.Join(m.workDir(value), "build.log"))
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(log, workerLog)
	if err = errors.Join(copyErr, workerLog.Close()); err != nil {
		return err
	}
	workerLogCopied = true
	artifact, err := artifactPath(worktree, loader)
	if err != nil {
		return err
	}
	artifactName := filepath.Base(artifact)
	if artifactName == "build.log" || artifactName == "manifest.json" {
		return fmt.Errorf("invalid artifact filename")
	}
	if err = copyFile(artifact, filepath.Join(outputDir, artifactName)); err != nil {
		return err
	}
	entry, err := m.buildFile(filepath.Join(outputDir, artifactName), value, "both")
	if err != nil {
		return err
	}
	value.Files = []model.BuildFile{entry}
	ids, err := JarModIDs(artifact, value.Loader)
	if err != nil {
		return err
	}
	uniqueIDs := map[string]bool{}
	for _, id := range ids {
		uniqueIDs[id] = true
	}
	names := map[string]bool{artifactName: true}
	for _, dep := range loader.Deps {
		dependency, path, err := m.dependency(ctx, props, dep, value.Loader)
		if err != nil {
			return err
		}
		if names[dependency.Name] {
			return fmt.Errorf("duplicate manifest filename %s", dependency.Name)
		}
		names[dependency.Name] = true
		if err = copyFile(path, filepath.Join(outputDir, dependency.Name)); err != nil {
			return err
		}
		entry, err := m.buildFile(filepath.Join(outputDir, dependency.Name), value, dependency.Side)
		if err != nil {
			return err
		}
		value.Files = append(value.Files, entry)
		ids, err := JarModIDs(path, value.Loader)
		if err != nil {
			return err
		}
		for _, id := range ids {
			uniqueIDs[id] = true
		}
	}
	if loader.Agent != "" {
		agent, source, err := m.agentArtifact(value)
		if err != nil {
			return err
		}
		if agent != nil {
			if names[agent.Name] {
				return fmt.Errorf("duplicate manifest filename %s", agent.Name)
			}
			if err = copyFile(source, filepath.Join(outputDir, agent.Name)); err != nil {
				return err
			}
			copied, err := m.buildFile(filepath.Join(outputDir, agent.Name), value, "server")
			if err != nil {
				return err
			}
			if copied.SHA256 != agent.SHA256 || copied.Size != agent.Size {
				return fmt.Errorf("agent artifact changed during publication")
			}
			value.Files = append(value.Files, copied)
			ids, err := JarModIDs(source, value.Loader)
			if err != nil {
				return err
			}
			for _, id := range ids {
				uniqueIDs[id] = true
			}
		}
	}
	value.ModIDs = make([]string, 0, len(uniqueIDs))
	for id := range uniqueIDs {
		value.ModIDs = append(value.ModIDs, id)
	}
	sort.Strings(value.ModIDs)
	finished := time.Now().UTC()
	duration := finished.Sub(*value.StartedAt).Seconds()
	value.Status, value.FinishedAt, value.BuiltAt, value.DurationSec = "ready", &finished, &finished, &duration
	manifest, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(outputDir, "manifest.json"), append(manifest, '\n'), 0640); err != nil {
		return err
	}
	if err = log.Sync(); err != nil {
		return err
	}
	return m.saveAttempt(ctx, value)
}

func (m *Manager) prepareJob(value model.Build, loader config.Loader, worktree string) error {
	job := Job{
		Version: 1, Project: m.cfg.ID, BuildID: value.ID, Attempt: value.Attempt,
		Worktree: worktree, TempDir: filepath.Join(m.workDir(value), ".work"),
		GradleHome: filepath.Join(m.cfg.DataDir, "cache", "gradle"), JDKHome: m.cfg.Build.JDKHome,
		Task: loader.GradleTask, GradleArgs: m.cfg.Build.GradleArgs, Timeout: m.cfg.Build.Timeout,
		LockFile: m.cfg.Build.LockFile,
	}
	if err := job.validate(m.jobPath(value)); err != nil {
		return err
	}
	for _, directory := range []string{job.TempDir, job.GradleHome, filepath.Dir(m.jobPath(value))} {
		if err := os.MkdirAll(directory, 0750); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(m.workDir(value), "build.log"), nil, 0660); err != nil {
		return err
	}
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	if err = os.WriteFile(m.jobPath(value), data, 0640); err != nil {
		return err
	}
	group, err := user.LookupGroup("vex-build")
	if err != nil {
		return fmt.Errorf("vex-build group is required: %w", err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return err
	}
	return m.prepareWorkerPermissions(value, job, gid)
}

func ensureWorkerTraversal(directory string, gid int) error {
	var info unix.Stat_t
	if err := unix.Lstat(directory, &info); err != nil {
		return err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("worker parent must be a directory: %s", directory)
	}
	if info.Mode&0001 != 0 || (int(info.Gid) == gid && info.Mode&0010 != 0) {
		return nil
	}
	if info.Uid == 0 || int(info.Uid) != os.Geteuid() {
		return fmt.Errorf("directory must allow vex-build traversal: %s", directory)
	}
	return os.Chmod(directory, os.FileMode(info.Mode&0777)|0011)
}

func (m *Manager) prepareWorkerPermissions(value model.Build, job Job, gid int) error {
	for _, directory := range []string{m.cfg.Server.DataDir, filepath.Join(m.cfg.Server.DataDir, "projects"), m.cfg.DataDir, filepath.Join(m.cfg.DataDir, "work"), filepath.Join(m.cfg.DataDir, "cache")} {
		if err := ensureWorkerTraversal(directory, gid); err != nil {
			return err
		}
	}
	for _, path := range []string{filepath.Dir(m.jobPath(value)), m.jobPath(value), job.GradleHome} {
		if err := os.Chown(path, -1, gid); err != nil {
			return err
		}
	}
	if err := os.Chmod(filepath.Dir(m.jobPath(value)), 0750); err != nil {
		return err
	}
	if err := os.Chmod(m.jobPath(value), 0640); err != nil {
		return err
	}
	if err := os.Chmod(job.GradleHome, 0770); err != nil {
		return err
	}
	return filepath.WalkDir(m.workDir(value), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := os.Lchown(path, -1, gid); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		mode := os.FileMode(0660)
		if entry.IsDir() {
			mode = 0770
		} else {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode()&0111 != 0 {
				mode |= 0110
			}
		}
		return os.Chmod(path, mode)
	})
}

func (m *Manager) saveAttempt(ctx context.Context, value model.Build) error {
	return m.db.Update(ctx, func(tx *store.Tx) error {
		var current model.Build
		if err := tx.Get(ctx, "build", value.ID, &current); err != nil {
			return err
		}
		if current.Attempt != value.Attempt || current.Status != "building" {
			return model.Failure("conflict", "build attempt changed before publication")
		}
		return tx.Put(ctx, "build", value.ID, value)
	})
}
