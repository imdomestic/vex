package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/sys/unix"
)

func SaveClientServer(path, name string, server ClientServer) error {
	if !filepath.IsAbs(path) || !ValidID(name) {
		return fmt.Errorf("invalid client configuration path or server ID")
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	resolved, err := LoadClient(Options{DefaultPath: path, Environ: []string{}})
	if err != nil {
		return err
	}
	cfg := resolved.Config
	if cfg.Servers == nil {
		cfg.Servers = map[string]ClientServer{}
	}
	if previous, exists := cfg.Servers[name]; exists && previous.URL != server.URL {
		return fmt.Errorf("server ID already belongs to a different URL")
	}
	cfg.Servers[name] = server
	if cfg.DefaultServer == "" {
		cfg.DefaultServer = name
	}
	if err = cfg.Validate(); err != nil {
		return err
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	staging := path + "." + uuid.NewString()
	file, err := os.OpenFile(staging, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(staging)
	_, writeErr := file.Write(data)
	if err = errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	if err = os.Rename(staging, path); err != nil {
		return err
	}
	directory, err := os.Open(parent)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
