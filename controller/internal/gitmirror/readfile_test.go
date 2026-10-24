package gitmirror

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileExactCommitRegularBoundedBlob(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	git(t, root, "init", "--initial-branch=main", source)
	first := commit(t, source, "vex.toml", "schema = 1\n")
	second := commit(t, source, "vex.toml", "schema = 2\n")
	if err := os.Remove(filepath.Join(source, "vex.toml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.toml", filepath.Join(source, "vex.toml")); err != nil {
		t.Fatal(err)
	}
	git(t, source, "add", "vex.toml")
	git(t, source, "commit", "-m", "manifest symbolic link")
	link := git(t, source, "rev-parse", "HEAD")
	mirror, err := New(source, filepath.Join(root, "mirror.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = mirror.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	for sha, expected := range map[string]string{first: "schema = 1\n", second: "schema = 2\n"} {
		data, err := mirror.ReadFile(t.Context(), sha, "vex.toml", 1024)
		if err != nil || string(data) != expected {
			t.Fatalf("commit blob mismatch: %q %v", data, err)
		}
	}
	if _, err = mirror.ReadFile(t.Context(), first, "absent.toml", 1024); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	for _, sha := range []string{link, strings.Repeat("0", 40)} {
		if _, err = mirror.ReadFile(t.Context(), sha, "vex.toml", 1024); err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("non-file or missing commit incorrectly permits fallback: %v", err)
		}
	}
	if _, err = mirror.ReadFile(t.Context(), first, "vex.toml", 1); err == nil {
		t.Fatal("oversize blob accepted")
	}
}
