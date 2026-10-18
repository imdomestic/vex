//go:build integration

package worlds

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/testfixture"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
)

func realWorldArchive(t *testing.T, source, destination string, compression int) {
	t.Helper()
	file, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewWriterLevel(file, compression)
	if err != nil {
		t.Fatal(err)
	}
	bundle := tar.NewWriter(gz)
	err = filepath.WalkDir(source, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			t.Fatal("real world contains an unsupported special file")
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, filename)
		if err != nil {
			return err
		}
		header.Name = "world"
		if rel != "." {
			header.Name += "/" + filepath.ToSlash(rel)
		}
		if err = bundle.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		input, err := os.Open(filename)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(bundle, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = bundle.Close(); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRealPlazaPublicationAndPersistentArchiveQueue(t *testing.T) {
	world, scanner, commit := os.Getenv("VEX_IT_WORLD"), os.Getenv("VEX_IT_SCANNER"), os.Getenv("VEX_IT_SOURCE_SHA")
	if world == "" || scanner == "" || commit == "" {
		t.Skip("requires real world, scanner and source SHA environment variables")
	}
	ctx := context.Background()
	document, manifestSHA := testfixture.Manifest(t)
	policy := ManifestPolicy{SHA: manifestSHA, Document: document}
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db, err = db.WithProject("world-test")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.ProjectConfig{ID: "world-test", DataDir: filepath.Join(root, "data"), Minecraft: config.ProjectMinecraft{Target: config.Target{Version: "26.3"}}, Loaders: map[string]config.Loader{"fabric": {Enabled: true}}, Worlds: config.EffectiveWorlds{Worlds: config.Worlds{ScannerFile: scanner}}}
	m, err := New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "plaza.tar.gz")
	realWorldArchive(t, world, archive, gzip.BestSpeed)
	metadata := model.WorldMetadata{ID: "plaza", Title: "高塔 · 中庭广场样板", Minecraft: "26.3", Requires: model.Requirements{Loaders: []string{"fabric"}, Namespaces: []string{}}, Source: model.Source{Repo: "encore:/home/linwhite/d2-map-dev", Commit: commit, Scale: 1}}
	first, created, err := m.Publish(ctx, metadata, archive, policy)
	if err != nil || !created || first.Rev != "1" {
		t.Fatalf("first publish: %+v %t %v", first, created, err)
	}
	metadata.Title = "幂等请求中的标题"
	again, created, err := m.Publish(ctx, metadata, archive, policy)
	if err != nil || created || !reflect.DeepEqual(first, again) {
		t.Fatalf("idempotent publish changed revision: %+v %t %v", again, created, err)
	}
	transfers, err := store.All[model.Transfer](ctx, db, "transfer")
	if err != nil || len(transfers) != 2 {
		t.Fatalf("persistent publication tasks: %+v %v", transfers, err)
	}
	for _, task := range transfers {
		if task.Status != "queued" || task.Attempt != 0 || filepath.IsAbs(task.LocalPath) || task.SHA256 == "" {
			t.Fatalf("invalid transfer task: %+v", task)
		}
	}
	_, _, err = m.SetState(ctx, "plaza", "archived")
	var failure *model.Error
	if !errors.As(err, &failure) || failure.Code != "archive_failed" {
		t.Fatalf("unverified archive accepted: %v", err)
	}
	published, accepted, err := m.RequestRestore(ctx, "plaza")
	if err != nil || accepted || published.State != "active" {
		t.Fatalf("local active restore: %+v %t %v", published, accepted, err)
	}
	metadata.Requires.Namespaces = []string{"missing-runtime"}
	_, _, err = m.Publish(ctx, metadata, archive, policy)
	if !errors.As(err, &failure) || failure.Code != "invalid_request" {
		t.Fatalf("inaccurate namespaces accepted: %v", err)
	}
	metadata.Requires.Namespaces = []string{}
	secondArchive := filepath.Join(root, "plaza-compressed.tar.gz")
	realWorldArchive(t, world, secondArchive, gzip.BestCompression)
	second, created, err := m.Publish(ctx, metadata, secondArchive, policy)
	if err != nil || !created || second.Rev != "2" || second.World.SHA256 == first.World.SHA256 {
		t.Fatalf("new archive revision: %+v %t %v", second, created, err)
	}
	file, err := m.OpenWorld(ctx, "plaza", "1")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	digest, size, err := worldarchive.Digest(archive)
	if err != nil || digest != first.World.SHA256 || size != first.World.Size {
		t.Fatalf("original upload changed: %s %d %v", digest, size, err)
	}
	if _, err = os.Stat(filepath.Join(world, "level.dat")); err != nil {
		t.Fatal("source world changed", err)
	}
	t.Logf("real plaza: revisions 1/2, idempotence, namespace scan, immutable downloads, persistent archive queue and local restore verified")
}
