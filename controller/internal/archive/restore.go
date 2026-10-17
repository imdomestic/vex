package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"

	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
)

func (m *Manager) RequestSnapshotRestore(ctx context.Context, id string) (model.Snapshot, bool, error) {
	var snapshot model.Snapshot
	accepted := false
	err := m.db.Update(ctx, func(tx *store.Tx) error {
		if err := tx.Get(ctx, "snapshot", id, &snapshot); err != nil {
			return err
		}
		if snapshot.LocalAvailable {
			return nil
		}
		if snapshot.Restore != nil && (snapshot.Restore.Status == "queued" || snapshot.Restore.Status == "running") {
			accepted = true
			return nil
		}
		if snapshot.TankVerifiedAt == nil {
			return model.Failure("archive_failed", "快照没有已校验的归档来源")
		}
		attempt := 1
		if snapshot.Restore != nil {
			attempt = snapshot.Restore.Attempt + 1
		}
		snapshot.Restore = &model.Restore{Attempt: attempt, Status: "queued", RequestedAt: time.Now().UTC()}
		accepted = true
		return tx.Put(ctx, "snapshot", id, snapshot)
	})
	return snapshot, accepted, err
}

func (m *Manager) OpenSnapshot(ctx context.Context, id string) (*os.File, error) {
	var file *os.File
	err := m.db.Update(ctx, func(tx *store.Tx) error {
		var snapshot model.Snapshot
		if err := tx.Get(ctx, "snapshot", id, &snapshot); err != nil {
			return err
		}
		if !snapshot.LocalAvailable {
			return &model.Error{Code: "archive_failed", Message: "快照尚未恢复到本地", Details: &model.ErrorDetails{SnapshotID: id}}
		}
		var err error
		if !safeRelative(snapshot.Path) {
			return fmt.Errorf("snapshot relative path is invalid")
		}
		file, err = os.Open(filepath.Join(m.cfg.DataDir, filepath.FromSlash(snapshot.Path)))
		return err
	})
	if err != nil && file != nil {
		file.Close()
	}
	return file, err
}

func (m *Manager) restoreSnapshots(ctx context.Context) error {
	snapshots, err := store.All[model.Snapshot](ctx, m.db, "snapshot")
	if err != nil {
		return err
	}
	for _, snapshot := range snapshots {
		if snapshot.Restore == nil || (snapshot.Restore.Status != "queued" && snapshot.Restore.Status != "running") {
			continue
		}
		now := time.Now().UTC()
		snapshot.Restore.Status = "running"
		snapshot.Restore.StartedAt = &now
		if err = m.db.Put(ctx, "snapshot", snapshot.ID, snapshot); err != nil {
			return err
		}
		restoreErr := m.restoreSnapshotFile(ctx, snapshot)
		finished := time.Now().UTC()
		snapshot.Restore.FinishedAt = &finished
		if restoreErr != nil {
			snapshot.Restore.Status = "failed"
			snapshot.Restore.Error = model.Failure("archive_failed", restoreErr.Error())
			snapshot.LocalAvailable = false
		} else {
			snapshot.Restore.Status = "succeeded"
			snapshot.Restore.Error = nil
			snapshot.LocalAvailable = true
		}
		writeCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err = m.db.Update(writeCtx, func(tx *store.Tx) error {
			var current model.Snapshot
			if err := tx.Get(writeCtx, "snapshot", snapshot.ID, &current); err != nil {
				return err
			}
			if current.Restore == nil || current.Restore.Attempt != snapshot.Restore.Attempt {
				return model.Failure("conflict", "快照恢复周期已经改变")
			}
			return tx.Put(writeCtx, "snapshot", snapshot.ID, snapshot)
		})
		done()
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) Download(ctx context.Context, remotePath, localPath, digest string, size int64) error {
	if !safeRelative(remotePath) || !safeRelative(localPath) {
		return fmt.Errorf("restore paths must be relative")
	}
	if err := m.Storage.Verify(ctx, remotePath, digest, size); err != nil {
		return err
	}
	if err := worldarchive.Space(m.cfg.DataDir); err != nil {
		return err
	}
	local := filepath.Join(m.cfg.DataDir, filepath.FromSlash(localPath))
	if err := os.MkdirAll(filepath.Dir(local), 0700); err != nil {
		return err
	}
	if err := m.Storage.Get(ctx, remotePath, local); err != nil {
		return err
	}
	actual, actualSize, err := worldarchive.Digest(local)
	if err != nil {
		return err
	}
	if actual != digest || actualSize != size {
		return fmt.Errorf("restored file checksum mismatch")
	}
	return nil
}

func (m *Manager) restoreSnapshotFile(ctx context.Context, snapshot model.Snapshot) error {
	relative := snapshot.Path
	if !safeRelative(relative) {
		return fmt.Errorf("snapshot relative path is invalid")
	}
	partial := path.Join(".work", "restore", snapshot.ID+".tar.gz.partial")
	if err := m.Download(ctx, path.Join("projects", snapshot.Project, relative), partial, snapshot.SHA256, snapshot.Size); err != nil {
		return err
	}
	source := filepath.Join(m.cfg.DataDir, filepath.FromSlash(partial))
	if err := worldarchive.Validate(source, snapshot.Minecraft, m.cfg.Worlds.ExtraRootEntries...); err != nil {
		return err
	}
	destination := filepath.Join(m.cfg.DataDir, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	return os.Rename(source, destination)
}

func RetainedSnapshots(snapshots []model.Snapshot, rooms []model.Room, keep int, days int, now time.Time) map[string]bool {
	retained := map[string]bool{}
	counts := map[string]int{}
	ordered := append([]model.Snapshot{}, snapshots...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].CapturedAt.After(ordered[j].CapturedAt) })
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	for _, snapshot := range ordered {
		counts[snapshot.RoomID]++
		if counts[snapshot.RoomID] <= keep || !snapshot.CapturedAt.Before(cutoff) || snapshot.TankVerifiedAt == nil || snapshot.Restore != nil && (snapshot.Restore.Status == "queued" || snapshot.Restore.Status == "running") {
			retained[snapshot.ID] = true
		}
	}
	for _, room := range rooms {
		if room.State != "closed" && room.SeedResolved != nil && room.SeedResolved.Kind == "snapshot" {
			retained[room.SeedResolved.ID] = true
		}
	}
	return retained
}

func (m *Manager) evictSnapshots(ctx context.Context) error {
	return m.db.Update(ctx, func(tx *store.Tx) error {
		snapshots, err := store.All[model.Snapshot](ctx, tx, "snapshot")
		if err != nil {
			return err
		}
		rooms, err := store.All[model.Room](ctx, tx, "room")
		if err != nil {
			return err
		}
		retained := RetainedSnapshots(snapshots, rooms, m.cfg.Archive.KeepPerRoom, m.cfg.Archive.KeepDays, time.Now().UTC())
		for _, snapshot := range snapshots {
			if !snapshot.LocalAvailable || retained[snapshot.ID] {
				continue
			}
			if !safeRelative(snapshot.Path) {
				return fmt.Errorf("snapshot relative path is invalid")
			}
			filename := filepath.Join(m.cfg.DataDir, filepath.FromSlash(snapshot.Path))
			if err = os.Remove(filename); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			snapshot.LocalAvailable = false
			if err = tx.Put(ctx, "snapshot", snapshot.ID, snapshot); err != nil {
				return err
			}
		}
		return nil
	})
}
