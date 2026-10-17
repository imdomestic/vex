//go:build integration && linux

package build

import (
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/imdomestic/vex/controller/internal/gitmirror"
)

func TestRealWorkerAndControllerCheckoutPermissions(t *testing.T) {
	if os.Getenv("VEX_IT_WORKER_IDENTITIES") != "1" {
		t.Skip("requires actual vexd and vex-build system identities")
	}
	if os.Geteuid() != 0 {
		t.Fatal("this identity integration requires root")
	}
	controller, err := user.Lookup("vexd")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := user.Lookup("vex-build")
	if err != nil {
		t.Fatal(err)
	}
	controllerUID, err := strconv.Atoi(controller.Uid)
	if err != nil {
		t.Fatal(err)
	}
	workerGID, err := strconv.Atoi(worker.Gid)
	if err != nil {
		t.Fatal(err)
	}
	m, commits := realManager(t)
	value, _, err := m.Request(t.Context(), commits[0], "fabric", false)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(m.cfg.Server.DataDir)
	if err = os.Chmod(filepath.Dir(root), 0755); err != nil {
		t.Fatal(err)
	}
	work := m.workDir(value)
	if err = os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	if err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(path, 0755)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = filepath.WalkDir(filepath.Join(root, "mirror.git"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chown(path, controllerUID, workerGID)
	}); err != nil {
		t.Fatal(err)
	}
	if err = os.Chown(work, controllerUID, workerGID); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(work, 0770); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(work, "source")
	for _, step := range []struct {
		phase    string
		identity *user.User
	}{
		{"checkout", controller},
		{"generate", worker},
		{"reject-removal", controller},
		{"prepare-removal", worker},
		{"remove", controller},
	} {
		t.Run(step.phase, func(t *testing.T) {
			uid, err := strconv.Atoi(step.identity.Uid)
			if err != nil {
				t.Fatal(err)
			}
			gid, err := strconv.Atoi(step.identity.Gid)
			if err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(executable, "-test.v", "-test.run=^TestWorkerIdentityProcess$")
			command.Env = append(os.Environ(),
				"VEX_IT_WORKER_PHASE="+step.phase,
				"VEX_IT_WORKER_SOURCE="+source,
				"VEX_IT_WORKER_REMOTE="+m.cfg.Git.Remote,
				"VEX_IT_WORKER_MIRROR="+filepath.Join(root, "mirror.git"),
				"VEX_IT_WORKER_SHA="+value.SHA,
				"VEX_IT_WORKER_GID="+worker.Gid)
			command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
				Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{uint32(workerGID)},
			}}
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("actual %s process failed: %v: %s", step.identity.Username, err, output)
			}
		})
		if t.Failed() {
			t.FailNow()
		}
	}
	if _, err = os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("checkout remains after controller removal", err)
	}
}

func TestWorkerIdentityProcess(t *testing.T) {
	phase := os.Getenv("VEX_IT_WORKER_PHASE")
	if phase == "" {
		t.Skip("identity subprocess only")
	}
	source := os.Getenv("VEX_IT_WORKER_SOURCE")
	mirror, err := gitmirror.New(os.Getenv("VEX_IT_WORKER_REMOTE"), os.Getenv("VEX_IT_WORKER_MIRROR"))
	if err != nil {
		t.Fatal(err)
	}
	switch phase {
	case "checkout":
		if err = mirror.Checkout(t.Context(), os.Getenv("VEX_IT_WORKER_SHA"), source); err != nil {
			t.Fatal(err)
		}
		gid, err := strconv.Atoi(os.Getenv("VEX_IT_WORKER_GID"))
		if err != nil {
			t.Fatal(err)
		}
		if err = filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err = os.Chown(path, -1, gid); err != nil {
				return err
			}
			if entry.IsDir() {
				return os.Chmod(path, 0770)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	case "generate":
		path := filepath.Join(source, "build-logic", "build", "resources", "META-INF")
		if err = os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.Chmod(path, 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(path, "plugin.properties"), []byte("implementation-class=BuildPlugin"), 0644); err != nil {
			t.Fatal(err)
		}
	case "reject-removal":
		if err = mirror.RemoveCheckout(t.Context(), source); err == nil {
			t.Fatal("controller accepted a non-writable generated directory")
		}
		if _, err = os.Stat(filepath.Join(source, ".git", "HEAD")); err != nil {
			t.Fatal("permission failure removed Git metadata", err)
		}
	case "prepare-removal":
		if err = prepareCheckoutRemoval(source); err != nil {
			t.Fatal(err)
		}
	case "remove":
		if err = mirror.RemoveCheckout(t.Context(), source); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("unknown identity subprocess operation")
	}
}
