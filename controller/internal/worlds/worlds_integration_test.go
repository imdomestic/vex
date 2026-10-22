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
	"github.com/imdomestic/vex/controller/internal/testproject"
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

func TestRealWorldPublicationAndPersistentArchiveQueue(t *testing.T) {
	scanner := os.Getenv("VEX_IT_SCANNER")
	if scanner == "" {
		t.Skip("requires the Vex world scanner")
	}
	ctx := context.Background()
	fixture := testproject.NewFixture(t, "world-test")
	policy := ManifestPolicy{SHA: fixture.Build.SHA, Document: *fixture.Build.Manifest, Commit: fixture.Build.Commit}
	root := t.TempDir()
	db := fixture.DB
	cfg := fixture.Config
	cfg.Worlds = config.EffectiveWorlds{Worlds: config.Worlds{ScannerFile: scanner}}
	m, err := New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	world := testfixture.WorldDirectory(t, "world-26.3.tar.gz", filepath.Join(root, "source"))
	archive := filepath.Join(root, "fixture.tar.gz")
	realWorldArchive(t, world, archive, gzip.BestSpeed)
	metadata := model.WorldMetadata{ID: "fixture", Title: "Vex 固定测试世界", Minecraft: "26.3", Requires: model.Requirements{Loaders: []string{"fabric"}, Namespaces: []string{}}, Source: model.Source{Scale: 1}}
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
	_, _, err = m.SetState(ctx, "fixture", "archived")
	var failure *model.Error
	if !errors.As(err, &failure) || failure.Code != "archive_failed" {
		t.Fatalf("unverified archive accepted: %v", err)
	}
	published, accepted, err := m.RequestRestore(ctx, "fixture")
	if err != nil || accepted || published.State != "active" {
		t.Fatalf("local active restore: %+v %t %v", published, accepted, err)
	}
	metadata.Requires.Namespaces = []string{"missing-runtime"}
	_, _, err = m.Publish(ctx, metadata, archive, policy)
	if !errors.As(err, &failure) || failure.Code != "invalid_request" {
		t.Fatalf("inaccurate namespaces accepted: %v", err)
	}
	metadata.Requires.Namespaces = []string{}
	secondArchive := filepath.Join(root, "fixture-compressed.tar.gz")
	realWorldArchive(t, world, secondArchive, gzip.BestCompression)
	second, created, err := m.Publish(ctx, metadata, secondArchive, policy)
	if err != nil || !created || second.Rev != "2" || second.World.SHA256 == first.World.SHA256 {
		t.Fatalf("new archive revision: %+v %t %v", second, created, err)
	}
	file, err := m.OpenWorld(ctx, "fixture", "1")
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
	t.Logf("fixed test world: revisions 1/2, idempotence, namespace scan, immutable downloads, persistent archive queue and local restore verified")
}
