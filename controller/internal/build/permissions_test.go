package build

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"golang.org/x/sys/unix"
)

func TestWorkerPermissionsUseGroupAccessWithoutSetgid(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "projects", "alpha")
	m := &Manager{cfg: config.ProjectConfig{ID: "alpha", DataDir: project, Server: config.Server{DataDir: root}}}
	value := model.Build{ID: "fabric-123456789abc", Project: "alpha", Attempt: 1}
	job := Job{GradleHome: filepath.Join(project, "cache", "gradle"), TempDir: filepath.Join(m.workDir(value), ".work")}
	source := filepath.Join(m.workDir(value), "source")
	for _, directory := range []string{source, job.TempDir, job.GradleHome, filepath.Dir(m.jobPath(value))} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{m.jobPath(value), filepath.Join(source, "gradle.properties"), filepath.Join(m.workDir(value), "build.log")} {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(source, "gradlew")
	if err := os.WriteFile(executable, nil, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(source, "linked-source")
	if err := os.Symlink("gradle.properties", link); err != nil {
		t.Fatal(err)
	}
	if err := m.prepareWorkerPermissions(value, job, os.Getgid()); err != nil {
		t.Fatal(err)
	}
	for path, expected := range map[string]os.FileMode{
		root: 0711, project: 0711, job.GradleHome: 0770, job.TempDir: 0770,
		m.workDir(value): 0770, source: 0770, executable: 0770,
		filepath.Dir(m.jobPath(value)): 0750, m.jobPath(value): 0640,
		filepath.Join(source, "gradle.properties"): 0660,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != expected || info.Mode()&(os.ModeSetgid|os.ModeSetuid) != 0 {
			t.Fatalf("incorrect worker permissions for %s: %v", path, info.Mode())
		}
		var metadata unix.Stat_t
		if err = unix.Stat(path, &metadata); err != nil || int(metadata.Gid) != os.Getgid() {
			t.Fatal("worker group differs", err)
		}
	}
	if target, err := os.Readlink(link); err != nil || target != "gradle.properties" {
		t.Fatal("checkout symlink changed", err)
	}
}

func TestWorkerTraversalPreservesProvisionedDirectory(t *testing.T) {
	for _, mode := range []os.FileMode{0711, 0750} {
		directory := filepath.Join(t.TempDir(), "provided")
		if err := os.Mkdir(directory, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, mode); err != nil {
			t.Fatal(err)
		}
		var before, after unix.Stat_t
		if err := unix.Stat(directory, &before); err != nil {
			t.Fatal(err)
		}
		if err := ensureWorkerTraversal(directory, int(before.Gid)); err != nil {
			t.Fatal(err)
		}
		if err := unix.Stat(directory, &after); err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("provisioned directory metadata changed", err)
		}
	}
	if err := ensureWorkerTraversal("/", os.Getgid()); err != nil {
		t.Fatal("root-owned traversable parent must be accepted without chmod", err)
	}
}

func TestWorkerTraversalRejectsFileAndSymlink(t *testing.T) {
	root := t.TempDir()
	file, link := filepath.Join(root, "file"), filepath.Join(root, "link")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, link} {
		if err := ensureWorkerTraversal(path, os.Getgid()); err == nil {
			t.Fatal("non-directory worker parent accepted")
		}
	}
}
