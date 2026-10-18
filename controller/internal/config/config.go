package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Config struct {
	Path      string                 `toml:"-"`
	Server    Server                 `toml:"server"`
	Minecraft Minecraft              `toml:"minecraft"`
	Runtime   Runtime                `toml:"runtime"`
	Runtimes  map[string]JavaRuntime `toml:"runtimes"`
	Agents    map[string]Agent       `toml:"agents"`
	Archive   Archive                `toml:"archive"`
	Auth      Auth                   `toml:"auth"`
	GitHub    GitHub                 `toml:"github"`
	Wake      Wake                   `toml:"wake"`
	Users     map[string]User        `toml:"users"`
	Projects  map[string]Project     `toml:"projects"`
	Build     Build                  `toml:"build"`
	Rooms     Rooms                  `toml:"rooms"`
	Worlds    Worlds                 `toml:"worlds"`
}

type Server struct {
	Name            string   `toml:"name"`
	Listen          []string `toml:"listen"`
	PublicBaseURL   string   `toml:"public_base_url"`
	InternalBaseURL string   `toml:"internal_base_url"`
	DataDir         string   `toml:"data_dir"`
	Zone            string   `toml:"zone"`
	RouterAddress   string   `toml:"router_address"`
	WakeListen      string   `toml:"wake_listen"`
}

type Minecraft struct {
	AcceptEULA bool `toml:"accept_eula"`
}
type Target struct {
	Version string `toml:"version"`
	Java    int    `toml:"java"`
}
type Runtime struct {
	Backend    string     `toml:"backend"`
	Kubernetes Kubernetes `toml:"kubernetes"`
}
type Kubernetes struct {
	NamespacePrefix string          `toml:"namespace_prefix"`
	SystemNamespace string          `toml:"system_namespace"`
	KubeconfigFile  string          `toml:"kubeconfig_file"`
	StorageClass    string          `toml:"storage_class"`
	WebhookSources  []string        `toml:"webhook_sources"`
	Nodes           map[string]Node `toml:"nodes"`
}
type Node struct {
	K8sName  string   `toml:"k8s_name"`
	Roles    []string `toml:"roles"`
	Priority int      `toml:"priority"`
	MaxRooms int      `toml:"max_rooms"`
}
type JavaRuntime struct {
	ServerImage string `toml:"server_image"`
	BuildImage  string `toml:"build_image"`
	JDKHome     string `toml:"jdk_home"`
}
type Agent struct {
	Minecraft string `toml:"minecraft"`
	Loader    string `toml:"loader"`
	Version   string `toml:"version"`
	File      string `toml:"file"`
}
type Archive struct {
	Backend  string       `toml:"backend"`
	Local    LocalArchive `toml:"local"`
	RsyncSSH RsyncSSH     `toml:"rsync_ssh"`
}
type LocalArchive struct {
	Directory string `toml:"directory"`
}
type RsyncSSH struct {
	Target         string `toml:"target"`
	IdentityFile   string `toml:"identity_file"`
	KnownHostsFile string `toml:"known_hosts_file"`
	BWLimit        string `toml:"bwlimit"`
}
type Auth struct {
	SigningKeyFile string                  `toml:"signing_key_file"`
	SessionTTL     string                  `toml:"session_ttl"`
	InviteTTL      string                  `toml:"invite_ttl"`
	AdminSocket    string                  `toml:"admin_socket"`
	Providers      map[string]AuthProvider `toml:"providers"`
}
type AuthProvider struct {
	Type string `toml:"type"`
}
type GitHub struct {
	App GitHubApp `toml:"app"`
}
type GitHubApp struct {
	AppID             int64  `toml:"app_id"`
	ClientID          string `toml:"client_id"`
	ClientSecretFile  string `toml:"client_secret_file"`
	PrivateKeyFile    string `toml:"private_key_file"`
	WebhookSecretFile string `toml:"webhook_secret_file"`
}
type Wake struct {
	Enabled   bool   `toml:"-"`
	Listen    string `toml:"-"`
	Endpoint  string `toml:"-"`
	RateLimit string `toml:"rate_limit"`
}
type User struct {
	Admin       bool     `toml:"admin"`
	TokenSHA256 string   `toml:"token_sha256"`
	Identities  []string `toml:"identities"`
	Minecraft   []string `toml:"minecraft"`
}
type Role string

const (
	Viewer    Role = "viewer"
	Developer Role = "developer"
	Owner     Role = "owner"
)

func (r Role) Allows(minimum Role) bool {
	rank := map[Role]int{Viewer: 1, Developer: 2, Owner: 3}
	return rank[minimum] > 0 && rank[r] >= rank[minimum]
}

type Project struct {
	Remote           string            `toml:"remote"`
	DefaultBranch    string            `toml:"default_branch"`
	Trust            string            `toml:"trust"`
	Git              ProjectGit        `toml:"git"`
	Minecraft        Target            `toml:"minecraft"`
	Build            ProjectBuild      `toml:"build"`
	Loaders          map[string]Loader `toml:"loaders"`
	Members          map[string]Role   `toml:"members"`
	Whitelist        []string          `toml:"whitelist"`
	Quota            Quota             `toml:"quota"`
	ManifestFallback string            `toml:"manifest_fallback"`
	GitHub           ProjectGitHub     `toml:"github"`
	CI               ProjectCI         `toml:"ci"`
	Policy           ProjectPolicy     `toml:"policy"`
}
type ProjectGitHub struct {
	Repository  string `toml:"repository"`
	DeriveRoles bool   `toml:"derive_roles"`
}
type ProjectCI struct {
	Mode            string `toml:"mode"`
	Workflow        string `toml:"workflow"`
	Artifact        string `toml:"artifact"`
	PollInterval    string `toml:"poll_interval"`
	PRPreviews      bool   `toml:"pr_previews"`
	ForkPreviews    string `toml:"fork_previews"`
	PreviewAccess   string `toml:"preview_access"`
	CommentLanguage string `toml:"comment_language"`
	PackLinkTTL     string `toml:"pack_link_ttl"`
}
type ProjectPolicy struct {
	OnNewHead string `toml:"on_new_head"`
}
type ProjectGit struct {
	PollInterval    string `toml:"poll_interval"`
	SSHIdentityFile string `toml:"ssh_identity_file"`
	KnownHostsFile  string `toml:"known_hosts_file"`
	HTTPSTokenFile  string `toml:"https_token_file"`
	HTTPSUsername   string `toml:"https_username"`
}
type ProjectBuild struct {
	Source     string   `toml:"source"`
	Timeout    string   `toml:"timeout"`
	GradleArgs []string `toml:"gradle_args"`
}
type Loader struct {
	Enabled               bool         `toml:"enabled"`
	GradleTask            string       `toml:"gradle_task"`
	ArtifactGlob          string       `toml:"artifact_glob"`
	ArtifactExclude       []string     `toml:"artifact_exclude"`
	LoaderVersionProperty string       `toml:"loader_version_property"`
	Deps                  []Dependency `toml:"deps"`
	Agent                 string       `toml:"agent"`
}
type Dependency struct {
	Name   string `toml:"name"`
	URL    string `toml:"url"`
	Side   string `toml:"side"`
	SHA256 string `toml:"sha256"`
}
type Quota struct {
	MaxRooms         int    `toml:"max_rooms"`
	RoomMemoryMax    string `toml:"room_memory_max"`
	BuildCache       int    `toml:"build_cache"`
	SnapshotKeep     int    `toml:"snapshot_keep"`
	SnapshotKeepDays int    `toml:"snapshot_keep_days"`
}
type Build struct {
	MaxConcurrent int    `toml:"max_concurrent"`
	LockFile      string `toml:"lock_file"`
}
type Idle struct {
	Commit string `toml:"commit"`
	Branch string `toml:"branch"`
	World  string `toml:"world"`
	PR     string `toml:"pr"`
}
type Rooms struct {
	Idle         Idle   `toml:"idle"`
	JoinLease    string `toml:"join_lease"`
	StartTimeout string `toml:"start_timeout"`
	Heap         string `toml:"heap"`
	DefaultSeed  string `toml:"default_seed"`
}
type Worlds struct {
	ScannerFile string `toml:"scanner_file"`
}
type Client struct {
	Path          string                  `toml:"-"`
	Server        string                  `toml:"server"`
	DefaultServer string                  `toml:"default_server"`
	Servers       map[string]ClientServer `toml:"servers"`
	URL           string                  `toml:"url"`
	TokenFile     string                  `toml:"token_file"`
	Project       string                  `toml:"project"`
	Timeout       string                  `toml:"timeout"`
}

type ClientServer struct {
	URL     string `toml:"url"`
	Name    string `toml:"name"`
	Project string `toml:"project"`
}

func (c Client) Selected() (Client, error) {
	selected := c.Server
	if selected == "" {
		selected = c.DefaultServer
	}
	if selected == "" && len(c.Servers) == 1 {
		for name := range c.Servers {
			selected = name
		}
	}
	if selected == "" && len(c.Servers) > 1 {
		names := make([]string, 0, len(c.Servers))
		for name := range c.Servers {
			names = append(names, name)
		}
		sort.Strings(names)
		return c, fmt.Errorf("select a server using --server: %s", strings.Join(names, ", "))
	}
	if selected != "" {
		server, ok := c.Servers[selected]
		if !ok {
			return c, fmt.Errorf("configured server does not exist: %s", selected)
		}
		c.Server, c.URL = selected, server.URL
		if c.Project == "" {
			c.Project = server.Project
		}
	}
	return c, nil
}

type Git struct {
	ProjectGit
	Remote        string
	DefaultBranch string
}
type ProjectMinecraft struct {
	Target
	Image      string
	Zone       string
	AcceptEULA bool
	Whitelist  []string
}
type EffectiveBuild struct {
	Build
	ProjectBuild
	CacheSize int
	JDKHome   string
}
type EffectiveRooms struct {
	Heap         string
	MemoryLimit  string
	MaxRooms     int
	CommitIdle   string
	WorldIdle    string
	BranchIdle   string
	JoinLease    string
	StartTimeout string
	DefaultSeed  string
	PRIdle       string
}
type EffectiveArchive struct {
	Archive
	KeepPerRoom int
	KeepDays    int
}
type EffectiveWorlds struct {
	Worlds
}
type ProjectConfig struct {
	ID               string
	Path             string
	DataDir          string
	Trust            string
	Server           Server
	Git              Git
	Minecraft        ProjectMinecraft
	Loaders          map[string]Loader
	Agents           map[string]Agent
	Build            EffectiveBuild
	Rooms            EffectiveRooms
	Archive          EffectiveArchive
	Worlds           EffectiveWorlds
	Runtime          Runtime
	Auth             Auth
	Members          map[string]Role
	Quota            Quota
	ManifestFallback string
	Runtimes         map[string]JavaRuntime
	GitHub           ProjectGitHub
	CI               ProjectCI
	Policy           ProjectPolicy
	Wake             Wake
}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,22}$`)

func ValidID(value string) bool {
	return idPattern.MatchString(value) && !strings.HasSuffix(value, "-") && !strings.Contains(value, "--")
}

func (c Config) ForProject(id string) (ProjectConfig, error) {
	p, ok := c.Projects[id]
	if !ok {
		return ProjectConfig{}, fmt.Errorf("unknown project")
	}
	runtime, ok := c.Runtimes[fmt.Sprintf("java%d", p.Minecraft.Java)]
	if !ok && p.Minecraft.Java != 0 {
		return ProjectConfig{}, fmt.Errorf("project has no configured Java runtime")
	}
	whitelist := map[string]bool{}
	for _, value := range p.Whitelist {
		whitelist[value] = true
	}
	for user := range p.Members {
		for _, value := range c.Users[user].Minecraft {
			whitelist[value] = true
		}
	}
	players := make([]string, 0, len(whitelist))
	for value := range whitelist {
		players = append(players, value)
	}
	sort.Strings(players)
	return ProjectConfig{
		ID: id, Path: c.Path, DataDir: filepath.Join(c.Server.DataDir, "projects", id), Trust: p.Trust,
		Server: c.Server, Git: Git{p.Git, p.Remote, p.DefaultBranch},
		Minecraft: ProjectMinecraft{p.Minecraft, runtime.ServerImage, c.Server.Zone, c.Minecraft.AcceptEULA, players},
		Loaders:   p.Loaders, Agents: c.Agents,
		Build:   EffectiveBuild{c.Build, p.Build, p.Quota.BuildCache, runtime.JDKHome},
		Rooms:   EffectiveRooms{c.Rooms.Heap, p.Quota.RoomMemoryMax, p.Quota.MaxRooms, c.Rooms.Idle.Commit, c.Rooms.Idle.World, c.Rooms.Idle.Branch, c.Rooms.JoinLease, c.Rooms.StartTimeout, c.Rooms.DefaultSeed, c.Rooms.Idle.PR},
		Archive: EffectiveArchive{c.Archive, p.Quota.SnapshotKeep, p.Quota.SnapshotKeepDays},
		Worlds:  EffectiveWorlds{c.Worlds}, Runtime: c.Runtime, Auth: c.Auth, Members: p.Members, Quota: p.Quota,
		ManifestFallback: p.ManifestFallback, Runtimes: c.Runtimes, GitHub: p.GitHub, CI: p.CI, Policy: p.Policy, Wake: c.Wake,
	}, nil
}
func (p Project) EnabledLoaders() []string {
	names := []string{}
	for name, loader := range p.Loaders {
		if loader.Enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
func (p ProjectConfig) EnabledLoaders() []string {
	return (Project{Loaders: p.Loaders}).EnabledLoaders()
}
func Duration(value string) time.Duration {
	d, err := time.ParseDuration(value)
	if err != nil {
		panic(err)
	}
	return d
}
func (c Config) Warnings() []string {
	if !c.Minecraft.AcceptEULA {
		return []string{"minecraft.accept_eula=false: room starts require acceptance of the Minecraft EULA"}
	}
	return nil
}
