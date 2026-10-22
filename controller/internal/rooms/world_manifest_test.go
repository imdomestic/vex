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

func TestRecordBackfillUsesCachedGitAndPreservesArchiveBytes(t *testing.T) {
	m, previous, current := manifestMigrationManager(t)
	room, err := m.Get(t.Context(), "main-fabric")
	if err != nil || room.Commit == nil || room.Commit.SHA != room.SHA || room.Commit.Subject != "初始分支" {
		t.Fatal("branch room lacks the metadata of its registered commit", room.Commit, err)
	}
	branchCommit := *room.Commit
	room.Commit = nil
	if err = m.DB.Put(t.Context(), "room", room.ID, room); err != nil {
		t.Fatal(err)
	}
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
		issues, err := m.Backfill(t.Context())
		if err != nil || len(issues) != 0 {
			t.Fatal(issues, err)
		}
	}
	var migrated model.Snapshot
	if err = m.DB.Get(t.Context(), "snapshot", snapshot.ID, &migrated); err != nil || migrated.ManifestSHA != previous || !slices.Equal(migrated.WorldExtraRootEntries, []string{"project-state.json"}) || migrated.TankVerifiedAt == nil || !migrated.TankVerifiedAt.Equal(now) {
		t.Fatal("evicted build snapshot did not recover its exact original commit policy", err)
	}
	if migrated.Commit == nil || migrated.Commit.SHA != previous || migrated.Commit.Subject != "world manifest with project state" || migrated.Manifest == nil || !slices.Equal(migrated.Manifest.World.ExtraRootEntries, []string{"project-state.json"}) {
		t.Fatal("snapshot did not record its commit metadata and manifest", migrated.Commit)
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
		if revision.Commit == nil || revision.Commit.SHA != expected || revision.Manifest == nil || !slices.Equal(revision.Manifest.World.ExtraRootEntries, revision.ExtraRootEntries) {
			t.Fatal("world revision did not record its commit metadata and manifest", revision.Commit)
		}
	}
	if room, err = m.Get(t.Context(), "main-fabric"); err != nil || room.Commit == nil || !reflect.DeepEqual(*room.Commit, branchCommit) {
		t.Fatal("room commit metadata was not backfilled", room.Commit, err)
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

func TestRecordBackfillListsUnresolvableHistoryAndCompletesOthers(t *testing.T) {
	m, previous, _ := manifestMigrationManager(t)
	valid := model.Snapshot{Project: m.Cfg.ID, ID: "a-valid", BuildID: BuildID(previous, "fabric"), Minecraft: "26.3"}
	invalid := model.Snapshot{Project: m.Cfg.ID, ID: "z-missing", BuildID: "fabric-000000000000", Minecraft: "26.3"}
	for _, value := range []model.Snapshot{valid, invalid} {
		if err := m.DB.Put(t.Context(), "snapshot", value.ID, value); err != nil {
			t.Fatal(err)
		}
	}
	issues, err := m.Backfill(t.Context())
	if err != nil || len(issues) != 1 || issues[0].Kind != "snapshot" || issues[0].ID != invalid.ID || issues[0].Reason != "commit_unavailable" {
		t.Fatal("unresolvable historical commit was not listed", issues, err)
	}
	var completed model.Snapshot
	if err = m.DB.Get(t.Context(), "snapshot", valid.ID, &completed); err != nil || completed.ManifestSHA != previous || completed.Commit == nil || completed.Manifest == nil {
		t.Fatal("independent snapshot was not backfilled", completed, err)
	}
	var unchanged model.Snapshot
	if err = m.DB.Get(t.Context(), "snapshot", invalid.ID, &unchanged); err != nil || !reflect.DeepEqual(unchanged, invalid) {
		t.Fatal("unresolvable snapshot was changed", err)
	}
	if _, err = m.Git.ResolveCommitPrefix(t.Context(), "--all"); err == nil {
		t.Fatal("commit resolver accepted a Git option")
	}
}

func TestEmptyRecordBackfillDoesNotRequireGitOrDefaultBranch(t *testing.T) {
	m, _, _ := freshnessManager(t)
	rooms, err := m.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, room := range rooms {
		if err = m.DB.Delete(t.Context(), "room", room.ID); err != nil {
			t.Fatal(err)
		}
	}
	m.Git, m.Builds = nil, nil
	if issues, err := m.Backfill(t.Context()); err != nil || len(issues) != 0 {
		t.Fatal(issues, err)
	}
}
