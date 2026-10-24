package worlds

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
)

func (m *Manager) requiredTransfers(ctx context.Context, tx store.Access, revision model.WorldRevision) ([]model.Transfer, error) {
	keys := []string{model.TransferID("world", revision.ID, revision.Rev, "world"), model.TransferID("world", revision.ID, revision.Rev, "metadata")}
	if revision.ResourcePack != nil {
		keys = append(keys, model.TransferID("pack", revision.ResourcePack.ID, revision.ResourcePack.Rev, "zip"), model.TransferID("pack", revision.ResourcePack.ID, revision.ResourcePack.Rev, "metadata"))
	}
	tasks := make([]model.Transfer, 0, len(keys))
	for _, key := range keys {
		var task model.Transfer
		if err := tx.Get(ctx, "transfer", key, &task); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func (m *Manager) SetState(ctx context.Context, id, state string) (model.World, bool, error) {
	if state == "active" {
		return m.RequestRestore(ctx, id)
	}
	var result model.World
	if state != "archived" {
		return result, false, model.Failure("invalid_request", "world state must be active or archived")
	}
	pending := false
	err := m.db.Update(ctx, func(tx *store.Tx) error {
		if err := tx.Get(ctx, "world", id, &result); err != nil {
			return missing(err)
		}
		if restoring(result) {
			return model.Failure("conflict", "world restoration is in progress")
		}
		if result.State == "archived" {
			return nil
		}
		rooms, err := store.All[model.Room](ctx, tx, "room")
		if err != nil {
			return err
		}
		for _, room := range rooms {
			if room.World != nil && *room.World == id && room.Generation > 0 && room.State != "closed" {
				return model.Failure("conflict", "world still has a room that has accepted a start")
			}
		}
		var revision model.WorldRevision
		if err = tx.Get(ctx, "world_revision", id+":"+result.LatestRev, &revision); err != nil {
			return err
		}
		if err = m.queueRevision(ctx, tx, revision); err != nil {
			return err
		}
		tasks, err := m.requiredTransfers(ctx, tx, revision)
		if err != nil {
			return err
		}
		for _, task := range tasks {
			if task.Status != "verified" || task.VerifiedAt == nil {
				pending = true
			}
		}
		if pending {
			return nil
		}
		result.State = "archived"
		return tx.Put(ctx, "world", id, result)
	})
	if err == nil && pending {
		err = model.Failure("archive_failed", "world, pack or metadata is awaiting archive verification")
	}
	return result, false, err
}

func (m *Manager) RequestRestore(ctx context.Context, id string) (model.World, bool, error) {
	var result model.World
	accepted := false
	err := m.db.Update(ctx, func(tx *store.Tx) error {
		if err := tx.Get(ctx, "world", id, &result); err != nil {
			return missing(err)
		}
		if restoring(result) {
			accepted = true
			return nil
		}
		var revision model.WorldRevision
		if err := tx.Get(ctx, "world_revision", id+":"+result.LatestRev, &revision); err != nil {
			return err
		}
		if err := revisionPolicyRecorded(revision); err != nil {
			return err
		}
		tasks, err := m.requiredTransfers(ctx, tx, revision)
		if err != nil {
			return err
		}
		complete := true
		for _, task := range tasks {
			valid, err := intact(m.absolute(task.LocalPath), task.SHA256, task.Size)
			if err != nil {
				return err
			}
			if !valid {
				complete = false
				if task.Status != "verified" || task.VerifiedAt == nil {
					return model.Failure("archive_failed", "world has no verified archive source for a missing file")
				}
			}
		}
		if complete {
			if err = worldarchive.Validate(m.absolute(revision.World.Path), revision.Minecraft, revision.ExtraRootEntries...); err != nil {
				return model.Failure("archive_failed", err.Error())
			}
			if revision.ResourcePack != nil {
				if err = validatePack(m.absolute(PackPath(revision.ResourcePack.ID, revision.ResourcePack.Rev))); err != nil {
					return model.Failure("archive_failed", err.Error())
				}
			}
			result.State = "active"
			if result.Restore != nil && result.Restore.Status == "failed" {
				now := time.Now().UTC()
				result.Restore.Status, result.Restore.Error, result.Restore.FinishedAt = "succeeded", nil, &now
			}
			return tx.Put(ctx, "world", id, result)
		}
		attempt, err := tx.Next(ctx, "world_restore:"+id)
		if err != nil {
			return err
		}
		result.State = "archived"
		result.Restore = &model.WorldRestore{Restore: model.Restore{Attempt: attempt, Status: "queued", RequestedAt: time.Now().UTC()}, Revision: revision.Rev}
		accepted = true
		return tx.Put(ctx, "world", id, result)
	})
	return result, accepted, err
}

func (m *Manager) RestorePending(ctx context.Context, download func(context.Context, string, string, string, int64) error) error {
	worlds, err := m.List(ctx)
	if err != nil {
		return err
	}
	for _, world := range worlds {
		if !restoring(world) {
			continue
		}
		if download == nil {
			return fmt.Errorf("world restore requires an archive downloader")
		}
		err = m.db.Update(ctx, func(tx *store.Tx) error {
			var current model.World
			if err := tx.Get(ctx, "world", world.ID, &current); err != nil {
				return err
			}
			if !restoring(current) || current.Restore.Attempt != world.Restore.Attempt || current.Restore.Revision != world.Restore.Revision {
				return model.Failure("conflict", "world restore attempt changed")
			}
			world = current
			world.Restore.Status = "running"
			if world.Restore.StartedAt == nil {
				now := time.Now().UTC()
				world.Restore.StartedAt = &now
			}
			return tx.Put(ctx, "world", world.ID, world)
		})
		if err != nil {
			return err
		}
		restoreCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
		restoreErr := m.restoreFiles(restoreCtx, world, download)
		cancel()
		writeCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err = m.db.Update(writeCtx, func(tx *store.Tx) error {
			var current model.World
			if err := tx.Get(writeCtx, "world", world.ID, &current); err != nil {
				return err
			}
			if current.Restore == nil || current.Restore.Attempt != world.Restore.Attempt || current.Restore.Revision != world.Restore.Revision {
				return model.Failure("conflict", "world restore attempt changed before publication")
			}
			now := time.Now().UTC()
			current.Restore.FinishedAt = &now
			if restoreErr != nil {
				current.State = "archived"
				current.Restore.Status = "failed"
				current.Restore.Error = model.Failure("archive_failed", restoreErr.Error())
			} else {
				current.State = "active"
				current.Restore.Status = "succeeded"
				current.Restore.Error = nil
			}
			return tx.Put(writeCtx, "world", current.ID, current)
		})
		done()
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) restoreFiles(ctx context.Context, worldRecord model.World, download func(context.Context, string, string, string, int64) error) error {
	revision, err := m.Revision(ctx, worldRecord.ID, worldRecord.Restore.Revision)
	if err != nil {
		return err
	}
	if err = revisionPolicyRecorded(revision); err != nil {
		return err
	}
	tasks, err := m.requiredTransfers(ctx, m.db, revision)
	if err != nil {
		return err
	}
	staged := map[string]string{}
	for _, task := range tasks {
		valid, err := intact(m.absolute(task.LocalPath), task.SHA256, task.Size)
		if err != nil {
			return err
		}
		if valid {
			continue
		}
		if task.Status != "verified" || task.VerifiedAt == nil {
			return fmt.Errorf("world restore source is not verified")
		}
		partial := path.Join(".work", "world-restore", worldRecord.ID, fmt.Sprintf("%d", worldRecord.Restore.Attempt), task.Kind+"-"+task.Part+".partial")
		if err = download(ctx, task.RemotePath, partial, task.SHA256, task.Size); err != nil {
			return err
		}
		valid, err = intact(m.absolute(partial), task.SHA256, task.Size)
		if err != nil {
			return err
		}
		if !valid {
			return fmt.Errorf("restored world file checksum mismatch")
		}
		staged[task.LocalPath] = partial
	}
	world := m.absolute(revision.World.Path)
	if partial, ok := staged[revision.World.Path]; ok {
		world = m.absolute(partial)
	}
	if err = worldarchive.Validate(world, revision.Minecraft, revision.ExtraRootEntries...); err != nil {
		return err
	}
	namespaces, err := m.scan(ctx, world, revision.Minecraft)
	if err != nil {
		return err
	}
	if !equalNamespaces(namespaces, revision.Requires.Namespaces) {
		return fmt.Errorf("restored world namespaces differ from the published revision")
	}
	if revision.ResourcePack != nil {
		filename := PackPath(revision.ResourcePack.ID, revision.ResourcePack.Rev)
		local := m.absolute(filename)
		if partial, ok := staged[filename]; ok {
			local = m.absolute(partial)
		}
		if err = validatePack(local); err != nil {
			return err
		}
	}
	for destination, partial := range staged {
		if err = os.MkdirAll(filepath.Dir(m.absolute(destination)), 0700); err != nil {
			return err
		}
		if err = os.Rename(m.absolute(partial), m.absolute(destination)); err != nil {
			return err
		}
	}
	return nil
}

func equalNamespaces(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}
