package rooms

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/auth"
	"github.com/imdomestic/chorus-testbed/controller/internal/config"
	"github.com/imdomestic/chorus-testbed/controller/internal/kube"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/slp"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
)

func (m *Manager) Run(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := m.Reconcile(ctx); err != nil {
				var local *roomReconcileErrors
				if !errors.As(err, &local) {
					return err
				}
				slog.Error("room reconcile failed", "error", err)
			}
		case <-m.wake.channel():
			if err := m.Reconcile(ctx); err != nil {
				var local *roomReconcileErrors
				if !errors.As(err, &local) {
					return err
				}
				slog.Error("room reconcile failed", "error", err)
			}
		}
	}
}

type roomReconcileErrors struct{ cause error }

func (e *roomReconcileErrors) Error() string { return e.cause.Error() }
func (e *roomReconcileErrors) Unwrap() error { return e.cause }

func (m *Manager) Reconcile(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rooms, err := m.List(ctx)
	if err != nil {
		return err
	}
	nodes, err := m.Kube.Nodes(ctx)
	if err != nil {
		return err
	}
	hosts, forceAll := m.wake.take()
	var failures []error
	persistenceFailed := false
	for _, room := range rooms {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		roomContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = m.reconcileRoom(roomContext, room, nodes, forceAll || hosts[room.Host])
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if saveErr := m.recordReconcileError(ctx, room.ID, err); saveErr != nil {
				persistenceFailed = true
				failures = append(failures, fmt.Errorf("room %s error persistence: %w", room.ID, saveErr))
			}
			failures = append(failures, fmt.Errorf("room %s reconciliation failed", room.ID))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	if persistenceFailed {
		return errors.Join(failures...)
	}
	return &roomReconcileErrors{cause: errors.Join(failures...)}
}

func (m *Manager) reconcileRoom(ctx context.Context, room model.Room, nodes []model.Node, forceProbe bool) error {
	rt, err := m.runtime(ctx, room.ID)
	if err != nil {
		return err
	}
	if rt.ReconcileError {
		rt.ReconcileError = false
		room.LastError = nil
		if err = m.save(ctx, room, rt); err != nil {
			return err
		}
	}
	if room.Host != room.ID+"."+m.Cfg.Minecraft.Zone {
		room.Host = room.ID + "." + m.Cfg.Minecraft.Zone
		if err = m.save(ctx, room, rt); err != nil {
			return err
		}
	}
	if room.State == "failed" {
		if rt.Failure == nil {
			rt.Failure = room.LastError
			if rt.Failure == nil {
				rt.Failure = model.Failure("start_timeout", "服务器启动失败")
			}
		}
		if err = m.reconcileFailure(ctx, room, rt); err != nil {
			return err
		}
		return m.Kube.SyncHost(ctx, room.ID, room.Host)
	}
	if err = m.Kube.SyncHost(ctx, room.ID, room.Host); err != nil {
		return err
	}
	if room.State == "stopped" || room.State == "closed" {
		if rt.Restart && room.Label == "testing" {
			_, _, err := m.start(ctx, room.ID, nil, auth.Identity{User: "controller"})
			return err
		}
		return nil
	}
	now := time.Now().UTC()
	if room.Node != nil {
		ready := false
		for _, node := range nodes {
			if node.Name == *room.Node && node.Ready {
				ready = true
			}
		}
		if !ready {
			if rt.NodeMissingSince == nil {
				rt.NodeMissingSince = &now
			}
			if now.Sub(*rt.NodeMissingSince) >= 60*time.Second {
				if room.State != "lost" {
					rt.LostFrom = room.State
				}
				room.State = "lost"
				room.Phase = "node unavailable"
				room.LastError = model.Failure("node_lost", "节点持续不可用超过 60 秒")
			}
			return m.save(ctx, room, rt)
		}
		rt.NodeMissingSince = nil
	}
	if room.State == "lost" {
		status, err := m.Kube.ObserveRoom(ctx, room.ID)
		if err != nil {
			return err
		}
		if rt.Failure != nil {
			return m.reconcileFailure(ctx, room, rt)
		}
		resume := lostRecoveryState(room, rt, status) == "starting"
		room.State, room.Phase = "stopping", "recovering lost room"
		if resume {
			room.State, room.Phase = "starting", "re-verify"
			rt.StartingAt = &now
		}
		room.LastError, room.IdleSince = nil, nil
		rt.LastProbe, rt.PodNotReadySince, rt.LostFrom = nil, nil, ""
		if err = m.save(ctx, room, rt); err != nil {
			return err
		}
	}
	if rt.Failure != nil {
		return m.reconcileFailure(ctx, room, rt)
	}
	if room.State == "stopping" || room.State == "archiving" {
		return m.reconcileStop(ctx, room, rt)
	}
	if room.SeedResolved != nil && room.SeedResolved.Kind == "snapshot" && !rt.WorldExists {
		var snapshot model.Snapshot
		if err = m.DB.Get(ctx, "snapshot", room.SeedResolved.ID, &snapshot); err != nil {
			return err
		}
		if !snapshot.LocalAvailable {
			if !rt.SeedRestoreRequested {
				var restoreErr error
				snapshot, _, restoreErr = m.Archive.RequestSnapshotRestore(ctx, snapshot.ID)
				if restoreErr != nil {
					return m.failWithError(ctx, room, rt, &model.Error{Code: "archive_failed", Message: "种子快照恢复失败", Details: &model.ErrorDetails{SnapshotID: room.SeedResolved.ID}})
				}
				rt.SeedRestoreRequested = true
			}
			if snapshot.Restore != nil && snapshot.Restore.Status == "failed" {
				return m.failWithError(ctx, room, rt, &model.Error{Code: "archive_failed", Message: "种子快照恢复失败", Details: &model.ErrorDetails{SnapshotID: snapshot.ID}})
			} else {
				room.State = "queued"
				room.Phase = "restoring seed"
			}
			return m.save(ctx, room, rt)
		}
	}
	var b model.Build
	if err = m.DB.Get(ctx, "build", room.BuildID, &b); err != nil {
		return err
	}
	if b.Status == "failed" {
		return m.Fail(ctx, room, rt, "build_failed", "房间依赖的构建失败")
	}
	if b.Status != "ready" {
		room.State = "building"
		room.Phase = "building mods"
		return m.save(ctx, room, rt)
	}
	if b.Minecraft == nil || *b.Minecraft != m.Cfg.Minecraft.Version {
		return m.Fail(ctx, room, rt, "build_mismatch", "构建 Minecraft 版本与服务器配置不一致")
	}
	if room.SeedResolved != nil && room.SeedResolved.Kind == "asset" {
		if err = m.assetCompatible(ctx, *room.SeedResolved, b); err != nil {
			return m.Fail(ctx, room, rt, "build_mismatch", err.Error())
		}
	}
	if rt.Spec == nil {
		node, err := m.chooseNode(ctx, room, nodes)
		if err != nil {
			return err
		}
		if node == "" {
			room.State = "queued"
			room.Phase = "waiting for capacity"
			return m.save(ctx, room, rt)
		}
		spec, err := m.makeSpec(ctx, room, b, node)
		if err != nil {
			return err
		}
		rt.Spec = &spec
		rt.StartingAt = &now
		room.Node = &node
		room.State = "starting"
		room.Phase = "creating server"
		if err = m.save(ctx, room, rt); err != nil {
			return err
		}
	}
	if err = m.Kube.EnsureRoom(ctx, *rt.Spec); err != nil {
		return err
	}
	rt.WorldExists = true
	status, err := m.Kube.ObserveRoom(ctx, room.ID)
	if err != nil {
		return err
	}
	if status.PodReady {
		rt.PodNotReadySince = nil
	}
	if status.Ready && status.Generation == room.Generation && status.BuildID == room.BuildID {
		if room.State == "ready" && !forceProbe && rt.LastProbe != nil && now.Sub(*rt.LastProbe) < 15*time.Second {
			return m.save(ctx, room, rt)
		}
		probe, probeErr := slp.Probe(ctx, m.Cfg.Rooms.RouterAddress, room.Host, room.Port)
		rt.LastProbe = &now
		if probeErr == nil && slp.Matches(probe, room) {
			room.State = "ready"
			room.Phase = "ready"
			room.LastError = nil
			room.Players = probe.Sample()
			rt.Online = probe.Players.Online
			activeLease, err := m.hasLease(ctx, room, now)
			if err != nil {
				return err
			}
			if rt.Online == 0 && !activeLease {
				if room.IdleSince == nil {
					room.IdleSince = &now
				}
				idle := m.Cfg.Rooms.CommitIdle
				if room.Kind == "branch" {
					idle = m.Cfg.Rooms.BranchIdle
				} else if room.Kind == "asset" {
					idle = m.Cfg.Rooms.AssetIdle
				}
				if now.Sub(*room.IdleSince) >= config.Duration(idle) {
					room.State = "stopping"
					room.Phase = "idle timeout"
				}
			} else {
				room.IdleSince = nil
			}
			return m.save(ctx, room, rt)
		}
		room.State, room.Phase = "starting", "route unavailable"
		room.IdleSince, room.LastError = nil, nil
		return m.save(ctx, room, rt)
	}
	room.State, room.IdleSince = "starting", nil
	if status.PodReady {
		room.Phase = "verifying Pod identity"
		return m.save(ctx, room, rt)
	}
	if rt.PodNotReadySince == nil {
		rt.PodNotReadySince = &now
	}
	if now.Sub(*rt.PodNotReadySince) >= config.Duration(m.Cfg.Rooms.StartTimeout) {
		return m.Fail(ctx, room, rt, "start_timeout", "Pod 连续未就绪超过启动期限")
	}
	room.Phase = status.Phase
	if room.Phase == "" {
		room.Phase = "waiting for server"
	}
	return m.save(ctx, room, rt)
}

func lostRecoveryState(room model.Room, rt Runtime, status kube.RoomStatus) string {
	if room.Label == "finished" || rt.LostFrom == "stopping" || rt.LostFrom == "archiving" {
		return "stopping"
	}
	if status.Ready && status.Generation == room.Generation && status.BuildID == room.BuildID {
		return "starting"
	}
	return "stopping"
}

func (m *Manager) chooseNode(ctx context.Context, room model.Room, nodes []model.Node) (string, error) {
	rooms, err := m.List(ctx)
	if err != nil {
		return "", err
	}
	used := map[string]int{}
	for _, r := range rooms {
		if r.ID != room.ID && r.Node != nil && r.State != "closed" && r.State != "stopped" && r.State != "lost" && r.State != "failed" {
			used[*r.Node]++
		}
	}
	preference := []string{"seraph", "encore"}
	if room.Kind == "branch" {
		preference = []string{"encore"}
	}
	for _, name := range preference {
		for _, node := range nodes {
			if node.Name == name && node.Ready && node.Schedulable && used[name] < m.Cfg.Rooms.MaxRunning[name] {
				return name, nil
			}
		}
	}
	return "", nil
}

func (m *Manager) hasLease(ctx context.Context, room model.Room, now time.Time) (bool, error) {
	leases, err := store.All[model.LeaseRecord](ctx, m.DB, "lease")
	if err != nil {
		return false, err
	}
	found := false
	for _, lease := range leases {
		if !lease.ExpiresAt.After(now) {
			if err = m.DB.Delete(ctx, "lease", lease.ID); err != nil {
				return false, err
			}
			continue
		}
		if lease.RoomID == room.ID && lease.Generation == room.Generation {
			found = true
		}
	}
	return found, nil
}

func (m *Manager) reconcileStop(ctx context.Context, room model.Room, rt Runtime) error {
	status, err := m.Kube.ObserveRoom(ctx, room.ID)
	if err != nil {
		return err
	}
	if room.State == "stopping" {
		if status.PodExists {
			if room.Phase != "waiting for Pod exit" {
				if status.Ready {
					saveContext, cancel := context.WithTimeout(ctx, 10*time.Second)
					_, err = m.Kube.RCON(saveContext, room.ID, "save-all flush")
					cancel()
					if err != nil {
						slog.Warn("room save command unavailable; continuing graceful Pod termination", "room", room.ID)
					}
				}
				if err = m.Kube.ScaleRoom(ctx, room.ID, 0); err != nil {
					return err
				}
				room.Phase = "waiting for Pod exit"
				return m.save(ctx, room, rt)
			}
			return nil
		}
		if !rt.WorldExists {
			return m.finishStop(ctx, room, rt, nil)
		}
		now := time.Now().UTC()
		rt.StoppedAt = &now
		room.State = "archiving"
		room.Phase = "archiving world"
		if err = m.save(ctx, room, rt); err != nil {
			return err
		}
	}
	var snapshot model.Snapshot
	snapshotID := fmt.Sprintf("%s-g%d", room.ID, room.Generation)
	err = m.DB.Get(ctx, "snapshot", snapshotID, &snapshot)
	if err == nil {
		if rt.ArchiveComplete {
			return m.finishStop(ctx, room, rt, &snapshot)
		}
		if rt.Spec == nil {
			return fmt.Errorf("room archive has no generation specification")
		}
		archive, archiveErr := m.Kube.Archive(ctx, kube.ArchiveSpec{Room: room, RoomToken: rt.Spec.RoomToken})
		if archiveErr != nil {
			return archiveErr
		}
		if archive.Complete {
			rt.ArchiveComplete = true
			if err = m.save(ctx, room, rt); err != nil {
				return err
			}
			return m.finishStop(ctx, room, rt, &snapshot)
		}
		return nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if rt.Spec == nil {
		return fmt.Errorf("room archive has no generation specification")
	}
	archive, err := m.Kube.Archive(ctx, kube.ArchiveSpec{Room: room, RoomToken: rt.Spec.RoomToken})
	if err != nil {
		return err
	}
	if archive.WorldMissing {
		rt.WorldExists = false
		return m.finishStop(ctx, room, rt, nil)
	}
	if archive.Failed {
		room.LastError = model.Failure("archive_failed", archive.Message)
		room.Phase = "archive failed"
		return m.save(ctx, room, rt)
	}
	return nil
}

func (m *Manager) finishStop(ctx context.Context, room model.Room, rt Runtime, snapshot *model.Snapshot) error {
	if snapshot != nil {
		room.LastSnapshot = &snapshot.CapturedAt
		if room.Kind != "branch" {
			if err := m.Kube.DeleteArchivedRoom(ctx, room, *snapshot); err != nil {
				return err
			}
			rt.WorldExists = false
		}
	}
	room.State = "closed"
	if room.Kind == "branch" && room.Label == "testing" {
		room.State = "stopped"
	}
	room.Phase = room.State
	room.Players = []model.Player{}
	room.IdleSince = nil
	room.LastError = nil
	rt.Spec = nil
	rt.StartingAt = nil
	rt.PodNotReadySince, rt.NodeMissingSince, rt.LastProbe = nil, nil, nil
	rt.Failure, rt.LostFrom = nil, ""
	rt.Online = 0
	restart := rt.Restart && room.Label == "testing"
	rt.Restart = restart
	if err := m.save(ctx, room, rt); err != nil {
		return err
	}
	if restart && room.Label == "testing" {
		_, _, err := m.start(ctx, room.ID, nil, auth.Identity{User: "controller"})
		return err
	}
	return nil
}

func (m *Manager) Nodes(ctx context.Context) ([]model.Node, error) {
	nodes, err := m.Kube.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	rooms, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range nodes {
		nodes[i].MaxRunning = m.Cfg.Rooms.MaxRunning[nodes[i].Name]
		nodes[i].Rooms = []string{}
		for _, room := range rooms {
			if room.Node != nil && *room.Node == nodes[i].Name && room.State != "closed" {
				nodes[i].Rooms = append(nodes[i].Rooms, room.ID)
			}
		}
	}
	return nodes, nil
}
