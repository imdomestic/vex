package router

import (
	"context"

	"github.com/imdomestic/vex/controller/internal/runtime"
)

type Registrar interface {
	Sync(context.Context, runtime.ResourceRef, string) error
	Remove(context.Context, runtime.ResourceRef) error
}
