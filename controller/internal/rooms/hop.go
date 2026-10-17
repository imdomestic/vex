package rooms

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/worlds"
)

func (m *Manager) hopSource(ctx context.Context, from string, identity auth.Identity) (model.Room, model.Build, error) {
	var build model.Build
	room, err := m.Get(ctx, from)
	if err != nil || identity.Project != m.Cfg.ID || identity.RoomID != from || room.Generation != identity.Generation || room.BuildID != identity.BuildID {
		return room, build, model.Failure("unauthorized", "来源房间凭据无效")
	}
	if room.State != "ready" {
		return room, build, model.Failure("conflict", "来源房间尚未就绪")
	}
	err = m.DB.Get(ctx, "build", room.BuildID, &build)
	return room, build, err
}

func (m *Manager) nextBuild(ctx context.Context, room model.Room) (string, error) {
	if room.Kind != "branch" || Active(room.State) {
		return room.BuildID, nil
	}
	branches, err := m.Git.Branches(ctx)
	if err != nil {
		return "", err
	}
	for _, branch := range branches {
		if room.Branch != nil && branch.Name == *room.Branch {
			return BuildID(branch.Head, room.Loader), nil
		}
	}
	return "", model.Failure("not_found", "目标分支已经不存在")
}

func (m *Manager) worldDestination(ctx context.Context, world model.World, build model.Build) (model.HopDestination, error) {
	var result model.HopDestination
	if world.State != "active" || world.Restore != nil && (world.Restore.Status == "queued" || world.Restore.Status == "running") {
		return result, model.Failure("conflict", "世界尚未处于可进入状态")
	}
	id, err := RoomID("world", build.SHA, build.Loader, world.ID)
	if err != nil {
		return result, err
	}
	if len(id)+2+len(m.Cfg.ID) > 63 {
		return result, model.Failure("invalid_request", "房间和项目组成的主机名超过 63 个字符")
	}
	revision := world.LatestRev
	state := "available"
	var existingRoomID *string
	worldExists := false
	room, err := m.Get(ctx, id)
	if err == nil {
		if room.Label != "testing" || room.State == "stopping" || room.State == "archiving" || room.State == "lost" {
			return result, model.Failure("conflict", "世界房间当前无法进入")
		}
		state = room.State
		existingRoomID = &room.ID
		runtime, err := m.runtime(ctx, id)
		if err != nil {
			return result, err
		}
		worldExists = runtime.WorldExists
		if (worldExists || Active(room.State)) && room.SeedResolved != nil {
			revision = room.SeedResolved.Rev
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return result, err
	}
	value, err := m.Worlds.Revision(ctx, world.ID, revision)
	if err != nil {
		return result, err
	}
	if err = worlds.ValidateCompatibility(value, build); err != nil {
		return result, err
	}
	if !worldExists {
		file, err := m.Worlds.OpenWorld(ctx, world.ID, revision)
		if err != nil {
			return result, err
		}
		if err = file.Close(); err != nil {
			return result, err
		}
		if value.ResourcePack != nil {
			file, err = m.Worlds.OpenPack(ctx, value.ResourcePack.ID, value.ResourcePack.Rev)
			if err != nil {
				return result, err
			}
			if err = file.Close(); err != nil {
				return result, err
			}
		}
	}
	return model.HopDestination{Project: m.Cfg.ID, Dest: "world:" + world.ID, Kind: "world", ID: world.ID, Title: world.Title, RoomID: existingRoomID, BuildID: build.ID, State: state}, nil
}

func (m *Manager) Destinations(ctx context.Context, from string, identity auth.Identity) ([]model.HopDestination, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	source, build, err := m.hopSource(ctx, from, identity)
	if err != nil {
		return nil, err
	}
	result := []model.HopDestination{}
	allWorlds, err := m.Worlds.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, world := range allWorlds {
		value, err := m.worldDestination(ctx, world, build)
		if err != nil {
			var business *model.Error
			if errors.As(err, &business) {
				continue
			}
			return nil, err
		}
		if value.RoomID == nil || *value.RoomID != source.ID {
			result = append(result, value)
		}
	}
	allRooms, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, room := range allRooms {
		if room.ID == source.ID || room.Kind == "world" || room.Label != "testing" {
			continue
		}
		if room.State != "stopped" && room.State != "queued" && room.State != "building" && room.State != "starting" && room.State != "ready" {
			continue
		}
		buildID, err := m.nextBuild(ctx, room)
		if err != nil {
			var business *model.Error
			if errors.As(err, &business) {
				continue
			}
			return nil, err
		}
		if buildID != source.BuildID {
			continue
		}
		id := room.ID
		result = append(result, model.HopDestination{Project: m.Cfg.ID, Dest: "room:" + id, Kind: "room", ID: id, Title: id, RoomID: &id, BuildID: buildID, State: room.State})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

func (m *Manager) Hop(ctx context.Context, from, destination string, player model.Player, identity auth.Identity) (model.StartResult, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result model.StartResult
	if _, err := uuid.Parse(player.UUID); err != nil || player.Name == "" || strings.ContainsAny(player.Name, "\x00\r\n") {
		return result, 0, model.Failure("invalid_request", "玩家身份格式无效")
	}
	source, build, err := m.hopSource(ctx, from, identity)
	if err != nil {
		return result, 0, err
	}
	kind, id, qualified := strings.Cut(destination, ":")
	if !qualified {
		id = destination
		var world model.World
		worldErr := m.DB.Get(ctx, "world", id, &world)
		_, roomErr := m.Get(ctx, id)
		if worldErr == nil && roomErr == nil {
			return result, 0, model.Failure("conflict", "目的地同时匹配世界和房间，请使用 world: 或 room: 前缀")
		}
		if worldErr == nil {
			kind = "world"
		} else if roomErr == nil {
			kind = "room"
		} else {
			if !errors.Is(worldErr, store.ErrNotFound) {
				return result, 0, worldErr
			}
			if !errors.Is(roomErr, store.ErrNotFound) {
				return result, 0, roomErr
			}
			return result, 0, model.Failure("not_found", "目的地不存在")
		}
	}
	switch kind {
	case "world":
		world, err := m.Worlds.Get(ctx, id)
		if err != nil {
			return result, 0, err
		}
		if _, err = m.worldDestination(ctx, world, build); err != nil {
			return result, 0, err
		}
		room, _, err := m.create(ctx, "world", source.SHA, source.Loader, "world:"+id, id)
		if err != nil {
			return result, 0, err
		}
		id = room.ID
	case "room":
		room, err := m.Get(ctx, id)
		if err != nil {
			return result, 0, err
		}
		buildID, err := m.nextBuild(ctx, room)
		if err != nil {
			return result, 0, err
		}
		if buildID != source.BuildID {
			return result, 0, model.Failure("build_mismatch", "目标房间使用不同的构建")
		}
	default:
		return result, 0, model.Failure("invalid_request", "目的地前缀无效")
	}
	if id == source.ID {
		return result, 0, model.Failure("conflict", "玩家已经在目标房间")
	}
	result, status, err := m.start(ctx, id, nil, identity)
	if err != nil {
		return result, 0, err
	}
	if result.Room.BuildID != source.BuildID {
		return result, 0, model.Failure("build_mismatch", "目标房间构建已经改变")
	}
	var lease model.LeaseRecord
	if err = m.DB.Get(ctx, "lease", result.Lease.ID, &lease); err != nil {
		return result, 0, err
	}
	lease.PlayerUUID = player.UUID
	if err = m.DB.Put(ctx, "lease", lease.ID, lease); err != nil {
		return result, 0, err
	}
	return result, status, nil
}
