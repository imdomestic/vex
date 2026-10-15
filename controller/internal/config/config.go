package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Path      string            `toml:"-"`
	Server    Server            `toml:"server"`
	Auth      Auth              `toml:"auth"`
	Git       Git               `toml:"git"`
	Minecraft Minecraft         `toml:"minecraft"`
	Loaders   map[string]Loader `toml:"loaders"`
	Build     Build             `toml:"build"`
	Rooms     Rooms             `toml:"rooms"`
	Archive   Archive           `toml:"archive"`
	Assets    Assets            `toml:"assets"`
}

type Server struct {
	Listen          []string `toml:"listen"`
	PublicBaseURL   string   `toml:"public_base_url"`
	InternalBaseURL string   `toml:"internal_base_url"`
	DataDir         string   `toml:"data_dir"`
}

type Auth struct {
	UsersFile      string `toml:"users_file"`
	SigningKeyFile string `toml:"signing_key_file"`
}

type Git struct {
	Remote        string `toml:"remote"`
	PollInterval  string `toml:"poll_interval"`
	DefaultBranch string `toml:"default_branch"`
}

type Minecraft struct {
	Version       string   `toml:"version"`
	Java          int      `toml:"java"`
	Zone          string   `toml:"zone"`
	Image         string   `toml:"image"`
	Whitelist     []string `toml:"whitelist"`
	HankWhitelist []string `toml:"hank_whitelist"`
}

type Loader struct {
	Enabled               bool         `toml:"enabled"`
	GradleTask            string       `toml:"gradle_task"`
	ArtifactGlob          string       `toml:"artifact_glob"`
	ArtifactExclude       []string     `toml:"artifact_exclude"`
	LoaderVersionProperty string       `toml:"loader_version_property"`
	Deps                  []Dependency `toml:"deps"`
}

type Dependency struct {
	Name   string `toml:"name"`
	URL    string `toml:"url"`
	Side   string `toml:"side"`
	SHA256 string `toml:"sha256"`
}

type Build struct {
	MaxConcurrent int      `toml:"max_concurrent"`
	CacheSize     int      `toml:"cache_size"`
	Timeout       string   `toml:"timeout"`
	GradleArgs    []string `toml:"gradle_args"`
	JDKHome       string   `toml:"jdk_home"`
	SystemdUser   bool     `toml:"systemd_user"`
	LockFile      string   `toml:"lock_file"`
}

type Rooms struct {
	Namespace      string         `toml:"namespace"`
	Kubeconfig     string         `toml:"kubeconfig"`
	RouterAddress  string         `toml:"router_address"`
	WebhookSources []string       `toml:"webhook_sources"`
	Heap           string         `toml:"heap"`
	MemoryLimit    string         `toml:"memory_limit"`
	MaxRunning     map[string]int `toml:"max_running"`
	CommitIdle     string         `toml:"commit_idle"`
	AssetIdle      string         `toml:"asset_idle"`
	BranchIdle     string         `toml:"branch_idle"`
	JoinLease      string         `toml:"join_lease"`
	StartTimeout   string         `toml:"start_timeout"`
	DefaultSeed    string         `toml:"default_seed"`
}

type Archive struct {
	Enabled        bool   `toml:"enabled"`
	KeepPerRoom    int    `toml:"keep_per_room"`
	KeepDays       int    `toml:"keep_days"`
	TankTarget     string `toml:"tank_target"`
	TankBWLimit    string `toml:"tank_bwlimit"`
	IdentityFile   string `toml:"identity_file"`
	KnownHostsFile string `toml:"known_hosts_file"`
}

type Assets struct {
	WorldScanner string `toml:"world_scanner"`
}

func Load(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err = toml.Unmarshal(b, &c); err != nil {
		return c, err
	}
	c.Path, err = filepath.Abs(path)
	if err != nil {
		return c, err
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if !filepath.IsAbs(c.Server.DataDir) || len(c.Server.Listen) == 0 {
		return fmt.Errorf("server requires absolute data_dir and listen addresses")
	}
	for _, address := range c.Server.Listen {
		if _, _, err := net.SplitHostPort(address); err != nil {
			return err
		}
	}
	for _, base := range []string{c.Server.PublicBaseURL, c.Server.InternalBaseURL} {
		u, err := url.Parse(base)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return fmt.Errorf("invalid base URL")
		}
	}
	if c.Auth.UsersFile == "" || c.Auth.SigningKeyFile == "" {
		return fmt.Errorf("auth secret file paths are required")
	}
	if c.Git.Remote == "" || c.Git.DefaultBranch == "" {
		return fmt.Errorf("git remote and default_branch are required")
	}
	if c.Minecraft.Version == "" || c.Minecraft.Java < 21 || c.Minecraft.Zone == "" {
		return fmt.Errorf("minecraft version, java and zone are required")
	}
	if c.Build.MaxConcurrent != 1 || c.Build.CacheSize < 1 {
		return fmt.Errorf("build requires max_concurrent=1 and positive cache_size")
	}
	if len(c.EnabledLoaders()) == 0 {
		return fmt.Errorf("at least one loader must be enabled")
	}
	for _, value := range []string{c.Git.PollInterval, c.Build.Timeout, c.Rooms.CommitIdle, c.Rooms.AssetIdle, c.Rooms.BranchIdle, c.Rooms.JoinLease, c.Rooms.StartTimeout} {
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid positive duration %q", value)
		}
	}
	if c.Rooms.Namespace == "" || c.Rooms.Kubeconfig == "" || c.Rooms.RouterAddress == "" {
		return fmt.Errorf("rooms namespace, kubeconfig and router_address are required")
	}
	if len(c.Rooms.MaxRunning) == 0 {
		return fmt.Errorf("room capacity is required")
	}
	for _, limit := range c.Rooms.MaxRunning {
		if limit < 0 {
			return fmt.Errorf("room capacity cannot be negative")
		}
	}
	return nil
}

func (c Config) EnabledLoaders() []string {
	names := []string{}
	for name, loader := range c.Loaders {
		if loader.Enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func Duration(value string) time.Duration {
	d, err := time.ParseDuration(value)
	if err != nil {
		panic(err)
	}
	return d
}
