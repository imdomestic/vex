package projectstate

import (
	"context"
	"errors"
	"fmt"
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

// Initialize 只在历史构建及世界清单全部可用后开放项目写操作。
func (s *State) Initialize(ctx context.Context) error {
	if s.Check() == nil {
		return nil
	}
	if err := s.builds.InitializeRecords(ctx); err != nil {
		return fmt.Errorf("initialize build metadata: %w", err)
	}
	if err := s.rooms.InitializeWorldRecords(ctx); err != nil {
		return fmt.Errorf("initialize world metadata: %w", err)
	}
	s.once.Do(func() { close(s.ready) })
	return nil
}

func (s *State) Run(ctx context.Context) error {
	if err := s.Initialize(ctx); err != nil {
		refresh, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return errors.Join(err, s.git.Refresh(refresh))
	}
	<-ctx.Done()
	return ctx.Err()
}
