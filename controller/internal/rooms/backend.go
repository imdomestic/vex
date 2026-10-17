package rooms

import (
	"context"

	"github.com/imdomestic/vex/controller/internal/model"
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
	if rt.Resource.ID == "" {
		return nil
	}
	return m.Registrar.Sync(ctx, rt.Resource, room.Host)
}
