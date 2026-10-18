//go:build integration

package rooms

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/gitmirror"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
	"github.com/pelletier/go-toml/v2"
)

func TestProductionWorldManifestMigrationOnDatabaseCopy(t *testing.T) {
	database, settings, fallbackDirectory := os.Getenv("VEX_IT_MIGRATION_DB"), os.Getenv("VEX_IT_CONFIG"), os.Getenv("VEX_IT_MANIFEST_FALLBACK_DIR")
	if database == "" || settings == "" || fallbackDirectory == "" {
		t.Skip("requires an explicit production SQLite backup and actual fallback manifests")
	}
	resolved, err := filepath.EvalSymlinks(database)
	if err != nil || resolved != filepath.Clean(database) || !filepath.IsAbs(resolved) || !strings.Contains(resolved, "/.work/") {
		t.Fatal("migration database must be an independent regular file inside .work")
	}
	file, err := os.Open(settings)
	if err != nil {
		t.Fatal("cannot read actual operator configuration")
	}
	// 只读取迁移所需的非凭据字段，旧配置由正式迁移处理。
	var operator struct {
		Server struct {
			DataDir string `toml:"data_dir"`
		} `toml:"server"`
		Runtimes map[string]config.JavaRuntime `toml:"runtimes"`
		Projects map[string]struct {
			Remote        string                   `toml:"remote"`
			DefaultBranch string                   `toml:"default_branch"`
			Minecraft     config.Target            `toml:"minecraft"`
			Loaders       map[string]config.Loader `toml:"loaders"`
			Quota         config.Quota             `toml:"quota"`
		} `toml:"projects"`
	}
	decodeErr := toml.NewDecoder(file).Decode(&operator)
	if err = file.Close(); decodeErr != nil || err != nil || !filepath.IsAbs(operator.Server.DataDir) {
		t.Fatal("operator migration fields could not be read")
	}
	db, err := store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for id, project := range operator.Projects {
		if !config.ValidID(id) {
			t.Fatal("actual project ID is invalid")
		}
		sourceDir := filepath.Join(operator.Server.DataDir, "projects", id)
		mirror, err := gitmirror.New(project.Remote, filepath.Join(sourceDir, "git.git"))
		if err != nil {
			t.Fatal(err)
		}
		scoped, err := db.WithProject(id)
		if err != nil {
			t.Fatal(err)
		}
		cfg := config.ProjectConfig{ID: id, DataDir: filepath.Join(filepath.Dir(database), "migration-work", id), Git: config.Git{Remote: project.Remote, DefaultBranch: project.DefaultBranch}, Minecraft: config.ProjectMinecraft{Target: project.Minecraft}, Loaders: project.Loaders, Quota: project.Quota, Runtimes: operator.Runtimes, ManifestFallback: filepath.Join(fallbackDirectory, id+".toml")}
		builder, err := build.New(cfg, scoped, mirror)
		if err != nil {
			t.Fatal(err)
		}
		before, err := store.All[model.Transfer](t.Context(), scoped, "transfer")
		if err != nil {
			t.Fatal(err)
		}
		files := map[string]string{}
		for _, transfer := range before {
			if transfer.Part != "metadata" {
				continue
			}
			if !filepath.IsLocal(transfer.LocalPath) {
				t.Fatal("actual transfer has a non-local metadata path")
			}
			filename := filepath.Join(sourceDir, filepath.FromSlash(transfer.LocalPath))
			canonical, err := filepath.EvalSymlinks(filename)
			if err != nil || canonical != filename {
				t.Fatal("actual metadata path is missing or symbolic")
			}
			digest, size, err := worldarchive.Digest(filename)
			if err != nil || digest != transfer.SHA256 || size != transfer.Size {
				t.Fatal("actual immutable metadata differs from its recorded archive digest")
			}
			files[filename] = digest
		}
		manager := &Manager{Cfg: cfg, DB: scoped, Git: mirror, Builds: builder}
		for range 2 {
			if err = builder.InitializeRecords(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err = manager.InitializeWorldRecords(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		after, err := store.All[model.Transfer](t.Context(), scoped, "transfer")
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("copy migration changed actual archive transfer identities or checksums")
		}
		for filename, expected := range files {
			digest, _, err := worldarchive.Digest(filename)
			if err != nil || digest != expected {
				t.Fatal("copy migration changed source metadata bytes")
			}
		}
		snapshots, err := store.All[model.Snapshot](t.Context(), scoped, "snapshot")
		if err != nil {
			t.Fatal(err)
		}
		revisions, err := store.All[model.WorldRevision](t.Context(), scoped, "world_revision")
		if err != nil {
			t.Fatal(err)
		}
		for _, snapshot := range snapshots {
			if !ValidSHA(snapshot.ManifestSHA) || snapshot.WorldExtraRootEntries == nil {
				t.Fatal("actual snapshot lacks authoritative world manifest metadata")
			}
		}
		for _, revision := range revisions {
			if !ValidSHA(revision.ManifestSHA) || revision.ExtraRootEntries == nil {
				t.Fatal("actual world revision lacks authoritative manifest metadata")
			}
		}
		report := map[string]any{"project": id, "snapshots": len(snapshots), "worldRevisions": len(revisions), "metadataFilesVerified": len(files), "transfersUnchanged": len(after), "idempotent": true}
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil || os.WriteFile(filepath.Join(filepath.Dir(database), id+"-world-manifest-result.json"), encoded, 0600) != nil {
			t.Fatal("cannot preserve migration evidence")
		}
		t.Logf("project=%s snapshots=%d worldRevisions=%d immutableMetadata=%d transfers=%d; two copy migrations passed", id, len(snapshots), len(revisions), len(files), len(after))
	}
}
