package rooms

import (
	"context"
	"errors"
	"time"

	"github.com/imdomestic/vex/controller/internal/archive"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

type roomTombstone struct {
	Kind       string  `json:"kind"`
	SHA        string  `json:"sha"`
	Loader     string  `json:"loader"`
	World      *string `json:"world"`
	PullNumber *int    `json:"pullNumber"`
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
	worldMatches := tombstone.World == nil && room.World == nil || tombstone.World != nil && room.World != nil && *tombstone.World == *room.World
	identityMatches := tombstone.SHA == room.SHA
	if room.Kind == "pr" {
		identityMatches = tombstone.PullNumber != nil && room.PullNumber != nil && *tombstone.PullNumber == *room.PullNumber
	}
	if tombstone.Kind != room.Kind || !identityMatches || tombstone.Loader != room.Loader || !worldMatches {
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

func (m *Manager) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.DB.Update(ctx, func(tx *store.Tx) error {
		var room model.Room
		if err := tx.Get(ctx, "room", id, &room); err != nil {
			return err
		}
		if room.Project != m.Cfg.ID || room.State != "closed" {
			return model.Failure("conflict", "仅允许删除当前项目已经关闭的房间记录")
		}
		var rt Runtime
		if err := tx.Get(ctx, "room_runtime", id, &rt); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if rt.WorldExists || rt.Restart {
			return model.Failure("conflict", "房间仍然保留世界或待执行的启动请求")
		}
		allRooms, err := store.All[model.Room](ctx, tx, "room")
		if err != nil {
			return err
		}
		snapshots, err := store.All[model.Snapshot](ctx, tx, "snapshot")
		if err != nil {
			return err
		}
		retained := archive.RetainedSnapshots(snapshots, allRooms, m.Cfg.Archive.KeepPerRoom, m.Cfg.Archive.KeepDays, time.Now().UTC())
		for _, snapshot := range snapshots {
			if snapshot.RoomID == id && retained[snapshot.ID] {
				return model.Failure("conflict", "房间快照仍受保留规则或世界种子引用保护")
			}
		}
		leases, err := store.All[model.LeaseRecord](ctx, tx, "lease")
		if err != nil {
			return err
		}
		for _, lease := range leases {
			if lease.RoomID == id && lease.ExpiresAt.After(time.Now()) {
				return model.Failure("conflict", "房间仍有有效租约")
			}
		}
		if err = m.forgetRoute(ctx, room); err != nil {
			return err
		}
		tombstone := roomTombstone{Kind: room.Kind, SHA: room.SHA, Loader: room.Loader, World: room.World, PullNumber: room.PullNumber, Generation: room.Generation}
		var previous roomTombstone
		if err = tx.Get(ctx, "room_tombstone", id, &previous); err == nil {
			if previous.Generation > tombstone.Generation {
				tombstone.Generation = previous.Generation
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err = tx.Put(ctx, "room_tombstone", id, tombstone); err != nil {
			return err
		}
		for _, kind := range []string{"room", "room_runtime"} {
			if err = tx.Delete(ctx, kind, id); err != nil {
				return err
			}
		}
		for _, lease := range leases {
			if lease.RoomID == id {
				if err = tx.Delete(ctx, "lease", lease.ID); err != nil {
					return err
				}
			}
		}
		return nil
	})
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
			if room.Kind != "commit" && room.Kind != "world" && room.Kind != "pr" || room.State != "closed" || room.ClosedAt == nil || !room.ClosedAt.Before(cutoff) || protected[room.ID] {
				continue
			}
			var runtime Runtime
			if err = tx.Get(ctx, "room_runtime", room.ID, &runtime); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if runtime.WorldExists || runtime.Restart {
				continue
			}
			leased := false
			for _, lease := range leases {
				leased = leased || lease.RoomID == room.ID && lease.ExpiresAt.After(now)
			}
			if leased {
				continue
			}
			if err = m.forgetRoute(ctx, room); err != nil {
				return err
			}
			tombstone := roomTombstone{Kind: room.Kind, SHA: room.SHA, Loader: room.Loader, World: room.World, PullNumber: room.PullNumber, Generation: room.Generation}
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
