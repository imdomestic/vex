package rooms

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/imdomestic/chorus-testbed/controller/internal/archive"
	"github.com/imdomestic/chorus-testbed/controller/internal/assets"
	"github.com/imdomestic/chorus-testbed/controller/internal/auth"
	"github.com/imdomestic/chorus-testbed/controller/internal/build"
	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/events"
	"github.com/imdomestic/chorus-testbed/controller/internal/gitmirror"
	"github.com/imdomestic/chorus-testbed/controller/internal/kube"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/slp"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
)

type Runtime struct {
	Generation           int64          `json:"generation"`
	Spec                 *kube.RoomSpec `json:"spec"`
	StartingAt           *time.Time     `json:"startingAt"`
	StoppedAt            *time.Time     `json:"stoppedAt"`
	LastProbe            *time.Time     `json:"lastProbe"`
	NodeMissingSince     *time.Time     `json:"nodeMissingSince"`
	PodNotReadySince     *time.Time     `json:"podNotReadySince"`
	LostFrom             string         `json:"lostFrom"`
	Failure              *model.Error   `json:"failure"`
	FailureLogsCaptured  bool           `json:"failureLogsCaptured"`
	ReconcileError       bool           `json:"reconcileError"`
	Online               int            `json:"online"`
	Restart              bool           `json:"restart"`
	WorldExists          bool           `json:"worldExists"`
	SeedRestoreRequested bool           `json:"seedRestoreRequested"`
	ArchiveComplete      bool           `json:"archiveComplete"`
}

type Manager struct {
	mu      sync.Mutex
	wake    reconcileSignals
	Cfg     config.Config
	DB      *store.Store
	Git     *gitmirror.Mirror
	Builds  *build.Manager
	Kube    *kube.Client
	Auth    *auth.Auth
	Events  *events.Bus
	Archive *archive.Manager
	Assets  *assets.Manager
}

func New(c config.Config, db *store.Store, g *gitmirror.Mirror, b *build.Manager, k *kube.Client, a *auth.Auth, e *events.Bus) *Manager {
	return &Manager{Cfg: c, DB: db, Git: g, Builds: b, Kube: k, Auth: a, Events: e}
}

func (m *Manager) Get(ctx context.Context, id string) (model.Room, error) {
	var room model.Room
	err := m.DB.Get(ctx, "room", id, &room)
	return room, err
}

func (m *Manager) List(ctx context.Context) ([]model.Room, error) {
	return store.All[model.Room](ctx, m.DB, "room")
}

func (m *Manager) runtime(ctx context.Context, id string) (Runtime, error) {
	var rt Runtime
	err := m.DB.Get(ctx, "room_runtime", id, &rt)
	if errors.Is(err, store.ErrNotFound) {
		return rt, nil
	}
	return rt, err
}

func (m *Manager) save(ctx context.Context, room model.Room, rt Runtime) error {
	room.UpdatedAt = time.Now().UTC()
	return m.DB.Update(ctx, func(tx *store.Tx) error {
		if err := tx.Put(ctx, "room", room.ID, room); err != nil {
			return err
		}
		return tx.Put(ctx, "room_runtime", room.ID, rt)
	})
}

func (m *Manager) newRoom(id, kind, sha, loader, seed string) model.Room {
	now := time.Now().UTC()
	return model.Room{ID: id, Kind: kind, SHA: sha, Loader: loader, BuildID: BuildID(sha, loader), Host: id + "." + m.Cfg.Minecraft.Zone, Port: 25565, State: "stopped", Phase: "registered", Label: "testing", Players: []model.Player{}, Seed: seed, CreatedAt: now, UpdatedAt: now}
}

func (m *Manager) Create(ctx context.Context, kind, sha, loader, seed, asset string) (model.Room, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.create(ctx, kind, sha, loader, seed, asset)
}

func (m *Manager) create(ctx context.Context, kind, sha, loader, seed, asset string) (model.Room, bool, error) {
	id, err := RoomID(kind, sha, loader, asset)
	if err != nil {
		return model.Room{}, false, err
	}
	if !m.Cfg.Loaders[loader].Enabled {
		return model.Room{}, false, model.Failure("invalid_request", "loader 尚未启用")
	}
	if err = m.Git.VerifyCommit(ctx, sha); err != nil {
		return model.Room{}, false, err
	}
	old, err := m.Get(ctx, id)
	if err == nil {
		if old.SHA != sha || old.Kind != kind || old.Loader != loader {
			return old, false, model.Failure("conflict", "房间短 ID 与其他提交冲突")
		}
		return old, false, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return model.Room{}, false, err
	}
	if seed == "" {
		seed = m.Cfg.Rooms.DefaultSeed
	}
	room := m.newRoom(id, kind, sha, loader, seed)
	if kind == "asset" {
		room.Asset = &asset
		room.Seed = "asset:" + asset
		if _, err = m.resolveSeed(ctx, room, room.Seed); err != nil {
			return room, false, err
		}
	}
	if err = m.DB.Put(ctx, "room", id, room); err != nil {
		return room, false, err
	}
	return room, true, nil
}

func (m *Manager) SyncBranches(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	refs, err := m.Git.Branches(ctx)
	if err != nil {
		return err
	}
	rooms, err := m.List(ctx)
	if err != nil {
		return err
	}
	known := map[string]model.Room{}
	for _, r := range rooms {
		known[r.ID] = r
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		seen[ref.Name] = true
		for _, loader := range m.Cfg.EnabledLoaders() {
			id := BranchID(ref.Name, loader, false)
			for _, existing := range rooms {
				if existing.Kind == "branch" && existing.Branch != nil && *existing.Branch == ref.Name && existing.Loader == loader {
					id = existing.ID
					break
				}
			}
			if existing, ok := known[id]; ok && (existing.Branch == nil || *existing.Branch != ref.Name) {
				id = BranchID(ref.Name, loader, true)
			}
			room, ok := known[id]
			if ok && (room.Branch == nil || *room.Branch != ref.Name) {
				return model.Failure("conflict", "分支房间 ID 冲突")
			}
			if !ok {
				room = m.newRoom(id, "branch", ref.Head, loader, m.Cfg.Rooms.DefaultSeed)
				name := ref.Name
				room.Branch = &name
			}
			room.Host = id + "." + m.Cfg.Minecraft.Zone
			room.BehindHead = room.SHA != ref.Head
			if room.Generation == 0 {
				room.SHA = ref.Head
				room.BuildID = BuildID(ref.Head, loader)
				room.BehindHead = false
			}
			if err = m.DB.Put(ctx, "room", id, room); err != nil {
				return err
			}
		}
	}
	for _, room := range rooms {
		if room.Kind == "branch" && room.Branch != nil && !seen[*room.Branch] && room.Label != "finished" {
			room.Label = "finished"
			rt, err := m.runtime(ctx, room.ID)
			if err != nil {
				return err
			}
			if room.State == "stopped" {
				room.State = "closed"
				room.Phase = "closed"
			} else if room.State != "closed" {
				room.State = "stopping"
				room.Phase = "saving world"
			}
			if err = m.save(ctx, room, rt); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) Branches(ctx context.Context) ([]model.Branch, error) {
	refs, err := m.Git.Branches(ctx)
	if err != nil {
		return nil, err
	}
	rooms, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	result := []model.Branch{}
	for _, ref := range refs {
		branch := model.Branch{Name: ref.Name, Head: ref.Head, Rooms: map[string]string{}, Builds: map[string]*model.BuildSummary{}}
		for _, loader := range m.Cfg.EnabledLoaders() {
			branch.Builds[loader] = nil
			for _, room := range rooms {
				if room.Branch != nil && *room.Branch == ref.Name && room.Loader == loader {
					branch.Rooms[loader] = room.ID
				}
			}
			var b model.Build
			err = m.DB.Get(ctx, "build", BuildID(ref.Head, loader), &b)
			if err == nil {
				branch.Builds[loader] = &model.BuildSummary{ID: b.ID, Status: b.Status}
			} else if !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
		}
		result = append(result, branch)
	}
	return result, nil
}

func (m *Manager) Refresh(ctx context.Context) error {
	before, err := m.Branches(ctx)
	if err != nil {
		return err
	}
	if err = m.Git.Refresh(ctx); err != nil {
		return err
	}
	if err = m.SyncBranches(ctx); err != nil {
		return err
	}
	after, err := m.Branches(ctx)
	if err != nil {
		return err
	}
	previous := map[string]string{}
	for _, b := range before {
		previous[b.Name] = b.Head
	}
	for _, b := range after {
		event := ""
		if head, ok := previous[b.Name]; !ok {
			event = "branch_added"
		} else if head != b.Head {
			event = "branch_moved"
		}
		delete(previous, b.Name)
		if event != "" {
			if err = m.Events.Publish(ctx, event, b); err != nil {
				return err
			}
		}
	}
	for name := range previous {
		if err = m.Events.Publish(ctx, "branch_deleted", map[string]string{"name": name}); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) resolveSeed(ctx context.Context, room model.Room, expression string) (*model.SeedResolved, error) {
	if expression == "empty" {
		return &model.SeedResolved{Kind: "empty"}, nil
	}
	kind, id, ok := strings.Cut(expression, ":")
	if !ok || id == "" {
		return nil, model.Failure("invalid_request", "世界种子格式无效")
	}
	switch kind {
	case "branch":
		rooms, err := m.List(ctx)
		if err != nil {
			return nil, err
		}
		var source *model.Room
		for _, candidate := range rooms {
			if candidate.Branch != nil && *candidate.Branch == id && candidate.Loader == room.Loader {
				copy := candidate
				source = &copy
				break
			}
		}
		if source == nil {
			return nil, model.Failure("not_found", "种子来源分支房间不存在")
		}
		snapshots, err := store.All[model.Snapshot](ctx, m.DB, "snapshot")
		if err != nil {
			return nil, err
		}
		sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].CapturedAt.After(snapshots[j].CapturedAt) })
		for _, snapshot := range snapshots {
			if snapshot.RoomID == source.ID {
				return m.resolveSeed(ctx, room, "snapshot:"+snapshot.ID)
			}
		}
		if source.SeedResolved != nil {
			return m.validateSeed(ctx, room, *source.SeedResolved)
		}
		return &model.SeedResolved{Kind: "empty"}, nil
	case "snapshot":
		return m.validateSeed(ctx, room, model.SeedResolved{Kind: kind, ID: id})
	case "asset":
		var asset model.Asset
		if err := m.DB.Get(ctx, "asset", id, &asset); err != nil {
			return nil, err
		}
		if asset.State != "active" || asset.Restore != nil && (asset.Restore.Status == "queued" || asset.Restore.Status == "running") {
			return nil, model.Failure("conflict", "资产尚未处于可进入状态")
		}
		return m.validateSeed(ctx, room, model.SeedResolved{Kind: kind, ID: id, Rev: asset.LatestRev})
	default:
		return nil, model.Failure("invalid_request", "未知的世界种子类型")
	}
}

func (m *Manager) validateSeed(ctx context.Context, room model.Room, seed model.SeedResolved) (*model.SeedResolved, error) {
	switch seed.Kind {
	case "empty":
		return &seed, nil
	case "snapshot":
		var snapshot model.Snapshot
		if err := m.DB.Get(ctx, "snapshot", seed.ID, &snapshot); err != nil {
			return nil, err
		}
		if snapshot.Minecraft != m.Cfg.Minecraft.Version {
			return nil, model.Failure("invalid_request", "种子 Minecraft 版本不一致")
		}
	case "asset":
		var revision model.AssetRevision
		if err := m.DB.Get(ctx, "asset_revision", seed.ID+":"+seed.Rev, &revision); err != nil {
			return nil, err
		}
		if revision.Minecraft != m.Cfg.Minecraft.Version {
			return nil, model.Failure("build_mismatch", "资产 Minecraft 版本不一致")
		}
		found := false
		for _, loader := range revision.Requires.Loaders {
			if loader == room.Loader {
				found = true
			}
		}
		if !found {
			return nil, model.Failure("build_mismatch", "资产不支持目标 loader")
		}
	default:
		return nil, model.Failure("invalid_request", "固定世界种子无效")
	}
	return &seed, nil
}

func (m *Manager) Start(ctx context.Context, id string, seed *string, identity auth.Identity) (model.StartResult, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.start(ctx, id, seed, identity)
}

func (m *Manager) start(ctx context.Context, id string, seed *string, identity auth.Identity) (model.StartResult, int, error) {
	var result model.StartResult
	room, err := m.Get(ctx, id)
	if err != nil {
		return result, 0, err
	}
	if err = CanStart(room); err != nil {
		return result, 0, err
	}
	if room.Asset != nil {
		asset, err := m.Assets.Get(ctx, *room.Asset)
		if err != nil {
			return result, 0, err
		}
		if asset.State != "active" || asset.Restore != nil && (asset.Restore.Status == "queued" || asset.Restore.Status == "running") {
			return result, 0, model.Failure("conflict", "资产尚未处于可进入状态")
		}
	}
	rt, err := m.runtime(ctx, id)
	if err != nil {
		return result, 0, err
	}
	if seed != nil {
		if room.Kind != "commit" {
			return result, 0, model.Failure("invalid_request", "仅 commit 房间接受 seed 参数")
		}
		if (Active(room.State) || rt.WorldExists || room.Generation > 0 && room.State != "closed") && *seed != room.Seed {
			return result, 0, model.Failure("conflict", "当前周期已经固定世界种子")
		}
		room.Seed = *seed
	}
	if !Active(room.State) {
		if room.State == "failed" {
			status, err := m.Kube.ObserveRoom(ctx, id)
			if err != nil {
				return result, 0, err
			}
			if status.PodExists {
				return result, 0, model.Failure("conflict", "旧周期 Pod 尚未退出，请先停止房间")
			}
		}
		if room.Kind == "branch" {
			refs, err := m.Git.Branches(ctx)
			if err != nil {
				return result, 0, err
			}
			found := false
			for _, ref := range refs {
				if room.Branch != nil && ref.Name == *room.Branch {
					room.SHA = ref.Head
					room.BuildID = BuildID(ref.Head, room.Loader)
					room.BehindHead = false
					found = true
					break
				}
			}
			if !found {
				return result, 0, model.Failure("not_found", "远端分支已经不存在")
			}
		}
		if !rt.WorldExists && (room.SeedResolved == nil || room.State == "closed") {
			room.SeedResolved, err = m.resolveSeed(ctx, room, room.Seed)
			if err != nil {
				return result, 0, err
			}
		}
		b, _, err := m.Builds.Request(ctx, room.SHA, room.Loader, false)
		if err != nil {
			return result, 0, err
		}
		if b.Status == "failed" {
			return result, 0, model.Failure("build_failed", "构建失败，请显式重新构建后启动")
		}
		if room.SeedResolved != nil && room.SeedResolved.Kind == "asset" && b.Status == "ready" {
			if err = m.assetCompatible(ctx, *room.SeedResolved, b); err != nil {
				return result, 0, err
			}
		}
		room.Generation++
		room.State = "queued"
		room.Phase = "waiting for build"
		room.LastError = nil
		room.IdleSince = nil
		room.Players = []model.Player{}
		rt = Runtime{Generation: room.Generation, WorldExists: rt.WorldExists}
		if room.SeedResolved != nil && room.SeedResolved.Kind == "snapshot" {
			var snapshot model.Snapshot
			if err = m.DB.Get(ctx, "snapshot", room.SeedResolved.ID, &snapshot); err != nil {
				return result, 0, err
			}
			if !snapshot.LocalAvailable {
				room.Phase = "restoring seed"
			}
		}
	}
	statusCode := 202
	if room.State == "ready" {
		status, probeErr := slp.Probe(ctx, m.Cfg.Rooms.RouterAddress, room.Host, room.Port)
		if probeErr == nil && slp.Matches(status, room) {
			statusCode = 200
			room.Players = status.Sample()
			rt.Online = status.Players.Online
		} else {
			room.State = "starting"
			room.Phase = "re-verify"
			now := time.Now().UTC()
			rt.StartingAt = &now
		}
	}
	now := time.Now().UTC()
	lease := model.Lease{ID: uuid.NewString(), Generation: room.Generation, ExpiresAt: now.Add(config.Duration(m.Cfg.Rooms.JoinLease))}
	record := model.LeaseRecord{Lease: lease, RoomID: room.ID, Owner: identity.User, FromRoom: identity.RoomID, FromGeneration: identity.Generation}
	room.UpdatedAt = now
	room.IdleSince = nil
	err = m.DB.Update(ctx, func(tx *store.Tx) error {
		if err := tx.Put(ctx, "room", room.ID, room); err != nil {
			return err
		}
		if err := tx.Put(ctx, "room_runtime", room.ID, rt); err != nil {
			return err
		}
		return tx.Put(ctx, "lease", lease.ID, record)
	})
	return model.StartResult{Room: room, Lease: lease}, statusCode, err
}

func (m *Manager) assetCompatible(ctx context.Context, seed model.SeedResolved, b model.Build) error {
	var revision model.AssetRevision
	if err := m.DB.Get(ctx, "asset_revision", seed.ID+":"+seed.Rev, &revision); err != nil {
		return err
	}
	return assets.ValidateCompatibility(revision, b)
}

func (m *Manager) Stop(ctx context.Context, id string, restart bool) (model.Room, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	room, err := m.Get(ctx, id)
	if err != nil {
		return room, 0, err
	}
	rt, err := m.runtime(ctx, id)
	if err != nil {
		return room, 0, err
	}
	if room.State == "lost" {
		return room, 0, model.Failure("node_lost", "无法确认失联节点的世界状态")
	}
	if room.State == "stopped" || room.State == "closed" {
		if !restart {
			return room, 200, nil
		}
		result, status, err := m.start(ctx, id, nil, auth.Identity{User: "controller"})
		return result.Room, status, err
	}
	rt.Restart = restart
	rt.Failure = nil
	room.State = "stopping"
	room.Phase = "saving world"
	room.IdleSince = nil
	if err = m.save(ctx, room, rt); err != nil {
		return room, 0, err
	}
	return room, 202, nil
}

func (m *Manager) Label(ctx context.Context, id, label string) (model.Room, int, error) {
	if label != "testing" && label != "finished" {
		return model.Room{}, 0, model.Failure("invalid_request", "房间标签无效")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	room, err := m.Get(ctx, id)
	if err != nil {
		return room, 0, err
	}
	rt, err := m.runtime(ctx, id)
	if err != nil {
		return room, 0, err
	}
	room.Label = label
	if label == "finished" {
		rt.Restart = false
		rt.Failure = nil
	}
	status := 200
	if label == "finished" && room.State != "closed" {
		if room.Generation == 0 || room.State == "stopped" {
			room.State = "closed"
			room.Phase = "closed"
		} else {
			room.State = "stopping"
			room.Phase = "saving world"
			status = 202
		}
	}
	return room, status, m.save(ctx, room, rt)
}

func (m *Manager) DeleteLease(ctx context.Context, roomID, leaseID string, identity auth.Identity) error {
	var lease model.LeaseRecord
	err := m.DB.Get(ctx, "lease", leaseID, &lease)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if lease.RoomID != roomID {
		return model.Failure("not_found", "租约不存在")
	}
	if identity.User != "" && lease.Owner == identity.User || identity.RoomID != "" && identity.RoomID == lease.FromRoom && identity.Generation == lease.FromGeneration {
		return m.DB.Delete(ctx, "lease", leaseID)
	}
	return model.Failure("unauthorized", "无权取消此租约")
}

func randomSecret() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func (m *Manager) makeSpec(ctx context.Context, room model.Room, b model.Build, node string) (kube.RoomSpec, error) {
	spec := kube.RoomSpec{Room: room, Build: b, Node: node, Mods: []string{}}
	expires := time.Now().Add(30 * 24 * time.Hour)
	var err error
	spec.RoomToken, err = m.Auth.RoomToken(room.ID, room.Generation, room.BuildID, expires)
	if err != nil {
		return spec, err
	}
	spec.RCONPassword, err = randomSecret()
	if err != nil {
		return spec, err
	}
	for _, file := range b.Files {
		if file.Side == "both" || file.Side == "server" {
			u, err := m.Auth.SignURL(m.Cfg.Server.InternalBaseURL, file.URL, expires)
			if err != nil {
				return spec, err
			}
			spec.Mods = append(spec.Mods, u)
		}
	}
	if room.SeedResolved != nil {
		seed := room.SeedResolved
		path := ""
		if seed.Kind == "snapshot" {
			var snapshot model.Snapshot
			if err = m.DB.Get(ctx, "snapshot", seed.ID, &snapshot); err != nil {
				return spec, err
			}
			path = "/v1/snapshots/" + seed.ID + "/download"
			spec.SeedSHA256 = snapshot.SHA256
		} else if seed.Kind == "asset" {
			var revision model.AssetRevision
			if err = m.DB.Get(ctx, "asset_revision", seed.ID+":"+seed.Rev, &revision); err != nil {
				return spec, err
			}
			path = revision.World.URL
			spec.SeedSHA256 = revision.World.SHA256
			if revision.ResourcePack != nil {
				var pack model.PackRevision
				if err = m.DB.Get(ctx, "pack_revision", revision.ResourcePack.ID+":"+revision.ResourcePack.Rev, &pack); err != nil {
					return spec, err
				}
				spec.ResourcePackURL, err = m.Auth.SignURL(m.Cfg.Server.PublicBaseURL, pack.URL, expires)
				if err != nil {
					return spec, err
				}
				spec.ResourcePackSHA1 = pack.SHA1
			}
		}
		if path != "" {
			spec.SeedURL, err = m.Auth.SignURL(m.Cfg.Server.InternalBaseURL, path, expires)
			if err != nil {
				return spec, err
			}
		}
	}
	return spec, nil
}

func SnapshotPath(data string, snapshot model.Snapshot) string {
	return filepath.Join(data, "snapshots", snapshot.RoomID, snapshot.ID+".tar.gz")
}

func (m *Manager) Fail(ctx context.Context, room model.Room, rt Runtime, code, message string) error {
	return m.failWithError(ctx, room, rt, model.Failure(code, message))
}

func (m *Manager) Log(ctx context.Context, id string) (string, error) {
	room, err := m.Get(ctx, id)
	if err != nil {
		return "", err
	}
	var saved string
	err = m.DB.Get(ctx, "room_failure_log", fmt.Sprintf("%s:%d", id, room.Generation), &saved)
	if err == nil {
		return saved, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	return m.Kube.Logs(ctx, id, 200)
}

func (m *Manager) String() string { return fmt.Sprintf("rooms namespace=%s", m.Cfg.Rooms.Namespace) }
