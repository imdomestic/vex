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
	"strconv"
	"time"

	"github.com/imdomestic/vex/controller/internal/manifest"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
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
	if value.Source.Kind == "ci" {
		return nil
	}
	err = m.execute(ctx, value)
	if err != nil {
		var stopErr *workerStopError
		if errors.As(err, &stopErr) {
			return err
		}
		writeCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer done()
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
	resume := value.Status == "building"
	unitTouched, checkoutOwned, workerLogCopied := resume, false, false
	worktree := filepath.Join(m.workDir(value), "source")
	var log *os.File
	defer func() {
		defer func() {
			if log != nil {
				if resultErr != nil {
					_, writeErr := fmt.Fprintln(log, resultErr)
					resultErr = errors.Join(resultErr, writeErr)
				}
				resultErr = errors.Join(resultErr, log.Close())
			}
		}()
		if unitTouched {
			stopCtx, stopDone := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
			stopErr := m.stopWorker(stopCtx, value)
			stopDone()
			resultErr = errors.Join(resultErr, stopErr)
			var unsafeStop *workerStopError
			if errors.As(stopErr, &unsafeStop) {
				return
			}
			if log != nil && !workerLogCopied {
				workerLog, err := os.Open(filepath.Join(m.workDir(value), "build.log"))
				if err == nil {
					_, copyErr := io.Copy(log, workerLog)
					resultErr = errors.Join(resultErr, copyErr, workerLog.Close())
				} else if !errors.Is(err, os.ErrNotExist) {
					resultErr = errors.Join(resultErr, err)
				}
			}
		}
		if checkoutOwned {
			cleanupCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer done()
			resultErr = errors.Join(resultErr, m.mirror.RemoveCheckout(cleanupCtx, worktree))
		}
	}()
	if m.cfg.Trust != "trusted" || m.cfg.Build.Source != "host" {
		return model.Failure("sandbox_unavailable", "host builds require a trusted project")
	}
	timeout, err := time.ParseDuration(m.cfg.Build.Timeout)
	if err != nil || timeout <= 0 {
		return fmt.Errorf("build timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if value.Status == "queued" {
		if err := worldarchive.Space(m.cfg.DataDir); err != nil {
			return err
		}
	}
	configured, exists := m.cfg.Loaders[value.Loader]
	if !exists || !configured.Enabled {
		return model.Failure("invalid_request", "loader is disabled")
	}
	doc, err := m.commitManifest(ctx, value.SHA)
	if err != nil {
		return err
	}
	loader, exists := doc.Loaders[value.Loader]
	if !exists || len(loader.Build) == 0 {
		return fmt.Errorf("manifest requires the selected loader and a host build command")
	}
	runtime, exists := m.cfg.Runtimes[fmt.Sprintf("java%d", doc.Minecraft.Java)]
	if !exists || !filepath.IsAbs(runtime.JDKHome) {
		return fmt.Errorf("build jdk_home must be absolute")
	}
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
	log, err = os.OpenFile(filepath.Join(outputDir, "build.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0640)
	if err != nil {
		return err
	}
	if !resume {
		if err = m.mirror.Checkout(ctx, value.SHA, worktree); err != nil {
			return err
		}
	} else {
		job, err := readJob(m.jobPath(value))
		if err != nil {
			return fmt.Errorf("interrupted build has an invalid worker job: %w", err)
		}
		if job.Attempt != value.Attempt || job.Project != m.cfg.ID || job.BuildID != value.ID {
			return fmt.Errorf("interrupted build has no matching worker job")
		}
	}
	checkoutOwned = true
	minecraft, loaderVersion, java := doc.Minecraft.Version, loader.LoaderVersion, doc.Minecraft.Java
	value.Minecraft, value.LoaderVersion, value.Java = &minecraft, &loaderVersion, &java
	if err = m.saveAttempt(ctx, value); err != nil {
		return err
	}
	if !resume {
		if err = m.prepareJob(value, doc, runtime.JDKHome, worktree); err != nil {
			return err
		}
	}
	unitTouched = true
	if err = m.launch(ctx, value, resume); err != nil {
		return err
	}
	if err = m.stopWorker(ctx, value); err != nil {
		return err
	}
	unitTouched = false
	workerLog, err := os.Open(filepath.Join(m.workDir(value), "build.log"))
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(log, workerLog)
	if err = errors.Join(copyErr, workerLog.Close()); err != nil {
		return err
	}
	workerLogCopied = true
	stage, err := os.MkdirTemp(filepath.Join(m.cfg.DataDir, ".work"), "host-import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	extracted := filepath.Join(stage, "contents")
	bundle, err := UnpackBundle(filepath.Join(m.workDir(value), ".work", "bundle.zip"), extracted)
	if err != nil {
		return err
	}
	selected := bundle.LoaderBuilds()
	if len(selected) != 1 || selected[0].Loader != value.Loader {
		return fmt.Errorf("worker bundle differs from the commit manifest")
	}
	if err = bundle.validateManifest(doc, false); err != nil {
		return err
	}
	finished := time.Now().UTC()
	duration := finished.Sub(*value.StartedAt).Seconds()
	value.Status, value.FinishedAt, value.BuiltAt, value.DurationSec = "ready", &finished, &finished, &duration
	if err = m.publishFiles(&value, doc, selected[0], extracted); err != nil {
		return err
	}
	if err = log.Sync(); err != nil {
		return err
	}
	if err = m.mirror.RemoveCheckout(ctx, worktree); err != nil {
		return err
	}
	checkoutOwned = false
	return m.saveAttempt(ctx, value)
}

type workerStopError struct{ cause error }

func (e *workerStopError) Error() string { return e.cause.Error() }
func (e *workerStopError) Unwrap() error { return e.cause }

func (m *Manager) stopWorker(ctx context.Context, value model.Build) error {
	stopErr := m.unitOperation(ctx, value, "stop")
	state, inspectErr := m.unitProperty(ctx, value, "ActiveState")
	if inspectErr != nil {
		return &workerStopError{errors.Join(stopErr, inspectErr)}
	}
	if state != "inactive" && state != "failed" {
		return &workerStopError{errors.Join(stopErr, fmt.Errorf("build service has not stopped: %s", state))}
	}
	for _, property := range []string{"MainPID", "ControlPID"} {
		pid, err := m.unitProperty(ctx, value, property)
		if err != nil || pid != "0" {
			return &workerStopError{errors.Join(stopErr, err, fmt.Errorf("build service %s is not confirmed zero", property))}
		}
	}
	return stopErr
}

func (m *Manager) workerJob(value model.Build, doc manifest.Document, jdkHome, worktree string) (Job, error) {
	job := Job{
		Version: 2, Project: m.cfg.ID, BuildID: value.ID, Attempt: value.Attempt,
		Worktree: worktree, TempDir: filepath.Join(m.workDir(value), ".work"),
		GradleHome: filepath.Join(m.cfg.DataDir, "cache", "gradle"), JDKHome: jdkHome,
		Manifest: doc, Loader: value.Loader, Timeout: m.cfg.Build.Timeout,
		LockFile: m.cfg.Build.LockFile,
	}
	return job, job.validate(m.jobPath(value))
}

func (m *Manager) prepareJob(value model.Build, doc manifest.Document, jdkHome, worktree string) error {
	job, err := m.workerJob(value, doc, jdkHome, worktree)
	if err != nil {
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
	for _, directory := range []string{m.cfg.Server.DataDir, filepath.Join(m.cfg.Server.DataDir, "projects"), m.cfg.DataDir, filepath.Join(m.cfg.DataDir, "work"), filepath.Dir(m.workDir(value)), filepath.Join(m.cfg.DataDir, "cache")} {
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
