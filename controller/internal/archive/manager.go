package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/assets"
	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
	"github.com/imdomestic/chorus-testbed/controller/internal/worldarchive"
)

type Manager struct {
	cfg    config.Config
	db     *store.Store
	mu     sync.Mutex
	Assets *assets.Manager
}

func New(cfg config.Config, db *store.Store) (*Manager, error) {
	if cfg.Archive.Enabled {
		if _, _, err := target(cfg.Archive.TankTarget); err != nil {
			return nil, err
		}
		if cfg.Archive.IdentityFile == "" {
			return nil, fmt.Errorf("enabled archive requires an explicit SSH identity")
		}
		if _, err := os.Stat(cfg.Archive.IdentityFile); err != nil {
			return nil, err
		}
	}
	return &Manager{cfg: cfg, db: db}, nil
}

var hostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]*$`)

func target(value string) (string, string, error) {
	host, root, ok := strings.Cut(value, ":")
	if !ok || !hostPattern.MatchString(host) || !strings.HasPrefix(root, "/") || path.Clean(root) != root || strings.ContainsAny(root, "\x00\n\r") {
		return "", "", fmt.Errorf("invalid archive SSH target")
	}
	return host, root, nil
}

func safeRelative(value string) bool {
	return value != "" && value != "." && !filepath.IsAbs(value) && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../") && !strings.ContainsAny(value, "\\\x00\n\r")
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func (m *Manager) sshOptions() []string {
	args := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2", "-i", m.cfg.Archive.IdentityFile}
	if m.cfg.Archive.KnownHostsFile != "" {
		args = append(args, "-o", "UserKnownHostsFile="+m.cfg.Archive.KnownHostsFile)
	}
	return args
}

func (m *Manager) sshArgs(host string) []string {
	return append(m.sshOptions(), host)
}

func (m *Manager) remote(ctx context.Context, host, command string, input []byte) error {
	args := append(m.sshArgs(host), command)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("archive SSH operation failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (m *Manager) checkRemote(ctx context.Context, host, filename, digest string, size int64) error {
	command := "sha256sum --check --status && test \"$(stat -c %s -- " + shellQuote(filename) + ")\" -eq " + strconv.FormatInt(size, 10)
	return m.remote(ctx, host, command, []byte(digest+"  "+filename+"\n"))
}

func (m *Manager) rsync(ctx context.Context, source, destination string) error {
	transport := append([]string{"ssh"}, m.sshOptions()...)
	quoted := make([]string, len(transport))
	for i, arg := range transport {
		quoted[i] = shellQuote(arg)
	}
	args := []string{"-n", "19", "rsync", "--partial", "--inplace", "--append-verify", "--protect-args", "--chmod=F640,D750", "--bwlimit=" + m.cfg.Archive.TankBWLimit, "-e", strings.Join(quoted, " "), "--", source, destination}
	cmd := exec.CommandContext(ctx, "nice", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	cmd.Env = append(os.Environ(), "TMPDIR="+filepath.Join(m.cfg.Server.DataDir, ".work"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("archive transfer failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (m *Manager) Upload(ctx context.Context, task model.Transfer) error {
	if !safeRelative(task.LocalPath) || !safeRelative(task.RemotePath) {
		return fmt.Errorf("archive task paths must be relative")
	}
	host, root, err := target(m.cfg.Archive.TankTarget)
	if err != nil {
		return err
	}
	local := filepath.Join(m.cfg.Server.DataDir, filepath.FromSlash(task.LocalPath))
	digest, size, err := worldarchive.Digest(local)
	if err != nil {
		return err
	}
	if digest != task.SHA256 || size != task.Size {
		return fmt.Errorf("local archive file does not match the published checksum")
	}
	remote := path.Join(root, task.RemotePath)
	if err = m.remote(ctx, host, "mkdir -p -- "+shellQuote(path.Dir(remote)), nil); err != nil {
		return err
	}
	if err = m.rsync(ctx, local, host+":"+remote); err != nil {
		return err
	}
	return m.checkRemote(ctx, host, remote, task.SHA256, task.Size)
}

func (m *Manager) Run(ctx context.Context) error {
	if !m.cfg.Archive.Enabled {
		<-ctx.Done()
		return nil
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if err := m.Step(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (m *Manager) Step(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.cfg.Archive.Enabled {
		return nil
	}
	if err := m.restoreSnapshots(ctx); err != nil {
		return err
	}
	if m.Assets != nil {
		if err := m.Assets.RestorePending(ctx, m.Download); err != nil {
			return err
		}
	}
	tasks, err := store.All[model.Transfer](ctx, m.db, "transfer")
	if err != nil {
		return err
	}
	var failures []error
	for _, task := range tasks {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		if task.Status == "verified" {
			continue
		}
		if task.Status == "failed" && time.Since(task.UpdatedAt) < retryDelay(task.Attempt) {
			continue
		}
		task.Status = "running"
		task.Attempt++
		task.UpdatedAt = time.Now().UTC()
		task.LastError = nil
		if err = m.db.Put(ctx, "transfer", task.ID, task); err != nil {
			return err
		}
		transferCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
		transferErr := m.Upload(transferCtx, task)
		cancel()
		now := time.Now().UTC()
		task.UpdatedAt = now
		if transferErr != nil {
			task.Status = "failed"
			task.LastError = model.Failure("archive_failed", transferErr.Error())
		} else {
			task.Status = "verified"
			task.VerifiedAt = &now
		}
		writeCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err = m.db.Put(writeCtx, "transfer", task.ID, task)
		done()
		if err != nil {
			return err
		}
		if transferErr != nil {
			failures = append(failures, fmt.Errorf("archive task %s: %w", task.ID, transferErr))
		}
	}
	if err = m.markSnapshots(ctx); err != nil {
		return err
	}
	if err = m.evictSnapshots(ctx); err != nil {
		return err
	}
	if m.Assets != nil {
		if err = m.Assets.MarkVerified(ctx); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if err = m.Assets.EvictArchived(ctx); err != nil {
			return errors.Join(append(failures, err)...)
		}
	}
	return errors.Join(failures...)
}

func retryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return time.Minute
	}
	if attempt > 6 {
		return time.Hour
	}
	return time.Minute << (attempt - 1)
}

func (m *Manager) markSnapshots(ctx context.Context) error {
	snapshots, err := store.All[model.Snapshot](ctx, m.db, "snapshot")
	if err != nil {
		return err
	}
	for _, snapshot := range snapshots {
		if snapshot.TankVerifiedAt != nil {
			continue
		}
		verified := true
		for _, part := range []string{"world", "metadata"} {
			var task model.Transfer
			err = m.db.Get(ctx, "transfer", model.TransferID("snapshot", snapshot.ID, "", part), &task)
			if errors.Is(err, store.ErrNotFound) || err == nil && task.Status != "verified" {
				verified = false
				break
			}
			if err != nil {
				return err
			}
		}
		if verified {
			now := time.Now().UTC()
			snapshot.TankVerifiedAt = &now
			if err = m.db.Put(ctx, "snapshot", snapshot.ID, snapshot); err != nil {
				return err
			}
		}
	}
	return nil
}

func QueueSnapshot(ctx context.Context, tx *store.Tx, dataDir string, snapshot model.Snapshot) error {
	relative := path.Join("snapshots", snapshot.RoomID, snapshot.ID+".tar.gz")
	metadataPath := path.Join("snapshots", snapshot.RoomID, snapshot.ID+".json")
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dataDir, filepath.FromSlash(metadataPath)), append(data, '\n'), 0640); err != nil {
		return err
	}
	digest, size, err := worldarchive.Digest(filepath.Join(dataDir, filepath.FromSlash(metadataPath)))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, item := range []struct {
		part, path, digest string
		size               int64
	}{{"world", relative, snapshot.SHA256, snapshot.Size}, {"metadata", metadataPath, digest, size}} {
		id := model.TransferID("snapshot", snapshot.ID, "", item.part)
		task := model.Transfer{ID: id, Kind: "snapshot", ObjectID: snapshot.ID, Part: item.part, LocalPath: item.path, RemotePath: item.path, SHA256: item.digest, Size: item.size, Status: "queued", CreatedAt: now, UpdatedAt: now}
		if err = tx.Put(ctx, "transfer", id, task); err != nil {
			return err
		}
	}
	return nil
}
