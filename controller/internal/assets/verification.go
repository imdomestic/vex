package assets

import (
	"context"
	"errors"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
)

func (m *Manager) MarkVerified(ctx context.Context) error {
	return m.db.Update(ctx, func(tx *store.Tx) error {
		revisions, err := store.All[model.AssetRevision](ctx, tx, "asset_revision")
		if err != nil {
			return err
		}
		for _, revision := range revisions {
			if revision.TankVerifiedAt != nil {
				continue
			}
			tasks, err := m.requiredTransfers(ctx, tx, revision)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			var latest time.Time
			complete := true
			for _, task := range tasks {
				if task.Status != "verified" || task.VerifiedAt == nil {
					complete = false
					break
				}
				if task.VerifiedAt.After(latest) {
					latest = *task.VerifiedAt
				}
			}
			if complete {
				revision.TankVerifiedAt = &latest
				if err = tx.Put(ctx, "asset_revision", revision.ID+":"+revision.Rev, revision); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
