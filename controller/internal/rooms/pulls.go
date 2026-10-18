package rooms

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	buildmetadata "github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

func (m *Manager) pullAllowed(pull model.Pull) bool {
	if !m.Cfg.CI.PRPreviews || pull.State != "open" {
		return false
	}
	if !pull.Fork {
		return true
	}
	return m.Cfg.CI.ForkPreviews == "auto" || m.Cfg.CI.ForkPreviews == "approval" && pull.Approved
}

func (m *Manager) SyncPull(ctx context.Context, pull model.Pull, builds []model.Build) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if pull.Project != m.Cfg.ID || pull.Number < 1 || !ValidSHA(pull.HeadSHA) || pull.State != "open" && pull.State != "closed" {
		return model.Failure("invalid_request", "PR 项目、编号、状态或提交无效")
	}
	available := map[string]model.Build{}
	for _, build := range builds {
		if build.Project != m.Cfg.ID || build.SHA != pull.HeadSHA || build.Status != "ready" || !m.Cfg.Loaders[build.Loader].Enabled || build.ID != BuildID(build.SHA, build.Loader) {
			return model.Failure("invalid_request", "PR 构建与当前提交或项目不匹配")
		}
		available[build.Loader] = build
	}
	known, err := m.List(ctx)
	if err != nil {
		return err
	}
	pull.Rooms = map[string]string{}
	pull.Builds = map[string]string{}
	for loader, build := range available {
		pull.Builds[loader] = build.ID
	}
	loaders := map[string]bool{}
	for _, loader := range m.Cfg.EnabledLoaders() {
		loaders[loader] = true
	}
	for _, existing := range known {
		if existing.Kind == "pr" && existing.PullNumber != nil && *existing.PullNumber == pull.Number {
			pull.Rooms[existing.Loader] = existing.ID
			loaders[existing.Loader] = true
		}
	}
	allowed := m.pullAllowed(pull)
	ordered := make([]string, 0, len(loaders))
	for loader := range loaders {
		ordered = append(ordered, loader)
	}
	sort.Strings(ordered)
	for _, loader := range ordered {
		loaderAllowed := allowed && m.Cfg.Loaders[loader].Enabled
		id := fmt.Sprintf("pr%d-%s", pull.Number, loader)
		room, err := m.Get(ctx, id)
		exists := err == nil
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if exists && (room.Kind != "pr" || room.PullNumber == nil || *room.PullNumber != pull.Number || room.Loader != loader) {
			return model.Failure("conflict", "PR 房间 ID 与其他房间冲突")
		}
		if !exists {
			if !loaderAllowed {
				continue
			}
			if _, ready := available[loader]; !ready {
				continue
			}
			if len(id)+2+len(m.Cfg.ID) > 63 {
				return model.Failure("invalid_request", "PR 房间主机名过长")
			}
			room = m.newRoom(id, "pr", pull.HeadSHA, loader, m.Cfg.Rooms.DefaultSeed)
			number := pull.Number
			room.PullNumber = &number
			if err = m.restoreGeneration(ctx, &room); err != nil {
				return err
			}
		}
		pull.Rooms[loader] = id
		rt, err := m.runtime(ctx, id)
		if err != nil {
			return err
		}
		room.PreviewAccess = m.Cfg.CI.PreviewAccess
		room.PR = &model.PullMetadata{Number: pull.Number, Title: pull.Title, Author: pull.Author, URL: pull.URL, HeadSHA: pull.HeadSHA, FromFork: pull.Fork}
		if !loaderAllowed {
			room.Label = "finished"
			rt.Restart, rt.PullClosed, rt.PendingSHA = false, true, ""
			if (room.State == "stopped" || room.State == "closed") && !rt.WorldExists {
				room.State, room.Phase = "closed", "closed"
			} else if room.State != "lost" && room.State != "archiving" {
				room.State, room.Phase = "stopping", "PR closed"
			}
		} else {
			if rt.PullClosed {
				room.Label, rt.PullClosed = "testing", false
				if room.State == "closed" {
					room.State, room.Phase, room.ClosedAt = "stopped", "registered", nil
				}
			}
			room.BehindHead = room.SHA != pull.HeadSHA
			if next, ready := available[loader]; ready {
				if room.Generation == 0 {
					room.SHA, room.BuildID, room.BehindHead = next.SHA, next.ID, false
					rt.PendingSHA = ""
				} else if room.SHA != next.SHA {
					rt.PendingSHA = next.SHA
				} else {
					rt.PendingSHA = ""
				}
			} else {
				rt.PendingSHA = ""
			}
		}
		if err = m.save(ctx, room, rt); err != nil {
			return err
		}
	}
	pull.UpdatedAt = time.Now().UTC()
	return m.DB.Put(ctx, "pull", strconv.Itoa(pull.Number), pull)
}

func (m *Manager) pullStartRoom(ctx context.Context, room model.Room) (model.Room, error) {
	if room.PullNumber == nil {
		return room, model.Failure("conflict", "PR 房间缺少来源编号")
	}
	var pull model.Pull
	if err := m.DB.Get(ctx, "pull", strconv.Itoa(*room.PullNumber), &pull); err != nil {
		return room, err
	}
	if !m.pullAllowed(pull) || pull.Project != m.Cfg.ID || !ValidSHA(pull.HeadSHA) {
		return room, model.Failure("room_finished", "PR 已关闭或当前项目尚未允许预览")
	}
	var build model.Build
	id := BuildID(pull.HeadSHA, room.Loader)
	if err := m.DB.Get(ctx, "build", id, &build); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return room, model.Failure("conflict", "PR 当前提交尚无可用构建")
		}
		return room, err
	}
	if build.Project != m.Cfg.ID || build.SHA != pull.HeadSHA || build.Loader != room.Loader || build.Status != "ready" {
		return room, model.Failure("conflict", "PR 当前提交的构建尚未就绪")
	}
	if err := buildmetadata.ValidateMetadata(build); err != nil {
		return room, model.Failure("build_mismatch", "PR 构建尚未具有完整有效的 manifest 信息")
	}
	room.SHA, room.BuildID, room.BehindHead = build.SHA, build.ID, false
	room.PreviewAccess = m.Cfg.CI.PreviewAccess
	room.PR = &model.PullMetadata{Number: pull.Number, Title: pull.Title, Author: pull.Author, URL: pull.URL, HeadSHA: pull.HeadSHA, FromFork: pull.Fork}
	return room, nil
}

func (m *Manager) PackTarget(ctx context.Context, id string) (model.Room, model.Build, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	room, err := m.Get(ctx, id)
	if err != nil {
		return room, model.Build{}, err
	}
	if room.Kind == "pr" && room.State == "stopped" {
		room, err = m.pullStartRoom(ctx, room)
		if err != nil {
			return room, model.Build{}, err
		}
	}
	var build model.Build
	if err = m.DB.Get(ctx, "build", room.BuildID, &build); err != nil {
		return room, build, err
	}
	if build.Project != room.Project || build.SHA != room.SHA || build.Loader != room.Loader || build.Status != "ready" {
		return room, build, model.Failure("build_mismatch", "房间对应的客户端构建尚未就绪")
	}
	if err := buildmetadata.ValidateMetadata(build); err != nil {
		return room, build, model.Failure("build_mismatch", "房间构建尚未具有完整有效的 manifest 信息")
	}
	return room, build, nil
}
