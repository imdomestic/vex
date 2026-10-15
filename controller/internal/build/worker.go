package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/gitmirror"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
	"github.com/imdomestic/chorus-testbed/controller/internal/worldarchive"
	"github.com/magiconair/properties"
	"golang.org/x/sys/unix"
)

func Worker(ctx context.Context, cfg config.Config, db *store.Store, mirror *gitmirror.Mirror, id string) error {
	m, err := New(cfg, db, mirror)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(cfg.Build.LockFile) {
		return fmt.Errorf("build lock_file must be absolute")
	}
	lock, err := os.Open(cfg.Build.LockFile)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = acquireLock(ctx, lock); err != nil {
		return err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	value, err := m.Get(ctx, id)
	if err != nil {
		return err
	}
	if value.Status != "queued" {
		return model.Failure("conflict", "build is not queued")
	}
	timeout, err := time.ParseDuration(cfg.Build.Timeout)
	if err != nil || timeout <= 0 {
		return fmt.Errorf("build timeout must be positive")
	}
	buildCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err = m.execute(buildCtx, value)
	if err != nil {
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
	if err := worldarchive.Space(m.cfg.Server.DataDir); err != nil {
		return err
	}
	loader, exists := m.cfg.Loaders[value.Loader]
	if !exists || !loader.Enabled {
		return model.Failure("invalid_request", "loader is disabled")
	}
	if !filepath.IsAbs(m.cfg.Build.JDKHome) {
		return fmt.Errorf("build jdk_home must be absolute")
	}
	now := time.Now().UTC()
	value.Status, value.StartedAt = "building", &now
	if err := m.db.Update(ctx, func(tx *store.Tx) error {
		var current model.Build
		if err := tx.Get(ctx, "build", value.ID, &current); err != nil {
			return err
		}
		if current.Attempt != value.Attempt || current.Status != "queued" {
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
	worktree := filepath.Join(m.cfg.Server.DataDir, "worktrees", fmt.Sprintf("%s-%d", value.ID, value.Attempt))
	if err = m.mirror.Checkout(ctx, value.SHA, worktree); err != nil {
		return err
	}
	defer func() {
		cleanupCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer done()
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
	temp, err := os.MkdirTemp(filepath.Join(m.cfg.Server.DataDir, ".work"), value.ID+"-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(temp); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	args := []string{"./gradlew", loader.GradleTask}
	args = append(args, m.cfg.Build.GradleArgs...)
	args = append(args, "--console=plain", "--no-daemon", "--max-workers=2",
		"-Dorg.gradle.jvmargs=-Xmx1536m -XX:ActiveProcessorCount=2 -Djava.io.tmpdir="+temp,
		"-Porg.gradle.java.installations.paths="+m.cfg.Build.JDKHome,
		"-Porg.gradle.java.installations.auto-download=false")
	command := exec.CommandContext(ctx, "bash", args...)
	command.Dir = worktree
	command.Env = append(os.Environ(),
		"JAVA_HOME="+m.cfg.Build.JDKHome,
		"PATH="+filepath.Join(m.cfg.Build.JDKHome, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TMPDIR="+temp, "GRADLE_USER_HOME="+filepath.Join(m.cfg.Server.DataDir, "gradle-home"),
		"JAVA_TOOL_OPTIONS=-XX:ActiveProcessorCount=2 -Djava.io.tmpdir="+temp)
	command.Stdout, command.Stderr = log, log
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	if err = command.Run(); err != nil {
		return fmt.Errorf("Gradle failed: %w", err)
	}
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
	digest, size, err := hashFile(filepath.Join(outputDir, artifactName))
	if err != nil {
		return err
	}
	value.Files = []model.BuildFile{{Name: artifactName, SHA256: digest, Size: size, Side: "both", URL: "/v1/builds/" + value.ID + "/files/" + artifactName}}
	ids, err := JarModIDs(artifact)
	if err != nil {
		return err
	}
	uniqueIDs := map[string]bool{}
	for _, id := range ids {
		uniqueIDs[id] = true
	}
	names := map[string]bool{artifactName: true}
	for _, dep := range loader.Deps {
		entry, path, err := m.dependency(ctx, props, dep)
		if err != nil {
			return err
		}
		if names[entry.Name] {
			return fmt.Errorf("duplicate manifest filename %s", entry.Name)
		}
		names[entry.Name] = true
		value.Files = append(value.Files, entry)
		ids, err := JarModIDs(path)
		if err != nil {
			return err
		}
		for _, id := range ids {
			uniqueIDs[id] = true
		}
	}
	if value.Loader == "fabric" {
		hop, source, err := m.hopArtifact(*value.Minecraft)
		if err != nil {
			return err
		}
		if hop != nil {
			if names[hop.Name] {
				return fmt.Errorf("duplicate manifest filename %s", hop.Name)
			}
			if err = copyFile(source, filepath.Join(outputDir, hop.Name)); err != nil {
				return err
			}
			value.Files = append(value.Files, *hop)
			ids, err := JarModIDs(source)
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
