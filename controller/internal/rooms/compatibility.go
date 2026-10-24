package rooms

import (
	"context"

	buildmetadata "github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

func (m *Manager) compatibleWorld(ctx context.Context, room model.Room, rt Runtime, build model.Build) error {
	if err := buildmetadata.ValidateMetadata(build); err != nil {
		return model.Failure("build_mismatch", "构建尚未具有完整有效的 manifest 信息")
	}
	if build.Project != m.Cfg.ID || build.Minecraft == nil || *build.Minecraft == "" {
		return model.Failure("build_mismatch", "构建缺少当前项目的 Minecraft 版本")
	}
	if rt.WorldExists {
		if rt.Spec != nil && rt.Spec.Build.Minecraft != nil && *rt.Spec.Build.Minecraft != *build.Minecraft {
			return model.Failure("build_mismatch", "保留世界与新构建的 Minecraft 版本不一致")
		}
		snapshots, err := store.All[model.Snapshot](ctx, m.DB, "snapshot")
		if err != nil {
			return err
		}
		var latest *model.Snapshot
		for i := range snapshots {
			if snapshots[i].RoomID == room.ID && (latest == nil || snapshots[i].RoomGeneration > latest.RoomGeneration) {
				latest = &snapshots[i]
			}
		}
		if latest != nil && (latest.Project != m.Cfg.ID || latest.Minecraft != *build.Minecraft) {
			return model.Failure("build_mismatch", "保留世界与新构建的 Minecraft 版本不一致")
		}
		return nil
	}
	if room.SeedResolved == nil || room.SeedResolved.Kind == "empty" {
		return nil
	}
	if room.SeedResolved.Kind == "world" {
		return m.worldCompatible(ctx, *room.SeedResolved, build)
	}
	if room.SeedResolved.Kind != "snapshot" {
		return model.Failure("invalid_request", "固定世界种子类型无效")
	}
	var snapshot model.Snapshot
	if err := m.DB.Get(ctx, "snapshot", room.SeedResolved.ID, &snapshot); err != nil {
		return err
	}
	if snapshot.Project != m.Cfg.ID || snapshot.Minecraft != *build.Minecraft {
		return model.Failure("build_mismatch", "快照种子与构建的 Minecraft 版本不一致")
	}
	return nil
}
