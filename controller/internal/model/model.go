package model

import "time"

type Error struct {
	Code string `json:"code"`
	Message string `json:"message"`
	Details *ErrorDetails `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Message }

type ErrorDetails struct { SnapshotID string `json:"snapshotId"` }

func Failure(code, message string) *Error { return &Error{Code: code, Message: message} }

type BuildFile struct {
	Name string `json:"name"`
	SHA256 string `json:"sha256"`
	Size int64 `json:"size"`
	Side string `json:"side"`
	URL string `json:"url"`
}

type Build struct {
	ID string `json:"id"`
	SHA string `json:"sha"`
	Loader string `json:"loader"`
	Status string `json:"status"`
	Attempt int `json:"attempt"`
	QueuedAt time.Time `json:"queuedAt"`
	StartedAt *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	Minecraft *string `json:"minecraft"`
	LoaderVersion *string `json:"loaderVersion"`
	Java *int `json:"java"`
	BuiltAt *time.Time `json:"builtAt"`
	DurationSec *float64 `json:"durationSec"`
	ModIDs []string `json:"modIds"`
	Files []BuildFile `json:"files"`
	Log string `json:"log"`
	LastError *Error `json:"lastError"`
}

type BuildSummary struct {
	ID string `json:"id"`
	Status string `json:"status"`
}

type Branch struct {
	Name string `json:"name"`
	Head string `json:"head"`
	Rooms map[string]string `json:"rooms"`
	Builds map[string]*BuildSummary `json:"builds"`
}

type Commit struct {
	SHA string `json:"sha"`
	Parents []string `json:"parents"`
	Subject string `json:"subject"`
	Author string `json:"author"`
	Time time.Time `json:"time"`
	Refs []string `json:"refs"`
	Builds map[string]*BuildSummary `json:"builds"`
}

type Player struct {
	Name string `json:"name"`
	UUID string `json:"uuid"`
}

type SeedResolved struct {
	Kind string `json:"kind"`
	ID string `json:"id,omitempty"`
	Rev string `json:"rev,omitempty"`
}

type Room struct {
	ID string `json:"id"`
	Kind string `json:"kind"`
	Branch *string `json:"branch"`
	SHA string `json:"sha"`
	Loader string `json:"loader"`
	BuildID string `json:"buildId"`
	Generation int64 `json:"generation"`
	BehindHead bool `json:"behindHead"`
	Host string `json:"host"`
	Port int `json:"port"`
	State string `json:"state"`
	Phase string `json:"phase"`
	Label string `json:"label"`
	Node *string `json:"node"`
	Players []Player `json:"players"`
	IdleSince *time.Time `json:"idleSince"`
	Seed string `json:"seed"`
	SeedResolved *SeedResolved `json:"seedResolved"`
	Asset *string `json:"asset"`
	LastSnapshot *time.Time `json:"lastSnapshot"`
	LastError *Error `json:"lastError"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Lease struct {
	ID string `json:"id"`
	Generation int64 `json:"generation"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type LeaseRecord struct {
	Lease
	RoomID string `json:"roomId"`
	Owner string `json:"owner"`
	FromRoom string `json:"fromRoom"`
	FromGeneration int64 `json:"fromGeneration"`
	PlayerUUID string `json:"playerUUID"`
}

type StartResult struct {
	Room Room `json:"room"`
	Lease Lease `json:"lease"`
}

type Limits struct {
	BuildConcurrency int `json:"buildConcurrency"`
	BuildCacheSize int `json:"buildCacheSize"`
	MaxRunning map[string]int `json:"maxRunning"`
}

type Meta struct {
	Minecraft string `json:"minecraft"`
	Java int `json:"java"`
	Zone string `json:"zone"`
	Loaders []string `json:"loaders"`
	PublicBaseURL string `json:"publicBaseURL"`
	Limits Limits `json:"limits"`
}

type Node struct {
	Name string `json:"name"`
	Ready bool `json:"ready"`
	Schedulable bool `json:"schedulable"`
	MaxRunning int `json:"maxRunning"`
	Rooms []string `json:"rooms"`
}

type Requirements struct {
	Loaders []string `json:"loaders"`
	ChorusMod bool `json:"chorusMod"`
	Namespaces []string `json:"namespaces"`
}

type PackRef struct {
	ID string `json:"id"`
	Rev string `json:"rev"`
}

type Source struct {
	Repo string `json:"repo"`
	Commit string `json:"commit"`
	Scale float64 `json:"scale"`
	Notes string `json:"notes"`
}

type AssetMetadata struct {
	ID string `json:"id"`
	Title string `json:"title"`
	Minecraft string `json:"minecraft"`
	Requires Requirements `json:"requires"`
	ResourcePack *PackRef `json:"resourcePack"`
	Source Source `json:"source"`
}

type WorldFile struct {
	SHA256 string `json:"sha256"`
	Size int64 `json:"size"`
	URL string `json:"url"`
}

type AssetRevision struct {
	AssetMetadata
	Rev string `json:"rev"`
	SnapshotID *string `json:"snapshotId"`
	World WorldFile `json:"world"`
	CreatedAt time.Time `json:"createdAt"`
}

type Asset struct {
	ID string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
	LatestRev string `json:"latestRev"`
	Minecraft string `json:"minecraft"`
	Requires Requirements `json:"requires"`
	ResourcePack *PackRef `json:"resourcePack"`
	Restore *AssetRestore `json:"restore"`
}

type PackRevision struct {
	ID string `json:"id"`
	Rev string `json:"rev"`
	SHA256 string `json:"sha256"`
	SHA1 string `json:"sha1"`
	Size int64 `json:"size"`
	URL string `json:"url"`
	CreatedAt time.Time `json:"createdAt"`
}

type Restore struct {
	Attempt int `json:"attempt"`
	Status string `json:"status"`
	RequestedAt time.Time `json:"requestedAt"`
	StartedAt *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	Error *Error `json:"error"`
}

type AssetRestore struct {
	Restore
	Revision string `json:"revision"`
}

type Snapshot struct {
	ID string `json:"id"`
	RoomID string `json:"roomId"`
	RoomGeneration int64 `json:"roomGeneration"`
	BuildID string `json:"buildId"`
	Minecraft string `json:"minecraft"`
	Asset *PackRef `json:"asset"`
	CapturedAt time.Time `json:"capturedAt"`
	CreatedAt time.Time `json:"createdAt"`
	SHA256 string `json:"sha256"`
	Size int64 `json:"size"`
	LocalAvailable bool `json:"localAvailable"`
	TankVerifiedAt *time.Time `json:"tankVerifiedAt"`
	Restore *Restore `json:"restore"`
}

type HopDestination struct {
	Dest string `json:"dest"`
	Kind string `json:"kind"`
	ID string `json:"id"`
	Title string `json:"title"`
	RoomID *string `json:"roomId"`
	BuildID string `json:"buildId"`
	State string `json:"state"`
}

type Sync struct {
	Rooms []Room `json:"rooms"`
	Builds []Build `json:"builds"`
}

type Transfer struct {
	ID string `json:"id"`
	Kind string `json:"kind"`
	ObjectID string `json:"objectId"`
	Revision string `json:"revision"`
	Part string `json:"part"`
	LocalPath string `json:"localPath"`
	RemotePath string `json:"remotePath"`
	SHA256 string `json:"sha256"`
	Size int64 `json:"size"`
	Status string `json:"status"`
	Attempt int `json:"attempt"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	VerifiedAt *time.Time `json:"verifiedAt"`
	LastError *Error `json:"lastError"`
}

func TransferID(kind,id,revision,part string) string { return kind+":"+id+":"+revision+":"+part }
