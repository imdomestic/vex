package rooms

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/manifest"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
	"github.com/pelletier/go-toml/v2"
)

func manifestMigrationManager(t *testing.T) (*Manager, string, string) {
	t.Helper()
	m, source, _ := freshnessManager(t)
	m.Cfg.Git.DefaultBranch = "main"
	m.Cfg.Runtimes = map[string]config.JavaRuntime{"java25": {}}
	document := manifest.Document{Schema: 1, Minecraft: manifest.Minecraft{Version: "26.3", Java: 25}, Loaders: map[string]manifest.Loader{"fabric": {LoaderVersion: "0.19.5", Files: []manifest.File{{Glob: "build/libs/*.jar", Side: "both"}}}}, World: manifest.World{ExtraRootEntries: []string{"project-state.json"}}}
	commit := func(message string) string {
		t.Helper()
		encoded, err := toml.Marshal(document)
		if err != nil || os.WriteFile(filepath.Join(source, "vex.toml"), encoded, 0600) != nil {
			t.Fatal("cannot write actual Git manifest fixture", err)
		}
		freshnessGit(t, source, "add", "vex.toml")
		freshnessGit(t, source, "commit", "-m", message)
		return freshnessGit(t, source, "rev-parse", "HEAD")
	}
	previous := commit("world manifest with project state")
	document.World.ExtraRootEntries = []string{}
	current := commit("world manifest with standard root entries")
	if err := m.Git.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	var err error
	m.Builds, err = build.New(m.Cfg, m.DB, m.Git)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(source, source+"-offline"); err != nil {
		t.Fatal(err)
	}
	return m, previous, current
}

func TestWorldManifestMigrationUsesCachedGitAndPreservesArchiveBytes(t *testing.T) {
	m, previous, current := manifestMigrationManager(t)
	now := time.Now().UTC()
	snapshot := model.Snapshot{Project: m.Cfg.ID, ID: "main-fabric-g1", RoomID: "main-fabric", RoomGeneration: 1, BuildID: BuildID(previous, "fabric"), Minecraft: "26.3", CreatedAt: now, CapturedAt: now, TankVerifiedAt: &now}
	revisions := []model.WorldRevision{
		{Project: m.Cfg.ID, WorldMetadata: model.WorldMetadata{ID: "published", Minecraft: "26.3", Requires: model.Requirements{Loaders: []string{"fabric"}}}, Rev: "1", CreatedAt: now, TankVerifiedAt: &now},
		{Project: m.Cfg.ID, WorldMetadata: model.WorldMetadata{ID: "promoted", Minecraft: "26.3", Requires: model.Requirements{Loaders: []string{"fabric"}}}, Rev: "1", SnapshotID: &snapshot.ID, CreatedAt: now, TankVerifiedAt: &now},
	}
	if err := m.DB.Put(t.Context(), "snapshot", snapshot.ID, snapshot); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, revision := range revisions {
		if err := m.DB.Put(t.Context(), "world_revision", revision.ID+":"+revision.Rev, revision); err != nil {
			t.Fatal(err)
		}
		filename := filepath.Join(m.Cfg.DataDir, "worlds", revision.ID, revision.Rev, "revision.json")
		encoded, err := json.Marshal(map[string]any{"project": revision.Project, "id": revision.ID, "rev": revision.Rev, "minecraft": revision.Minecraft, "createdAt": revision.CreatedAt})
		if err != nil || os.MkdirAll(filepath.Dir(filename), 0700) != nil || os.WriteFile(filename, encoded, 0600) != nil {
			t.Fatal("cannot create immutable revision metadata fixture", err)
		}
		files[filename] = encoded
		digest, size, err := worldarchive.Digest(filename)
		if err != nil {
			t.Fatal(err)
		}
		task := model.Transfer{Project: m.Cfg.ID, ID: model.TransferID("world", revision.ID, revision.Rev, "metadata"), Kind: "world", ObjectID: revision.ID, Revision: revision.Rev, Part: "metadata", Status: "verified", SHA256: digest, Size: size, VerifiedAt: &now}
		if err = m.DB.Put(t.Context(), "transfer", task.ID, task); err != nil {
			t.Fatal(err)
		}
	}
	before, err := store.All[model.Transfer](t.Context(), m.DB, "transfer")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = m.InitializeWorldRecords(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	var migrated model.Snapshot
	if err = m.DB.Get(t.Context(), "snapshot", snapshot.ID, &migrated); err != nil || migrated.ManifestSHA != previous || !slices.Equal(migrated.WorldExtraRootEntries, []string{"project-state.json"}) || migrated.TankVerifiedAt == nil || !migrated.TankVerifiedAt.Equal(now) {
		t.Fatal("evicted build snapshot did not recover its exact original commit policy", err)
	}
	for _, initial := range revisions {
		var revision model.WorldRevision
		if err = m.DB.Get(t.Context(), "world_revision", initial.ID+":"+initial.Rev, &revision); err != nil {
			t.Fatal(err)
		}
		expected := current
		if initial.SnapshotID != nil {
			expected = previous
		}
		if revision.ManifestSHA != expected || revision.ExtraRootEntries == nil || (initial.SnapshotID != nil && !slices.Equal(revision.ExtraRootEntries, migrated.WorldExtraRootEntries)) {
			t.Fatal("world manifest source differs from its publication or promotion origin")
		}
	}
	after, err := store.All[model.Transfer](t.Context(), m.DB, "transfer")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("manifest migration changed verified archive transfers", err)
	}
	for filename, expected := range files {
		actual, err := os.ReadFile(filename)
		if err != nil || !slices.Equal(actual, expected) {
			t.Fatal("manifest migration changed immutable revision metadata bytes", err)
		}
	}
}

func TestWorldManifestMigrationRejectsUnresolvableHistoryAtomically(t *testing.T) {
	m, previous, _ := manifestMigrationManager(t)
	valid := model.Snapshot{Project: m.Cfg.ID, ID: "a-valid", BuildID: BuildID(previous, "fabric"), Minecraft: "26.3"}
	invalid := model.Snapshot{Project: m.Cfg.ID, ID: "z-missing", BuildID: "fabric-000000000000", Minecraft: "26.3"}
	for _, value := range []model.Snapshot{valid, invalid} {
		if err := m.DB.Put(t.Context(), "snapshot", value.ID, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.InitializeWorldRecords(t.Context()); err == nil {
		t.Fatal("missing historical commit accepted during world migration")
	}
	var unchanged model.Snapshot
	if err := m.DB.Get(t.Context(), "snapshot", valid.ID, &unchanged); err != nil || !reflect.DeepEqual(unchanged, valid) {
		t.Fatal("failed manifest migration partially updated another snapshot", err)
	}
	if _, err := m.Git.ResolveCommitPrefix(t.Context(), "--all"); err == nil {
		t.Fatal("commit resolver accepted a Git option")
	}
}

func TestEmptyWorldMigrationDoesNotRequireGitOrDefaultBranch(t *testing.T) {
	m, _, _ := freshnessManager(t)
	m.Git, m.Builds = nil, nil
	if err := m.InitializeWorldRecords(t.Context()); err != nil {
		t.Fatal(err)
	}
}
