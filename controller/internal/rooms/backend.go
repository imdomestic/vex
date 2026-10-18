package rooms

import (
	"context"
	"fmt"

	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/router"
	"github.com/imdomestic/vex/controller/internal/runtime"
)

func (m *Manager) host(id string) string {
	return id + "--" + m.Cfg.ID + "." + m.Cfg.Server.Zone
}

func (m *Manager) observe(ctx context.Context, room model.Room, rt Runtime) (runtime.RoomStatus, error) {
	if rt.Resource.ID == "" {
		return runtime.RoomStatus{}, nil
	}
	return m.Backend.ObserveRoom(ctx, rt.Resource)
}

func (m *Manager) syncHost(ctx context.Context, room model.Room, rt Runtime) error {
	if m.Cfg.Wake.Enabled && (room.Kind == "branch" || room.Kind == "pr") && room.State == "closed" {
		return m.forgetRoute(ctx, room)
	}
	if m.Cfg.Wake.Enabled && (room.Kind == "branch" || room.Kind == "pr") && (room.State == "stopped" || room.State == "failed") {
		registrar, ok := m.Registrar.(router.WakeRegistrar)
		if !ok {
			return fmt.Errorf("runtime does not support sleeping room routes")
		}
		return registrar.Sleep(ctx, room, room.Host, m.Cfg.Wake.Endpoint)
	}
	if rt.Resource.ID == "" {
		return nil
	}
	return m.Registrar.Sync(ctx, rt.Resource, room.Host)
}

func (m *Manager) restoreRoute(ctx context.Context, room model.Room, rt Runtime) error {
	if !m.Cfg.Wake.Enabled || room.Kind != "branch" && room.Kind != "pr" {
		return nil
	}
	registrar, ok := m.Registrar.(router.WakeRegistrar)
	if !ok {
		return fmt.Errorf("runtime does not support sleeping room routes")
	}
	return registrar.Restore(ctx, rt.Resource, room.Host)
}

func (m *Manager) forgetRoute(ctx context.Context, room model.Room) error {
	if !m.Cfg.Wake.Enabled {
		return nil
	}
	registrar, ok := m.Registrar.(router.WakeRegistrar)
	if !ok {
		return fmt.Errorf("runtime does not support sleeping room routes")
	}
	return registrar.Forget(ctx, room)
}
