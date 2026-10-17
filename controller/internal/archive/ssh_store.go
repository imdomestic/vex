package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
)

type SSHStore struct {
	Config  config.RsyncSSH
	WorkDir string
}

var hostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]*$`)

func target(value string) (string, string, error) {
	host, root, ok := strings.Cut(value, ":")
	if !ok || !hostPattern.MatchString(host) || !strings.HasPrefix(root, "/") || path.Clean(root) != root || strings.ContainsAny(root, "\x00\n\r") {
		return "", "", fmt.Errorf("invalid archive SSH target")
	}
	return host, root, nil
}

func safeRelative(value string) bool {
	return value != "" && value != "." && !filepath.IsAbs(value) && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../") && !strings.ContainsAny(value, "\\\x00\n\r")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (s SSHStore) location(key string) (string, string, error) {
	if !safeRelative(key) {
		return "", "", fmt.Errorf("archive key must be relative")
	}
	host, root, err := target(s.Config.Target)
	if err != nil {
		return "", "", err
	}
	return host, path.Join(root, key), nil
}

func (s SSHStore) sshOptions() []string {
	return []string{"-F", "/dev/null", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2", "-i", s.Config.IdentityFile, "-o", "UserKnownHostsFile=" + s.Config.KnownHostsFile}
}

func (s SSHStore) remote(ctx context.Context, host, command string, input []byte) ([]byte, error) {
	args := append(s.sshOptions(), host, command)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("archive SSH operation failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func (s SSHStore) rsync(ctx context.Context, source, destination string) error {
	transport := append([]string{"ssh"}, s.sshOptions()...)
	quoted := make([]string, len(transport))
	for i, arg := range transport {
		quoted[i] = shellQuote(arg)
	}
	args := []string{"-n", "19", "rsync", "--partial", "--inplace", "--append-verify", "--protect-args", "--chmod=F640,D750", "--bwlimit=" + s.Config.BWLimit, "-e", strings.Join(quoted, " "), "--", source, destination}
	cmd := exec.CommandContext(ctx, "nice", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 10 * time.Second
	cmd.Env = append(os.Environ(), "TMPDIR="+s.WorkDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("archive transfer failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (s SSHStore) Put(ctx context.Context, key, source string) error {
	host, filename, err := s.location(key)
	if err != nil {
		return err
	}
	if _, err = s.remote(ctx, host, "mkdir -p -- "+shellQuote(path.Dir(filename)), nil); err != nil {
		return err
	}
	return s.rsync(ctx, source, host+":"+filename)
}

func (s SSHStore) Verify(ctx context.Context, key, digest string, size int64) error {
	host, filename, err := s.location(key)
	if err != nil {
		return err
	}
	command := "sha256sum --check --status && test \"$(stat -c %s -- " + shellQuote(filename) + ")\" -eq " + strconv.FormatInt(size, 10)
	_, err = s.remote(ctx, host, command, []byte(digest+"  "+filename+"\n"))
	return err
}

func (s SSHStore) Get(ctx context.Context, key, destination string) error {
	host, filename, err := s.location(key)
	if err != nil {
		return err
	}
	return s.rsync(ctx, host+":"+filename, destination)
}

func (s SSHStore) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	host, filename, err := s.location(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	output, err := s.remote(ctx, host, "test -f "+shellQuote(filename)+" && stat -c %s -- "+shellQuote(filename), nil)
	if err != nil {
		return ObjectInfo{}, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil || size < 0 {
		return ObjectInfo{}, fmt.Errorf("invalid remote archive size")
	}
	return ObjectInfo{Size: size}, nil
}
