package rooms

import (
	"context"

	"github.com/google/uuid"
	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/model"
)

func (m *Manager) WakeAuthorize(ctx context.Context, roomID, name string, player uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := m.wakeAuthorize(ctx, roomID, player)
	return err
}

func (m *Manager) wakeAuthorize(ctx context.Context, roomID string, player uuid.UUID) (model.Room, error) {
	room, err := m.Get(ctx, roomID)
	if err != nil {
		return room, err
	}
	if !m.Cfg.Wake.Enabled || player == uuid.Nil {
		return room, model.Failure("unauthorized", "连接唤醒尚未启用或账号身份无效")
	}
	if room.Kind != "branch" && room.Kind != "pr" || room.State == "closed" {
		return room, model.Failure("room_finished", "此房间不接受连接唤醒")
	}
	if err = CanStart(room); err != nil {
		return room, err
	}
	access := "members"
	if room.Kind == "pr" {
		if _, err = m.pullStartRoom(ctx, room); err != nil {
			return room, err
		}
		access = room.PreviewAccess
	}
	if access == "public" && room.Kind == "pr" {
		return room, nil
	}
	for _, value := range m.Auth.MinecraftPlayers(m.Cfg.ID, access) {
		id, err := uuid.Parse(value)
		if err == nil && id == player {
			return room, nil
		}
	}
	return room, model.Failure("unauthorized", "此 Minecraft 账号没有唤醒该项目房间的权限")
}

func (m *Manager) WakeStart(ctx context.Context, roomID, name string, player uuid.UUID) error {
	schedulingMu.Lock()
	defer schedulingMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	room, err := m.wakeAuthorize(ctx, roomID, player)
	if err != nil {
		return err
	}
	if Active(room.State) {
		return nil
	}
	nodes, err := m.Backend.Capacity(ctx)
	if err != nil {
		return err
	}
	node, err := m.chooseNode(ctx, room, nodes)
	if err != nil {
		return err
	}
	if node == "" {
		return model.Failure("conflict", "当前没有可用的房间容量，请稍后连接")
	}
	started, _, err := m.start(ctx, roomID, nil, auth.Identity{User: "minecraft:" + player.String()})
	if err != nil {
		return err
	}
	return m.reconcileRoom(ctx, started.Room, nodes, true)
}
