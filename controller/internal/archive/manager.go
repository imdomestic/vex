package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
	"github.com/imdomestic/vex/controller/internal/worlds"
)

type Manager struct {
	cfg     config.ProjectConfig
	db      *store.Store
	mu      sync.Mutex
	Worlds  *worlds.Manager
	Storage Store
}

func New(cfg config.ProjectConfig, db *store.Store) (*Manager, error) {
	var storage Store
	switch cfg.Archive.Backend {
	case "local":
		if err := os.MkdirAll(cfg.Archive.Local.Directory, 0750); err != nil {
			return nil, err
		}
		storage = LocalStore{Directory: cfg.Archive.Local.Directory}
	case "rsync-ssh":
		if _, _, err := target(cfg.Archive.RsyncSSH.Target); err != nil {
			return nil, err
		}
		for _, name := range []string{cfg.Archive.RsyncSSH.IdentityFile, cfg.Archive.RsyncSSH.KnownHostsFile} {
			if !filepath.IsAbs(name) {
				return nil, fmt.Errorf("archive credential paths must be absolute")
			}
			if _, err := os.Stat(name); err != nil {
				return nil, err
			}
		}
		storage = SSHStore{Config: cfg.Archive.RsyncSSH, WorkDir: filepath.Join(cfg.DataDir, ".work")}
	default:
		return nil, fmt.Errorf("unsupported archive backend")
	}
	return &Manager{cfg: cfg, db: db, Storage: storage}, nil
}

func (m *Manager) Upload(ctx context.Context, task model.Transfer) error {
	if !safeRelative(task.LocalPath) || !safeRelative(task.RemotePath) {
		return fmt.Errorf("archive task paths must be relative")
	}
	local := filepath.Join(m.cfg.DataDir, filepath.FromSlash(task.LocalPath))
	digest, size, err := worldarchive.Digest(local)
	if err != nil {
		return err
	}
	if digest != task.SHA256 || size != task.Size {
		return fmt.Errorf("local archive file does not match the published checksum")
	}
	if err = m.Storage.Put(ctx, task.RemotePath, local); err != nil {
		return err
	}
	return m.Storage.Verify(ctx, task.RemotePath, task.SHA256, task.Size)
}

func (m *Manager) Run(ctx context.Context) error {
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
	if err := m.restoreSnapshots(ctx); err != nil {
		return err
	}
	if m.Worlds != nil {
		if err := m.Worlds.RestorePending(ctx, m.Download); err != nil {
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
	if m.Worlds != nil {
		if err = m.Worlds.MarkVerified(ctx); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if err = m.Worlds.EvictArchived(ctx); err != nil {
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
	relative := snapshot.Path
	if !safeRelative(relative) || snapshot.Project == "" {
		return fmt.Errorf("snapshot requires project and recorded relative path")
	}
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
		task := model.Transfer{Project: snapshot.Project, ID: id, Kind: "snapshot", ObjectID: snapshot.ID, Part: item.part, LocalPath: item.path, RemotePath: path.Join("projects", snapshot.Project, item.path), SHA256: item.digest, Size: item.size, Status: "queued", CreatedAt: now, UpdatedAt: now}
		if err = tx.Put(ctx, "transfer", id, task); err != nil {
			return err
		}
	}
	return nil
}
