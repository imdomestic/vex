package rooms

import (
	"context"
	"errors"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/archive"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
)

type roomTombstone struct {
	Kind       string  `json:"kind"`
	SHA        string  `json:"sha"`
	Loader     string  `json:"loader"`
	Asset      *string `json:"asset"`
	Generation int64   `json:"generation"`
}

func setClosedAt(room *model.Room, now time.Time) {
	if room.State != "closed" {
		room.ClosedAt = nil
	} else if room.ClosedAt == nil {
		room.ClosedAt = &now
	}
}

func (m *Manager) InitializeRecords(ctx context.Context) error {
	return m.DB.Update(ctx, func(tx *store.Tx) error {
		rooms, err := store.All[model.Room](ctx, tx, "room")
		if err != nil {
			return err
		}
		for _, room := range rooms {
			if room.State != "closed" || room.ClosedAt != nil {
				continue
			}
			closed := room.UpdatedAt
			room.ClosedAt = &closed
			if err = tx.Put(ctx, "room", room.ID, room); err != nil {
				return err
			}
		}
		return nil
	})
}

func (m *Manager) restoreGeneration(ctx context.Context, room *model.Room) error {
	var tombstone roomTombstone
	err := m.DB.Get(ctx, "room_tombstone", room.ID, &tombstone)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	assetMatches := tombstone.Asset == nil && room.Asset == nil || tombstone.Asset != nil && room.Asset != nil && *tombstone.Asset == *room.Asset
	if tombstone.Kind != room.Kind || tombstone.SHA != room.SHA || tombstone.Loader != room.Loader || !assetMatches {
		return model.Failure("conflict", "房间短 ID 与已经清理的其他房间冲突")
	}
	room.Generation = tombstone.Generation
	return nil
}

func (m *Manager) PruneClosed(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pruneClosed(ctx, time.Now().UTC())
}

func (m *Manager) pruneClosed(ctx context.Context, now time.Time) error {
	return m.DB.Update(ctx, func(tx *store.Tx) error {
		rooms, err := store.All[model.Room](ctx, tx, "room")
		if err != nil {
			return err
		}
		snapshots, err := store.All[model.Snapshot](ctx, tx, "snapshot")
		if err != nil {
			return err
		}
		retained := archive.RetainedSnapshots(snapshots, rooms, m.Cfg.Archive.KeepPerRoom, m.Cfg.Archive.KeepDays, now)
		protected := map[string]bool{}
		for _, snapshot := range snapshots {
			if retained[snapshot.ID] {
				protected[snapshot.RoomID] = true
			}
		}
		leases, err := store.All[model.LeaseRecord](ctx, tx, "lease")
		if err != nil {
			return err
		}
		cutoff := now.Add(-30 * 24 * time.Hour)
		for _, room := range rooms {
			if room.Kind != "commit" && room.Kind != "asset" || room.State != "closed" || room.ClosedAt == nil || !room.ClosedAt.Before(cutoff) || protected[room.ID] {
				continue
			}
			var runtime Runtime
			if err = tx.Get(ctx, "room_runtime", room.ID, &runtime); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if runtime.WorldExists || runtime.Restart {
				continue
			}
			tombstone := roomTombstone{Kind: room.Kind, SHA: room.SHA, Loader: room.Loader, Asset: room.Asset, Generation: room.Generation}
			var previous roomTombstone
			if err = tx.Get(ctx, "room_tombstone", room.ID, &previous); err == nil {
				if previous.Generation > tombstone.Generation {
					tombstone.Generation = previous.Generation
				}
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if err = tx.Put(ctx, "room_tombstone", room.ID, tombstone); err != nil {
				return err
			}
			for _, kind := range []string{"room", "room_runtime"} {
				if err = tx.Delete(ctx, kind, room.ID); err != nil {
					return err
				}
			}
			for _, lease := range leases {
				if lease.RoomID == room.ID {
					if err = tx.Delete(ctx, "lease", lease.ID); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}
