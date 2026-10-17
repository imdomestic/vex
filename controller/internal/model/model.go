package model

import "time"

type Error struct {
	Code    string        `json:"code"`
	Message string        `json:"message"`
	Details *ErrorDetails `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Message }

type ErrorDetails struct {
	SnapshotID string `json:"snapshotId"`
}

func Failure(code, message string) *Error { return &Error{Code: code, Message: message} }

type BuildFile struct {
	Name   string `json:"name"`
	SHA1   string `json:"sha1"`
	SHA256 string `json:"sha256"`
	SHA512 string `json:"sha512"`
	Size   int64  `json:"size"`
	Side   string `json:"side"`
	URL    string `json:"url"`
	Path   string `json:"path"`
}

type BuildSource struct {
	Kind string `json:"kind"`
}

type Build struct {
	Project       string      `json:"project"`
	ID            string      `json:"id"`
	Source        BuildSource `json:"source"`
	SHA           string      `json:"sha"`
	Loader        string      `json:"loader"`
	Status        string      `json:"status"`
	Attempt       int         `json:"attempt"`
	QueuedAt      time.Time   `json:"queuedAt"`
	StartedAt     *time.Time  `json:"startedAt"`
	FinishedAt    *time.Time  `json:"finishedAt"`
	Minecraft     *string     `json:"minecraft"`
	LoaderVersion *string     `json:"loaderVersion"`
	Java          *int        `json:"java"`
	BuiltAt       *time.Time  `json:"builtAt"`
	DurationSec   *float64    `json:"durationSec"`
	ModIDs        []string    `json:"modIds"`
	Files         []BuildFile `json:"files"`
	Log           string      `json:"log"`
	LastError     *Error      `json:"lastError"`
}

type BuildSummary struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type Branch struct {
	Project string                   `json:"project"`
	Name    string                   `json:"name"`
	Head    string                   `json:"head"`
	Rooms   map[string]string        `json:"rooms"`
	Builds  map[string]*BuildSummary `json:"builds"`
}

type Commit struct {
	Project string                   `json:"project"`
	SHA     string                   `json:"sha"`
	Parents []string                 `json:"parents"`
	Subject string                   `json:"subject"`
	Author  string                   `json:"author"`
	Time    time.Time                `json:"time"`
	Refs    []string                 `json:"refs"`
	Builds  map[string]*BuildSummary `json:"builds"`
}

type Player struct {
	Name string `json:"name"`
	UUID string `json:"uuid"`
}

type SeedResolved struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Rev  string `json:"rev,omitempty"`
}

type Room struct {
	Project      string        `json:"project"`
	ID           string        `json:"id"`
	Kind         string        `json:"kind"`
	Branch       *string       `json:"branch"`
	SHA          string        `json:"sha"`
	Loader       string        `json:"loader"`
	BuildID      string        `json:"buildId"`
	Generation   int64         `json:"generation"`
	BehindHead   bool          `json:"behindHead"`
	Host         string        `json:"host"`
	Port         int           `json:"port"`
	State        string        `json:"state"`
	Phase        string        `json:"phase"`
	Label        string        `json:"label"`
	Node         *string       `json:"node"`
	Players      []Player      `json:"players"`
	IdleSince    *time.Time    `json:"idleSince"`
	Seed         string        `json:"seed"`
	SeedResolved *SeedResolved `json:"seedResolved"`
	World        *string       `json:"world"`
	LastSnapshot *time.Time    `json:"lastSnapshot"`
	LastError    *Error        `json:"lastError"`
	CreatedAt    time.Time     `json:"createdAt"`
	UpdatedAt    time.Time     `json:"updatedAt"`
	ClosedAt     *time.Time    `json:"closedAt"`
}

type Lease struct {
	ID         string    `json:"id"`
	Generation int64     `json:"generation"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

type LeaseRecord struct {
	Lease
	Project        string `json:"project"`
	RoomID         string `json:"roomId"`
	Owner          string `json:"owner"`
	FromRoom       string `json:"fromRoom"`
	FromGeneration int64  `json:"fromGeneration"`
	PlayerUUID     string `json:"playerUUID"`
}

type StartResult struct {
	Room  Room  `json:"room"`
	Lease Lease `json:"lease"`
}

type Limits struct {
	BuildConcurrency int            `json:"buildConcurrency"`
	BuildCacheSize   int            `json:"buildCacheSize"`
	MaxRunning       map[string]int `json:"maxRunning"`
}

type Meta struct {
	Name          string                    `json:"name"`
	Version       string                    `json:"version"`
	Zone          string                    `json:"zone"`
	PublicBaseURL string                    `json:"publicBaseURL"`
	Runtimes      map[string]RuntimeCatalog `json:"runtimes"`
	Agents        []AgentVersion            `json:"agents"`
	Auth          AuthMethods               `json:"auth"`
}

type AuthMethods struct {
	Methods []string `json:"methods"`
}

type Session struct {
	ID         string    `json:"id"`
	User       string    `json:"user"`
	Client     string    `json:"client"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Current    bool      `json:"current"`
}

type RuntimeCatalog struct {
	Java        int    `json:"java"`
	ServerImage string `json:"serverImage"`
}

type AgentVersion struct {
	Minecraft string `json:"minecraft"`
	Loader    string `json:"loader"`
	Version   string `json:"version"`
}

type Project struct {
	ID            string         `json:"id"`
	Remote        string         `json:"remote"`
	DefaultBranch string         `json:"defaultBranch"`
	Role          string         `json:"role"`
	Minecraft     string         `json:"minecraft"`
	Java          int            `json:"java"`
	Loaders       []string       `json:"loaders"`
	Policy        map[string]any `json:"policy"`
	Quota         map[string]any `json:"quota"`
}

type User struct {
	ID         string            `json:"id"`
	Admin      bool              `json:"admin"`
	Minecraft  []string          `json:"minecraft"`
	Identities []string          `json:"identities"`
	Projects   map[string]string `json:"projects"`
}

type Node struct {
	Name          string   `json:"name"`
	Roles         []string `json:"roles"`
	Priority      int      `json:"priority"`
	Ready         bool     `json:"ready"`
	Schedulable   bool     `json:"schedulable"`
	MaxRunning    int      `json:"maxRunning"`
	Rooms         []string `json:"rooms"`
	RootFreeBytes *int64   `json:"rootFreeBytes"`
}

type Requirements struct {
	Loaders    []string `json:"loaders"`
	Mods       []string `json:"mods"`
	Namespaces []string `json:"namespaces"`
}

type PackRef struct {
	ID  string `json:"id"`
	Rev string `json:"rev"`
}

type Source struct {
	Repo   string  `json:"repo"`
	Commit string  `json:"commit"`
	Scale  float64 `json:"scale"`
	Notes  string  `json:"notes"`
}

type WorldMetadata struct {
	ID           string       `json:"id"`
	Title        string       `json:"title"`
	Minecraft    string       `json:"minecraft"`
	Requires     Requirements `json:"requires"`
	ResourcePack *PackRef     `json:"resourcePack"`
	Source       Source       `json:"source"`
}

type WorldFile struct {
	SHA1   string `json:"sha1"`
	SHA256 string `json:"sha256"`
	SHA512 string `json:"sha512"`
	Size   int64  `json:"size"`
	URL    string `json:"url"`
	Path   string `json:"path"`
}

type WorldRevision struct {
	WorldMetadata
	Project        string     `json:"project"`
	Rev            string     `json:"rev"`
	SnapshotID     *string    `json:"snapshotId"`
	World          WorldFile  `json:"world"`
	CreatedAt      time.Time  `json:"createdAt"`
	TankVerifiedAt *time.Time `json:"tankVerifiedAt"`
}

type World struct {
	Project      string        `json:"project"`
	ID           string        `json:"id"`
	Title        string        `json:"title"`
	State        string        `json:"state"`
	LatestRev    string        `json:"latestRev"`
	Minecraft    string        `json:"minecraft"`
	Requires     Requirements  `json:"requires"`
	ResourcePack *PackRef      `json:"resourcePack"`
	Restore      *WorldRestore `json:"restore"`
}

type PackRevision struct {
	Project   string    `json:"project"`
	ID        string    `json:"id"`
	Rev       string    `json:"rev"`
	SHA256    string    `json:"sha256"`
	SHA1      string    `json:"sha1"`
	SHA512    string    `json:"sha512"`
	Size      int64     `json:"size"`
	URL       string    `json:"url"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"createdAt"`
}

type Restore struct {
	Attempt     int        `json:"attempt"`
	Status      string     `json:"status"`
	RequestedAt time.Time  `json:"requestedAt"`
	StartedAt   *time.Time `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
	Error       *Error     `json:"error"`
}

type WorldRestore struct {
	Restore
	Revision string `json:"revision"`
}

type Snapshot struct {
	Project        string     `json:"project"`
	ID             string     `json:"id"`
	RoomID         string     `json:"roomId"`
	RoomGeneration int64      `json:"roomGeneration"`
	BuildID        string     `json:"buildId"`
	Minecraft      string     `json:"minecraft"`
	World          *PackRef   `json:"world"`
	CapturedAt     time.Time  `json:"capturedAt"`
	CreatedAt      time.Time  `json:"createdAt"`
	SHA256         string     `json:"sha256"`
	SHA1           string     `json:"sha1"`
	SHA512         string     `json:"sha512"`
	Path           string     `json:"path"`
	Size           int64      `json:"size"`
	LocalAvailable bool       `json:"localAvailable"`
	TankVerifiedAt *time.Time `json:"tankVerifiedAt"`
	Restore        *Restore   `json:"restore"`
}

type HopDestination struct {
	Project string  `json:"project"`
	Dest    string  `json:"dest"`
	Kind    string  `json:"kind"`
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	RoomID  *string `json:"roomId"`
	BuildID string  `json:"buildId"`
	State   string  `json:"state"`
}

type Sync struct {
	Project string  `json:"project"`
	Rooms   []Room  `json:"rooms"`
	Builds  []Build `json:"builds"`
}

type Transfer struct {
	Project    string     `json:"project"`
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	ObjectID   string     `json:"objectId"`
	Revision   string     `json:"revision"`
	Part       string     `json:"part"`
	LocalPath  string     `json:"localPath"`
	RemotePath string     `json:"remotePath"`
	SHA256     string     `json:"sha256"`
	Size       int64      `json:"size"`
	Status     string     `json:"status"`
	Attempt    int        `json:"attempt"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	VerifiedAt *time.Time `json:"verifiedAt"`
	LastError  *Error     `json:"lastError"`
}

func TransferID(kind, id, revision, part string) string {
	return kind + ":" + id + ":" + revision + ":" + part
}
