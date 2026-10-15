package assets

import (
	"context"
	"errors"
	"os"

	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
)

func (m *Manager) EvictArchived(ctx context.Context) error {
	if !m.cfg.Archive.Enabled {
		return nil
	}
	return m.db.Update(ctx, func(tx *store.Tx) error {
		assets, err := store.All[model.Asset](ctx, tx, "asset")
		if err != nil {
			return err
		}
		revisions, err := store.All[model.AssetRevision](ctx, tx, "asset_revision")
		if err != nil {
			return err
		}
		rooms, err := store.All[model.Room](ctx, tx, "room")
		if err != nil {
			return err
		}
		protected := map[string]bool{}
		for _, room := range rooms {
			if room.Generation == 0 || room.State == "closed" {
				continue
			}
			if room.Asset != nil {
				protected[*room.Asset] = true
			}
			if room.SeedResolved != nil && room.SeedResolved.Kind == "asset" {
				protected[room.SeedResolved.ID] = true
			}
		}
		archived := map[string]bool{}
		for _, asset := range assets {
			if asset.State == "archived" && !restoring(asset) && !protected[asset.ID] {
				archived[asset.ID] = true
			}
		}
		for _, revision := range revisions {
			if !archived[revision.ID] {
				continue
			}
			tasks, err := m.requiredTransfers(ctx, tx, revision)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			verified := true
			for _, task := range tasks {
				if task.Status != "verified" || task.VerifiedAt == nil {
					verified = false
				}
			}
			if !verified {
				continue
			}
			if err = os.Remove(m.absolute(WorldPath(revision.ID, revision.Rev))); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if revision.ResourcePack == nil {
				continue
			}
			exclusive := true
			for _, other := range revisions {
				if other.ID != revision.ID && other.ResourcePack != nil && *other.ResourcePack == *revision.ResourcePack {
					exclusive = false
					break
				}
			}
			if exclusive {
				if err = os.Remove(m.absolute(PackPath(revision.ResourcePack.ID, revision.ResourcePack.Rev))); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
		}
		return nil
	})
}
