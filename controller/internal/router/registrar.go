package router

import (
	"context"

	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/runtime"
)

type Registrar interface {
	Sync(context.Context, runtime.ResourceRef, string) error
	Remove(context.Context, runtime.ResourceRef) error
}

type WakeRegistrar interface {
	Sleep(context.Context, model.Room, string, string) error
	Restore(context.Context, runtime.ResourceRef, string) error
	Forget(context.Context, model.Room) error
}
