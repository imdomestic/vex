package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"golang.org/x/sys/unix"
)

type Job struct {
	Version    int      `json:"version"`
	Project    string   `json:"project"`
	BuildID    string   `json:"buildId"`
	Attempt    int      `json:"attempt"`
	Worktree   string   `json:"worktree"`
	TempDir    string   `json:"tempDir"`
	GradleHome string   `json:"gradleHome"`
	JDKHome    string   `json:"jdkHome"`
	Task       string   `json:"task"`
	GradleArgs []string `json:"gradleArgs"`
	Timeout    string   `json:"timeout"`
	LockFile   string   `json:"lockFile"`
}

func readJob(path string) (Job, error) {
	var job Job
	info, err := os.Lstat(path)
	if err != nil {
		return job, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return job, fmt.Errorf("worker job must be a regular JSON file no larger than 64 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return job, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&job); err != nil {
		return job, fmt.Errorf("invalid worker job JSON")
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return job, fmt.Errorf("worker job contains trailing data")
	}
	return job, job.validate(path)
}

func (j Job) validate(path string) error {
	if j.Version != 1 || !config.ValidID(j.Project) || !buildIDPattern.MatchString(j.BuildID) || j.Attempt < 1 {
		return fmt.Errorf("invalid worker job identity")
	}
	if !filepath.IsAbs(path) || filepath.Base(path) != j.Project+"--"+j.BuildID+".json" || filepath.Base(filepath.Dir(path)) != "jobs" {
		return fmt.Errorf("worker job path does not match its identity")
	}
	root := filepath.Dir(filepath.Dir(path))
	project := filepath.Join(root, "projects", j.Project)
	work := filepath.Join(project, "work", j.BuildID)
	if j.Worktree != filepath.Join(work, "source") || j.TempDir != filepath.Join(work, ".work") || j.GradleHome != filepath.Join(project, "cache", "gradle") {
		return fmt.Errorf("worker paths must belong to the selected project")
	}
	if !filepath.IsAbs(j.JDKHome) || !filepath.IsAbs(j.LockFile) || j.Task == "" || strings.HasPrefix(j.Task, "-") || strings.ContainsAny(j.Task, "\x00\r\n") {
		return fmt.Errorf("invalid worker Java, lock or Gradle task configuration")
	}
	duration, err := time.ParseDuration(j.Timeout)
	if err != nil || duration <= 0 {
		return fmt.Errorf("worker timeout must be positive")
	}
	return nil
}

func Worker(ctx context.Context, jobPath string) error {
	job, err := readJob(jobPath)
	if err != nil {
		return err
	}
	lock, err := os.Open(job.LockFile)
	if err != nil {
		return err
	}
	defer lock.Close()
	workerCtx, cancel := context.WithTimeout(ctx, config.Duration(job.Timeout))
	defer cancel()
	if err = acquireLock(workerCtx, lock); err != nil {
		return err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	for _, directory := range []string{job.TempDir, job.GradleHome} {
		if err = os.MkdirAll(directory, 0770); err != nil {
			return err
		}
	}
	log, err := os.OpenFile(filepath.Join(filepath.Dir(job.Worktree), "build.log"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0660)
	if err != nil {
		return err
	}
	defer log.Close()
	args := []string{"./gradlew", job.Task}
	args = append(args, job.GradleArgs...)
	args = append(args, "--console=plain", "--no-daemon", "--max-workers=2",
		"-Dorg.gradle.jvmargs=-Xmx1536m -XX:ActiveProcessorCount=2 -Djava.io.tmpdir="+job.TempDir,
		"-Porg.gradle.java.installations.paths="+job.JDKHome,
		"-Porg.gradle.java.installations.auto-download=false")
	command := exec.CommandContext(workerCtx, "bash", args...)
	command.Dir = job.Worktree
	command.Env = []string{
		"HOME=" + job.GradleHome,
		"JAVA_HOME=" + job.JDKHome,
		"PATH=" + filepath.Join(job.JDKHome, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
		"TMPDIR=" + job.TempDir,
		"GRADLE_USER_HOME=" + job.GradleHome,
		"JAVA_TOOL_OPTIONS=-XX:ActiveProcessorCount=2 -Djava.io.tmpdir=" + job.TempDir,
	}
	for _, name := range []string{"SSL_CERT_FILE", "SSL_CERT_DIR", "NIX_SSL_CERT_FILE", "LANG", "LC_ALL", "TZ"} {
		if value, ok := os.LookupEnv(name); ok {
			command.Env = append(command.Env, name+"="+value)
		}
	}
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
	return log.Sync()
}
