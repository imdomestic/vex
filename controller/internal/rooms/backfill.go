package rooms

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	buildmetadata "github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/manifest"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/worlds"
)

// setCommit 在房间提交改变时写入提交元数据，优先使用同一提交的构建记录。
func (m *Manager) setCommit(ctx context.Context, room *model.Room, build *model.Build) error {
	if room.Commit != nil && room.Commit.SHA == room.SHA {
		return nil
	}
	room.CommitMissingAt = nil
	if build != nil && build.SHA == room.SHA && build.Commit != nil {
		room.Commit = build.Commit
		return nil
	}
	info, err := m.Git.CommitInfo(ctx, room.SHA)
	if err != nil {
		return err
	}
	room.Commit = &info
	return nil
}

// historyReader 在补录期间缓存从镜像读取的提交元数据与清单。
type historyReader struct {
	m         *Manager
	commits   map[string]model.CommitInfo
	documents map[string]manifest.Document
}

func (h *historyReader) commit(ctx context.Context, sha string) (*model.CommitInfo, string, error) {
	if value, ok := h.commits[sha]; ok {
		return &value, "", nil
	}
	value, err := h.m.Git.CommitInfo(ctx, sha)
	if err != nil {
		issue, err := buildmetadata.RecordIssue(ctx, err)
		return nil, issue, err
	}
	h.commits[sha] = value
	return &value, "", nil
}

func (h *historyReader) document(ctx context.Context, sha string) (*manifest.Document, string, error) {
	if value, ok := h.documents[sha]; ok {
		return &value, "", nil
	}
	value, err := h.m.Builds.Manifest(ctx, sha)
	if err != nil {
		issue, err := buildmetadata.RecordIssue(ctx, err)
		return nil, issue, err
	}
	h.documents[sha] = value
	return &value, "", nil
}

func sameEntries(left, right []string) bool {
	left, right = slices.Clone(left), slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

// Backfill 从记录或镜像补录快照、世界 revision 与房间缺少的提交元数据和清单。
// 无法取得内容的记录写入返回的列表；已有内容只读取不改写。
func (m *Manager) Backfill(ctx context.Context) ([]model.RecordIssue, error) {
	snapshots, err := store.All[model.Snapshot](ctx, m.DB, "snapshot")
	if err != nil {
		return nil, err
	}
	revisions, err := store.All[model.WorldRevision](ctx, m.DB, "world_revision")
	if err != nil {
		return nil, err
	}
	rooms, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	builds := map[string]model.Build{}
	values, err := store.All[model.Build](ctx, m.DB, "build")
	if err != nil {
		return nil, err
	}
	for _, value := range values {
		builds[value.ID] = value
	}
	reader := &historyReader{m: m, commits: map[string]model.CommitInfo{}, documents: map[string]manifest.Document{}}
	issues := []model.RecordIssue{}
	snapshotUpdates := map[string]model.Snapshot{}
	snapshotByID := map[string]model.Snapshot{}
	for _, snapshot := range snapshots {
		if snapshot.Project != m.Cfg.ID {
			return nil, fmt.Errorf("snapshot backfill found a different project")
		}
		updated, issue, err := m.backfillSnapshot(ctx, reader, snapshot, builds)
		if err != nil {
			return nil, fmt.Errorf("backfill snapshot %s: %w", snapshot.ID, err)
		}
		if issue != "" {
			issues = append(issues, model.RecordIssue{Kind: "snapshot", ID: snapshot.ID, SHA: updated.ManifestSHA, Reason: issue})
		}
		if !snapshotEqual(updated, snapshot) {
			snapshotUpdates[snapshot.ID] = updated
		}
		snapshotByID[snapshot.ID] = updated
	}
	revisionUpdates := map[string]model.WorldRevision{}
	var defaultPolicy *worlds.ManifestPolicy
	for _, revision := range revisions {
		if revision.Project != m.Cfg.ID {
			return nil, fmt.Errorf("world revision backfill found a different project")
		}
		updated, issue, err := m.backfillRevision(ctx, reader, revision, snapshotByID, &defaultPolicy)
		if err != nil {
			return nil, fmt.Errorf("backfill world revision %s:%s: %w", revision.ID, revision.Rev, err)
		}
		if issue != "" {
			issues = append(issues, model.RecordIssue{Kind: "world_revision", ID: revision.ID + ":" + revision.Rev, SHA: updated.ManifestSHA, Reason: issue})
		}
		if !revisionEqual(updated, revision) {
			revisionUpdates[revision.ID+":"+revision.Rev] = updated
		}
	}
	roomUpdates := map[string]model.Room{}
	for _, room := range rooms {
		if room.Commit != nil || room.SHA == "" {
			continue
		}
		build := builds[room.BuildID]
		if build.SHA == room.SHA && build.Commit != nil {
			room.Commit = build.Commit
		} else {
			commit, issue, err := reader.commit(ctx, room.SHA)
			if err != nil {
				return nil, fmt.Errorf("backfill room %s: %w", room.ID, err)
			}
			if issue != "" {
				issues = append(issues, model.RecordIssue{Kind: "room", ID: room.ID, SHA: room.SHA, Reason: issue})
				continue
			}
			room.Commit = commit
		}
		roomUpdates[room.ID] = room
	}
	if len(snapshotUpdates) == 0 && len(revisionUpdates) == 0 && len(roomUpdates) == 0 {
		return issues, nil
	}
	err = m.DB.Update(ctx, func(tx *store.Tx) error {
		for id, updated := range snapshotUpdates {
			var current model.Snapshot
			if err := tx.Get(ctx, "snapshot", id, &current); err != nil {
				return err
			}
			if current.BuildID != updated.BuildID || current.SHA256 != updated.SHA256 || current.RoomGeneration != updated.RoomGeneration || current.ManifestSHA != "" && current.ManifestSHA != updated.ManifestSHA {
				return fmt.Errorf("snapshot changed during backfill")
			}
			if current.ManifestSHA == "" {
				current.ManifestSHA = updated.ManifestSHA
			}
			if current.WorldExtraRootEntries == nil {
				current.WorldExtraRootEntries = updated.WorldExtraRootEntries
			}
			if current.Commit == nil {
				current.Commit = updated.Commit
			}
			if current.Manifest == nil {
				current.Manifest = updated.Manifest
			}
			if err := tx.Put(ctx, "snapshot", id, current); err != nil {
				return err
			}
		}
		for key, updated := range revisionUpdates {
			var current model.WorldRevision
			if err := tx.Get(ctx, "world_revision", key, &current); err != nil {
				return err
			}
			if current.World.SHA256 != updated.World.SHA256 || current.ManifestSHA != "" && current.ManifestSHA != updated.ManifestSHA {
				return fmt.Errorf("world revision changed during backfill")
			}
			if current.ManifestSHA == "" {
				current.ManifestSHA = updated.ManifestSHA
			}
			if current.ExtraRootEntries == nil {
				current.ExtraRootEntries = updated.ExtraRootEntries
			}
			if current.Commit == nil {
				current.Commit = updated.Commit
			}
			if current.Manifest == nil {
				current.Manifest = updated.Manifest
			}
			if err := tx.Put(ctx, "world_revision", key, current); err != nil {
				return err
			}
		}
		for id, updated := range roomUpdates {
			var current model.Room
			if err := tx.Get(ctx, "room", id, &current); err != nil {
				return err
			}
			// 房间提交在补录期间改变时，由改变提交的流程写入新的元数据。
			if current.SHA != updated.SHA || current.Commit != nil {
				continue
			}
			current.Commit = updated.Commit
			if err := tx.Put(ctx, "room", id, current); err != nil {
				return err
			}
		}
		return nil
	})
	return issues, err
}

func snapshotEqual(left, right model.Snapshot) bool {
	return left.ManifestSHA == right.ManifestSHA && (left.WorldExtraRootEntries == nil) == (right.WorldExtraRootEntries == nil) && left.Commit == right.Commit && left.Manifest == right.Manifest
}

func revisionEqual(left, right model.WorldRevision) bool {
	return left.ManifestSHA == right.ManifestSHA && (left.ExtraRootEntries == nil) == (right.ExtraRootEntries == nil) && left.Commit == right.Commit && left.Manifest == right.Manifest
}

func (m *Manager) backfillSnapshot(ctx context.Context, reader *historyReader, snapshot model.Snapshot, builds map[string]model.Build) (model.Snapshot, string, error) {
	if snapshot.ManifestSHA != "" && snapshot.WorldExtraRootEntries != nil && snapshot.Commit != nil && snapshot.Manifest != nil {
		return snapshot, "", nil
	}
	build, recorded := builds[snapshot.BuildID]
	if snapshot.ManifestSHA == "" {
		if recorded {
			if build.Project != m.Cfg.ID || !ValidSHA(build.SHA) {
				return snapshot, "", fmt.Errorf("snapshot build identity differs during backfill")
			}
			snapshot.ManifestSHA = build.SHA
		} else {
			loader, prefix, ok := strings.Cut(snapshot.BuildID, "-")
			if !ok || loader != "fabric" && loader != "neoforge" {
				return snapshot, "", fmt.Errorf("snapshot references an invalid build ID")
			}
			sha, err := m.Git.ResolveCommitPrefix(ctx, prefix)
			if err != nil {
				return snapshot, "commit_unavailable", nil
			}
			snapshot.ManifestSHA = sha
		}
	}
	fromBuild := recorded && build.SHA == snapshot.ManifestSHA
	if snapshot.WorldExtraRootEntries == nil || snapshot.Manifest == nil {
		var document *manifest.Document
		if fromBuild && build.Manifest != nil {
			document = build.Manifest
		} else {
			value, issue, err := reader.document(ctx, snapshot.ManifestSHA)
			if err != nil || issue != "" {
				return snapshot, issue, err
			}
			document = value
		}
		if document.Minecraft.Version != snapshot.Minecraft {
			return snapshot, "manifest_differs: snapshot Minecraft version differs from its commit manifest", nil
		}
		if snapshot.WorldExtraRootEntries != nil && !sameEntries(snapshot.WorldExtraRootEntries, document.World.ExtraRootEntries) {
			return snapshot, "manifest_differs: snapshot root entries differ from its commit manifest", nil
		}
		if snapshot.WorldExtraRootEntries == nil {
			snapshot.WorldExtraRootEntries = append([]string{}, document.World.ExtraRootEntries...)
		}
		snapshot.Manifest = document
	}
	if snapshot.Commit == nil {
		if fromBuild && build.Commit != nil {
			snapshot.Commit = build.Commit
		} else {
			value, issue, err := reader.commit(ctx, snapshot.ManifestSHA)
			if err != nil || issue != "" {
				return snapshot, issue, err
			}
			snapshot.Commit = value
		}
	}
	return snapshot, "", nil
}

func (m *Manager) backfillRevision(ctx context.Context, reader *historyReader, revision model.WorldRevision, snapshots map[string]model.Snapshot, defaultPolicy **worlds.ManifestPolicy) (model.WorldRevision, string, error) {
	if revision.ManifestSHA != "" && revision.ExtraRootEntries != nil && revision.Commit != nil && revision.Manifest != nil {
		return revision, "", nil
	}
	var origin *model.Snapshot
	if revision.SnapshotID != nil {
		snapshot, ok := snapshots[*revision.SnapshotID]
		if !ok || snapshot.ManifestSHA == "" {
			return revision, "", fmt.Errorf("promoted world revision has no authoritative snapshot manifest")
		}
		origin = &snapshot
	}
	var document *manifest.Document
	var commit *model.CommitInfo
	if revision.ManifestSHA == "" {
		if origin != nil {
			revision.ManifestSHA = origin.ManifestSHA
		} else {
			// 旧版上传没有记录清单来源，按上传规则使用默认分支当前提交。
			if *defaultPolicy == nil {
				policy, err := m.DefaultWorldManifest(ctx)
				if err != nil {
					return revision, "", err
				}
				*defaultPolicy = &policy
			}
			revision.ManifestSHA = (*defaultPolicy).SHA
			document, commit = &(*defaultPolicy).Document, (*defaultPolicy).Commit
		}
	}
	if origin != nil && origin.ManifestSHA == revision.ManifestSHA {
		if document == nil {
			document = origin.Manifest
		}
		if commit == nil {
			commit = origin.Commit
		}
	}
	if revision.ExtraRootEntries == nil || revision.Manifest == nil {
		if document == nil {
			value, issue, err := reader.document(ctx, revision.ManifestSHA)
			if err != nil || issue != "" {
				return revision, issue, err
			}
			document = value
		}
		if revision.Minecraft != document.Minecraft.Version {
			return revision, "manifest_differs: world revision Minecraft version differs from its manifest", nil
		}
		for _, loader := range revision.Requires.Loaders {
			if _, ok := document.Loaders[loader]; !ok {
				return revision, "manifest_differs: world revision loader is absent from its manifest", nil
			}
		}
		if revision.ExtraRootEntries != nil && !sameEntries(revision.ExtraRootEntries, document.World.ExtraRootEntries) {
			return revision, "manifest_differs: world revision root entries differ from its manifest", nil
		}
		if revision.ExtraRootEntries == nil {
			revision.ExtraRootEntries = append([]string{}, document.World.ExtraRootEntries...)
		}
		revision.Manifest = document
	}
	if revision.Commit == nil {
		if commit == nil {
			value, issue, err := reader.commit(ctx, revision.ManifestSHA)
			if err != nil || issue != "" {
				return revision, issue, err
			}
			commit = value
		}
		revision.Commit = commit
	}
	return revision, "", nil
}

// MarkMissingCommits 根据镜像现有的全部 ref，标注或清除记录的“提交已不在仓库中”时间。
func (m *Manager) MarkMissingCommits(ctx context.Context) error {
	reachable, err := m.Git.Reachable(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	mark := func(current **time.Time, sha string) bool {
		missing := sha != "" && !reachable[sha]
		if missing && *current == nil {
			*current = &now
			return true
		}
		if !missing && *current != nil {
			*current = nil
			return true
		}
		return false
	}
	return m.DB.Update(ctx, func(tx *store.Tx) error {
		builds, err := store.All[model.Build](ctx, tx, "build")
		if err != nil {
			return err
		}
		for _, value := range builds {
			if mark(&value.CommitMissingAt, value.SHA) {
				if err = tx.Put(ctx, "build", value.ID, value); err != nil {
					return err
				}
			}
		}
		rooms, err := store.All[model.Room](ctx, tx, "room")
		if err != nil {
			return err
		}
		for _, value := range rooms {
			if mark(&value.CommitMissingAt, value.SHA) {
				if err = tx.Put(ctx, "room", value.ID, value); err != nil {
					return err
				}
			}
		}
		snapshots, err := store.All[model.Snapshot](ctx, tx, "snapshot")
		if err != nil {
			return err
		}
		for _, value := range snapshots {
			if mark(&value.CommitMissingAt, value.ManifestSHA) {
				if err = tx.Put(ctx, "snapshot", value.ID, value); err != nil {
					return err
				}
			}
		}
		revisions, err := store.All[model.WorldRevision](ctx, tx, "world_revision")
		if err != nil {
			return err
		}
		for _, value := range revisions {
			if mark(&value.CommitMissingAt, value.ManifestSHA) {
				if err = tx.Put(ctx, "world_revision", value.ID+":"+value.Rev, value); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
