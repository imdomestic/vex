package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/filehash"
	"github.com/imdomestic/vex/controller/internal/model"
)

func TestDirectoryArchivePreservesBytesAndDeterminism(t *testing.T) {
	root := t.TempDir()
	source, scratch := filepath.Join(root, "source"), filepath.Join(root, "scratch")
	if err := os.MkdirAll(filepath.Join(source, "subdirectory"), 0700); err != nil {
		t.Fatal(err)
	}
	content := []byte("archive member content\n")
	input := filepath.Join(source, "subdirectory", "contents.txt")
	if err := os.WriteFile(input, content, 0600); err != nil {
		t.Fatal(err)
	}
	first, err := directoryArchive(source, scratch)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(input, time.Unix(100, 0), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	second, err := directoryArchive(source, scratch)
	if err != nil {
		t.Fatal(err)
	}
	one, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one, two) {
		t.Fatal("same file content produced different publication bytes")
	}
	compressed, err := gzip.NewReader(bytes.NewReader(one))
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	names := []string{}
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
		if header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" {
			t.Fatal("archive disclosed local account ownership")
		}
		if header.Name == "world/subdirectory/contents.txt" {
			actual, err := io.ReadAll(archive)
			if err != nil || !bytes.Equal(actual, content) {
				t.Fatal("archive content differs from source")
			}
		}
	}
	if len(names) != 3 || names[0] != "world" || names[2] != "world/subdirectory/contents.txt" {
		t.Fatalf("unexpected archive members: %v", names)
	}
	if _, err = directoryArchive(source, filepath.Join(source, ".work")); err == nil {
		t.Fatal("archive output inside source accepted")
	}
	alias := filepath.Join(root, "alias")
	if err = os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	if _, err = directoryArchive(source, filepath.Join(alias, "not-created")); err == nil {
		t.Fatal("archive output through source symlink accepted")
	}
	if _, err = os.Stat(filepath.Join(source, "not-created")); !os.IsNotExist(err) {
		t.Fatal("rejected work directory modified the source")
	}
	if err = os.Symlink(input, filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err = directoryArchive(source, scratch); err == nil {
		t.Fatal("symbolic link inside source accepted")
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 2 {
		t.Fatal("failed archive left a temporary publication file")
	}
}

func TestWorldMultipartStreamsActualArchive(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "contents.txt"), []byte("multipart input\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path, err := directoryArchive(source, filepath.Join(root, "scratch"))
	if err != nil {
		t.Fatal(err)
	}
	metadata := model.WorldMetadata{ID: "world-test", Title: "World test", Minecraft: "26.3", Requires: model.Requirements{Loaders: []string{"fabric"}}}
	upload, err := worldUpload("example-mod", metadata, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upload.Close()
	encoded, err := io.ReadAll(upload.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(encoded)) != upload.ContentLength {
		t.Fatal("multipart Content-Length differs from actual bytes")
	}
	_, parameters, err := mime.ParseMediaType(upload.ContentType)
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(bytes.NewReader(encoded), parameters["boundary"])
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "metadata" || part.Header.Get("Content-Type") != "application/json" {
		t.Fatal("metadata part contract differs")
	}
	var decoded model.WorldMetadata
	if err = json.NewDecoder(part).Decode(&decoded); err != nil || decoded.ID != metadata.ID {
		t.Fatal("metadata was not preserved")
	}
	part, err = reader.NextPart()
	if err != nil || part.FormName() != "world" || part.Header.Get("Content-Type") != "application/gzip" {
		t.Fatal("world part contract differs")
	}
	hashes, err := filehash.Reader(part)
	if err != nil {
		t.Fatal(err)
	}
	original, err := filehash.File(path)
	if err != nil || hashes != original {
		t.Fatal("multipart file bytes differ from source")
	}
	if _, err = reader.NextPart(); err != io.EOF {
		t.Fatal("unexpected multipart data")
	}
}

func TestPackUploadStreamsActualZIP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resource-pack.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	metadata, err := writer.Create("pack.mcmeta")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(metadata, `{"pack":{"description":"Archive transport verification","pack_format":1}}`); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	upload, err := packUpload("example-mod", "pack-test", path)
	if err != nil {
		t.Fatal(err)
	}
	defer upload.Close()
	hashes, err := filehash.Reader(upload.Reader)
	if err != nil {
		t.Fatal(err)
	}
	original, err := filehash.File(path)
	if err != nil || hashes != original || hashes.Size != upload.ContentLength || upload.ContentType != "application/zip" {
		t.Fatal("resource pack transfer differs from source ZIP")
	}
}
