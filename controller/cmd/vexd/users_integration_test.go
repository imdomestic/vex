//go:build integration && linux

package main

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/auth"
)

func TestAdminSocketRealSystemGroup(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to launch real processes as linwhite and nobody")
	}
	group, err := user.LookupGroup("vex-admin")
	if err != nil {
		t.Fatal("configured vex-admin system group is required")
	}
	groupID, err := strconv.Atoi(group.Gid)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(os.Getenv("TMPDIR"), "admin-group-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chown(directory, -1, groupID); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(directory, 0750); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, "a.sock")
	listener, err := auth.ListenAdminSocket(socket, groupID)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer program.Close()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(previous)
	for _, name := range []string{"linwhite", "nobody"} {
		account, err := user.Lookup(name)
		if err != nil {
			t.Fatal("real system test user is unavailable")
		}
		uid, err := strconv.ParseUint(account.Uid, 10, 32)
		if err != nil {
			t.Fatal(err)
		}
		gid, err := strconv.ParseUint(account.Gid, 10, 32)
		if err != nil {
			t.Fatal(err)
		}
		memberships, err := account.GroupIds()
		if err != nil {
			t.Fatal(err)
		}
		groups := []uint32{}
		member := false
		for _, value := range memberships {
			parsed, err := strconv.ParseUint(value, 10, 32)
			if err != nil {
				t.Fatal(err)
			}
			groups = append(groups, uint32(parsed))
			member = member || value == group.Gid
		}
		if member != (name == "linwhite") {
			t.Fatal("actual system group membership does not match the test requirement")
		}
		command := exec.CommandContext(t.Context(), "/proc/self/fd/3", "-test.run=^TestAdminSocketPermissionProbe$", "-test.v")
		command.ExtraFiles = []*os.File{program}
		command.Env = append(os.Environ(), "VEX_IT_SOCKET_PROBE=a.sock", "VEX_IT_SOCKET_ALLOWED="+strconv.FormatBool(member))
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups}}
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("real %s Unix socket check failed; exit=%v, diagnosticBytes=%d", name, err, len(output))
		}
		t.Logf("actual Unix socket user=%s member=%t permission verified", name, member)
	}
}

func TestAdminSocketPermissionProbe(t *testing.T) {
	socket := os.Getenv("VEX_IT_SOCKET_PROBE")
	if socket == "" {
		t.Skip("invoked by the real system-group test")
	}
	connection, err := net.DialTimeout("unix", socket, time.Second)
	if connection != nil {
		connection.Close()
	}
	if os.Getenv("VEX_IT_SOCKET_ALLOWED") == "true" {
		if err != nil {
			t.Fatal("admin group member could not connect")
		}
	} else if !errors.Is(err, os.ErrPermission) {
		t.Fatal("nonmember did not receive a Unix permission denial")
	}
}
