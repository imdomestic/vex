package runtime

import (
	"context"
	"time"

	"github.com/imdomestic/vex/controller/internal/model"
)

type ResourceRef struct {
	ID         string            `json:"id"`
	WorldID    string            `json:"worldId"`
	Attributes map[string]string `json:"attributes"`
}

type RoomSpec struct {
	Room             model.Room  `json:"room"`
	Build            model.Build `json:"build"`
	Node             string      `json:"node"`
	Mods             []string    `json:"mods"`
	SeedURL          string      `json:"seedURL"`
	SeedSHA256       string      `json:"seedSHA256"`
	RoomToken        string      `json:"roomToken"`
	RCONPassword     string      `json:"rconPassword"`
	ResourcePackURL  string      `json:"resourcePackURL"`
	ResourcePackSHA1 string      `json:"resourcePackSHA1"`
	Whitelist        []string    `json:"whitelist"`
	EnforceWhitelist bool        `json:"enforceWhitelist"`
}

type RoomStatus struct {
	Exists          bool
	ProcessExists   bool
	Ready           bool
	ProcessReady    bool
	IdentityMatches bool
	Terminating     bool
	Node            string
	BuildID         string
	Generation      int64
	Phase           string
	Message         string
}

type StopOptions struct {
	Save    bool
	Timeout time.Duration
}

type DeleteOptions struct {
	KeepWorld bool
	Snapshot  *model.Snapshot
}

type LogOptions struct {
	Tail        int64
	Diagnostics bool
}

type ArchiveSpec struct {
	Room      model.Room
	Build     model.Build
	Resource  ResourceRef
	RoomToken string
}

type ArchiveState string

const (
	ArchivePending      ArchiveState = "pending"
	ArchiveComplete     ArchiveState = "complete"
	ArchiveWorldMissing ArchiveState = "worldMissing"
	ArchiveFailed       ArchiveState = "failed"
)

type ArchiveResult struct {
	State   ArchiveState
	Path    string
	Message string
}

type Backend interface {
	EnsureProject(context.Context) error
	Reference(model.Room) (ResourceRef, error)
	EnsureRoom(context.Context, RoomSpec) (ResourceRef, error)
	ObserveRoom(context.Context, ResourceRef) (RoomStatus, error)
	StopRoom(context.Context, ResourceRef, StopOptions) error
	ArchiveWorld(context.Context, ArchiveSpec) (ArchiveResult, error)
	DeleteRoom(context.Context, ResourceRef, DeleteOptions) error
	// DeleteWorld 按 UID 删除已停止房间的世界卷，返回被删除卷的 UID。
	DeleteWorld(context.Context, ResourceRef) (string, error)
	Logs(context.Context, ResourceRef, LogOptions) (string, error)
	RCON(context.Context, ResourceRef, string) (string, error)
	Capacity(context.Context) ([]model.Node, error)
}
