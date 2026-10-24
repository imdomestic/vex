package projectstate

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/gitmirror"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/rooms"
)

type State struct {
	ready  chan struct{}
	once   sync.Once
	builds *build.Manager
	rooms  *rooms.Manager
	git    *gitmirror.Mirror
}

func New(builds *build.Manager, manager *rooms.Manager, mirror *gitmirror.Mirror) *State {
	return &State{ready: make(chan struct{}), builds: builds, rooms: manager, git: mirror}
}

func (s *State) Check() error {
	if s == nil {
		return nil
	}
	select {
	case <-s.ready:
		return nil
	default:
		return model.Failure("project_initializing", "项目正在补录历史清单，请稍后重试")
	}
}

func (s *State) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ready:
		return nil
	}
}

func (s *State) After(operation func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := s.Wait(ctx); err != nil {
			return err
		}
		return operation(ctx)
	}
}

// Initialize 补录已有记录后开放项目写操作。镜像缺少某个提交时先刷新一次；
// 刷新成功后仍无法读取的提交已经不在仓库中，相关记录列入补录结果，不阻止项目。
func (s *State) Initialize(ctx context.Context) error {
	if s.Check() == nil {
		return nil
	}
	issues, err := s.backfill(ctx)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(issues, func(issue model.RecordIssue) bool { return issue.Reason == "commit_unavailable" }) {
		refresh, cancel := context.WithTimeout(ctx, time.Minute)
		err = s.git.Refresh(refresh)
		cancel()
		if err != nil {
			return fmt.Errorf("refresh the mirror before listing unavailable commits: %w", err)
		}
		if issues, err = s.backfill(ctx); err != nil {
			return err
		}
	}
	for _, issue := range issues {
		slog.Warn("record cannot be backfilled", "project", s.rooms.Cfg.ID, "kind", issue.Kind, "id", issue.ID, "sha", issue.SHA, "reason", issue.Reason)
	}
	if err = s.rooms.DB.Put(ctx, "record_backfill", "status", model.RecordBackfill{CheckedAt: time.Now().UTC(), Issues: issues}); err != nil {
		return err
	}
	s.once.Do(func() { close(s.ready) })
	return nil
}

func (s *State) backfill(ctx context.Context) ([]model.RecordIssue, error) {
	builds, err := s.builds.Backfill(ctx)
	if err != nil {
		return nil, fmt.Errorf("backfill build records: %w", err)
	}
	records, err := s.rooms.Backfill(ctx)
	if err != nil {
		return nil, fmt.Errorf("backfill room, snapshot and world records: %w", err)
	}
	return append(builds, records...), nil
}

func (s *State) Run(ctx context.Context) error {
	if err := s.Initialize(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}
