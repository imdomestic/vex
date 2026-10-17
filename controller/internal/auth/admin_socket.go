package auth

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

type adminListener struct {
	net.Listener
	lock *os.File
}

func (l *adminListener) Close() error {
	return errors.Join(l.Listener.Close(), l.lock.Close())
}

func ListenAdminSocket(filename string, groupID int) (net.Listener, error) {
	if !filepath.IsAbs(filename) || len(filename) > 100 || groupID < 0 {
		return nil, fmt.Errorf("invalid admin socket location or group")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(filename))
	if err != nil || parent != filepath.Dir(filename) {
		return nil, fmt.Errorf("admin socket requires an existing canonical parent directory")
	}
	var parentInfo unix.Stat_t
	if err = unix.Stat(parent, &parentInfo); err != nil || int(parentInfo.Uid) != os.Geteuid() || parentInfo.Mode&0027 != 0 || parentInfo.Mode&0050 != 0 && int(parentInfo.Gid) != groupID {
		return nil, fmt.Errorf("admin socket parent must be owned by this user, accessible only to its owner and admin group, and not writable by the group")
	}
	lock, err := os.OpenFile(filename+".lock", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	keepLock := false
	defer func() {
		if !keepLock {
			lock.Close()
		}
	}()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, fmt.Errorf("admin socket is already managed")
	}
	info, err := os.Lstat(filename)
	if err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("admin socket path is occupied by another file type")
		}
		connection, dialErr := net.DialTimeout("unix", filename, time.Second)
		if dialErr == nil {
			connection.Close()
			return nil, fmt.Errorf("admin socket is already listening")
		}
		if !errors.Is(dialErr, unix.ECONNREFUSED) {
			return nil, fmt.Errorf("cannot verify stale admin socket")
		}
		if err = os.Remove(filename); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filename, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err = os.Chown(filename, -1, groupID); err != nil {
		listener.Close()
		return nil, err
	}
	if err = os.Chmod(filename, 0660); err != nil {
		listener.Close()
		return nil, err
	}
	keepLock = true
	return &adminListener{Listener: listener, lock: lock}, nil
}
