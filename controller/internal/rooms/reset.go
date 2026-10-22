package rooms

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

// WorldReset 记录一次分支房间世界重置。
type WorldReset struct {
	Project    string    `json:"project"`
	RoomID     string    `json:"roomId"`
	Generation int64     `json:"generation"`
	VolumeUID  string    `json:"volumeUid"`
	SnapshotID string    `json:"snapshotId"`
	Seed       string    `json:"seed"`
	At         time.Time `json:"at"`
}

// ResetWorld 删除已停止分支房间的世界卷，下次启动按 seed 创建世界。
// 当前世界必须已经由 generation 最大的快照完整归档，并且该快照已经通过 tank 校验。
func (m *Manager) ResetWorld(ctx context.Context, id, confirm, seed string) (model.Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	room, err := m.Get(ctx, id)
	if err != nil {
		return room, err
	}
	if confirm != room.ID {
		return room, model.Failure("invalid_request", "确认内容必须与房间 ID 完全一致")
	}
	if room.Kind != "branch" {
		return room, model.Failure("conflict", "只有分支房间可以重置世界")
	}
	if room.State != "stopped" && room.State != "closed" {
		return room, model.Failure("conflict", "房间需要先停止")
	}
	if room.Label != "finished" {
		return room, model.Failure("conflict", "房间需要先标记为 finished，避免重置期间被启动或唤醒")
	}
	rt, err := m.runtime(ctx, id)
	if err != nil {
		return room, err
	}
	if !rt.WorldExists {
		return room, model.Failure("conflict", "房间没有保留的世界")
	}
	snapshots, err := store.All[model.Snapshot](ctx, m.DB, "snapshot")
	if err != nil {
		return room, err
	}
	var latest *model.Snapshot
	for index := range snapshots {
		if snapshots[index].RoomID == room.ID && (latest == nil || snapshots[index].RoomGeneration > latest.RoomGeneration) {
			latest = &snapshots[index]
		}
	}
	if latest == nil || latest.Project != m.Cfg.ID || latest.RoomGeneration > room.Generation || latest.TankVerifiedAt == nil {
		return room, model.Failure("conflict", "当前世界最新的快照尚未通过 tank 校验")
	}
	if seed == "" {
		seed = "empty"
	}
	if _, err = m.resolveSeed(ctx, room, seed); err != nil {
		return room, err
	}
	ref, err := m.Backend.Reference(room)
	if err != nil {
		return room, err
	}
	uid, err := m.Backend.DeleteWorld(ctx, ref)
	if err != nil {
		return room, fmt.Errorf("delete room world volume: %w", err)
	}
	record := WorldReset{Project: m.Cfg.ID, RoomID: room.ID, Generation: room.Generation, VolumeUID: uid, SnapshotID: latest.ID, Seed: seed, At: time.Now().UTC()}
	room.Seed, room.SeedResolved, room.LastError = seed, nil, nil
	rt = Runtime{Generation: rt.Generation, Resource: ref}
	room.UpdatedAt = record.At
	err = m.DB.Update(ctx, func(tx *store.Tx) error {
		if err := tx.Put(ctx, "room", room.ID, room); err != nil {
			return err
		}
		if err := tx.Put(ctx, "room_runtime", room.ID, rt); err != nil {
			return err
		}
		key := fmt.Sprintf("%s-g%d", room.ID, room.Generation)
		var existing WorldReset
		if err := tx.Get(ctx, "world_reset", key, &existing); err == nil {
			key = fmt.Sprintf("%s-%d", key, record.At.UnixNano())
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		return tx.Put(ctx, "world_reset", key, record)
	})
	if err != nil {
		return room, err
	}
	return room, m.Events.Publish(ctx, m.Cfg.ID, "world_reset", record)
}
