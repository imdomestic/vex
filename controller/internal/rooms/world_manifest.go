package rooms

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/imdomestic/vex/controller/internal/manifest"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/worlds"
)

func (m *Manager) DefaultWorldManifest(ctx context.Context) (worlds.ManifestPolicy, error) {
	refs, err := m.Git.Branches(ctx)
	if err != nil {
		return worlds.ManifestPolicy{}, err
	}
	for _, ref := range refs {
		if ref.Name == m.Cfg.Git.DefaultBranch {
			document, err := m.Builds.Manifest(ctx, ref.Head)
			return worlds.ManifestPolicy{SHA: ref.Head, Document: document}, err
		}
	}
	return worlds.ManifestPolicy{}, model.Failure("conflict", "默认分支的提交清单尚未同步")
}

func (m *Manager) InitializeWorldRecords(ctx context.Context) error {
	snapshots, err := store.All[model.Snapshot](ctx, m.DB, "snapshot")
	if err != nil {
		return err
	}
	revisions, err := store.All[model.WorldRevision](ctx, m.DB, "world_revision")
	if err != nil {
		return err
	}
	snapshotUpdates := map[string]model.Snapshot{}
	revisionUpdates := map[string]model.WorldRevision{}
	snapshotByID := map[string]model.Snapshot{}
	documents := map[string]manifest.Document{}
	readManifest := func(sha string) (manifest.Document, error) {
		if document, ok := documents[sha]; ok {
			return document, nil
		}
		document, err := m.Builds.Manifest(ctx, sha)
		if err == nil {
			documents[sha] = document
		}
		return document, err
	}
	for _, snapshot := range snapshots {
		if snapshot.Project != m.Cfg.ID {
			return fmt.Errorf("snapshot manifest initialization found a different project")
		}
		if snapshot.ManifestSHA == "" || snapshot.WorldExtraRootEntries == nil {
			sha := snapshot.ManifestSHA
			if sha == "" {
				var value model.Build
				err = m.DB.Get(ctx, "build", snapshot.BuildID, &value)
				if err == nil {
					if value.Project != m.Cfg.ID || value.ID != snapshot.BuildID || !ValidSHA(value.SHA) {
						return fmt.Errorf("snapshot build identity differs during manifest initialization")
					}
					sha = value.SHA
				} else if errors.Is(err, store.ErrNotFound) {
					loader, prefix, ok := strings.Cut(snapshot.BuildID, "-")
					if !ok || loader != "fabric" && loader != "neoforge" {
						return fmt.Errorf("snapshot references an invalid build ID")
					}
					sha, err = m.Git.ResolveCommitPrefix(ctx, prefix)
					if err != nil {
						return err
					}
				} else {
					return err
				}
			}
			document, err := readManifest(sha)
			if err != nil {
				return err
			}
			if document.Minecraft.Version != snapshot.Minecraft {
				return fmt.Errorf("snapshot Minecraft version differs from its commit manifest")
			}
			snapshot.ManifestSHA, snapshot.WorldExtraRootEntries = sha, append([]string{}, document.World.ExtraRootEntries...)
			snapshotUpdates[snapshot.ID] = snapshot
		}
		snapshotByID[snapshot.ID] = snapshot
	}
	var defaultPolicy *worlds.ManifestPolicy
	for _, revision := range revisions {
		if revision.Project != m.Cfg.ID {
			return fmt.Errorf("world manifest initialization found a different project")
		}
		if revision.ManifestSHA != "" && revision.ExtraRootEntries != nil {
			continue
		}
		sha := revision.ManifestSHA
		if sha == "" && revision.SnapshotID != nil {
			snapshot, ok := snapshotByID[*revision.SnapshotID]
			if !ok || snapshot.ManifestSHA == "" {
				return fmt.Errorf("promoted world revision has no authoritative snapshot manifest")
			}
			sha = snapshot.ManifestSHA
		}
		var document manifest.Document
		if sha == "" {
			if defaultPolicy == nil {
				policy, err := m.DefaultWorldManifest(ctx)
				if err != nil {
					return err
				}
				defaultPolicy = &policy
			}
			sha, document = defaultPolicy.SHA, defaultPolicy.Document
		} else {
			document, err = readManifest(sha)
			if err != nil {
				return err
			}
		}
		if revision.Minecraft != document.Minecraft.Version {
			return fmt.Errorf("world revision Minecraft version differs from its manifest")
		}
		for _, loader := range revision.Requires.Loaders {
			if _, ok := document.Loaders[loader]; !ok {
				return fmt.Errorf("world revision loader is absent from its manifest")
			}
		}
		revision.ManifestSHA, revision.ExtraRootEntries = sha, append([]string{}, document.World.ExtraRootEntries...)
		revisionUpdates[revision.ID+":"+revision.Rev] = revision
	}
	if len(snapshotUpdates) == 0 && len(revisionUpdates) == 0 {
		return nil
	}
	return m.DB.Update(ctx, func(tx *store.Tx) error {
		for _, previous := range snapshots {
			if updated, ok := snapshotUpdates[previous.ID]; ok {
				var current model.Snapshot
				if err := tx.Get(ctx, "snapshot", previous.ID, &current); err != nil {
					return err
				}
				if !reflect.DeepEqual(current, previous) {
					return fmt.Errorf("snapshot changed during manifest initialization")
				}
				if err := tx.Put(ctx, "snapshot", previous.ID, updated); err != nil {
					return err
				}
			}
		}
		for _, previous := range revisions {
			key := previous.ID + ":" + previous.Rev
			if updated, ok := revisionUpdates[key]; ok {
				var current model.WorldRevision
				if err := tx.Get(ctx, "world_revision", key, &current); err != nil {
					return err
				}
				if !reflect.DeepEqual(current, previous) {
					return fmt.Errorf("world revision changed during manifest initialization")
				}
				if err := tx.Put(ctx, "world_revision", key, updated); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
