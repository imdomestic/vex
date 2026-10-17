package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

type Auth struct {
	mu         sync.RWMutex
	users      map[string]config.User
	projects   map[string]config.Project
	identities map[string]string
	providers  []Provider
	key        []byte
	platform   *store.Store
	sessionTTL time.Duration
	inviteTTL  time.Duration
	baseURL    string
	limits     *redeemLimits
}

type Identity struct {
	SessionID  string `json:"-"`
	LocalAdmin bool   `json:"-"`
	User       string `json:"user,omitempty"`
	Project    string `json:"project,omitempty"`
	RoomID     string `json:"roomId,omitempty"`
	Generation int64  `json:"generation,omitempty"`
	BuildID    string `json:"buildId,omitempty"`
	Expires    int64  `json:"expires,omitempty"`
}

type Subject struct {
	Provider string
	Subject  string
}

type Provider interface {
	Name() string
	Authenticate(context.Context, string) (Subject, error)
}

var ErrUnauthenticated = errors.New("authentication failed")

type StaticProvider struct {
	digests map[string][sha256.Size]byte
}

func NewStaticProvider(users map[string]config.User) (*StaticProvider, error) {
	provider := &StaticProvider{digests: map[string][sha256.Size]byte{}}
	seen := map[string]bool{}
	for user, cfg := range users {
		if !config.ValidID(user) {
			return nil, fmt.Errorf("invalid user ID")
		}
		if cfg.TokenSHA256 == "" {
			continue
		}
		digest, err := hex.DecodeString(cfg.TokenSHA256)
		if err != nil || len(digest) != sha256.Size || cfg.TokenSHA256 != strings.ToLower(cfg.TokenSHA256) || seen[cfg.TokenSHA256] {
			return nil, fmt.Errorf("static tokens require unique lowercase SHA256 digests")
		}
		seen[cfg.TokenSHA256] = true
		provider.digests[user] = [sha256.Size]byte(digest)
	}
	return provider, nil
}

func (*StaticProvider) Name() string { return "static" }

func (p *StaticProvider) Authenticate(ctx context.Context, token string) (Subject, error) {
	if err := ctx.Err(); err != nil {
		return Subject{}, err
	}
	if token == "" {
		return Subject{}, ErrUnauthenticated
	}
	digest := sha256.Sum256([]byte(token))
	var user string
	for candidate, expected := range p.digests {
		if subtle.ConstantTimeCompare(digest[:], expected[:]) == 1 {
			user = candidate
		}
	}
	if user == "" {
		return Subject{}, ErrUnauthenticated
	}
	return Subject{Provider: p.Name(), Subject: user}, nil
}

func New(cfg config.Config) (*Auth, error) {
	provider, err := NewStaticProvider(cfg.Users)
	if err != nil {
		return nil, err
	}
	identities := map[string]string{}
	for user, value := range cfg.Users {
		for _, identity := range append(append([]string{}, value.Identities...), "static:"+user) {
			provider, subject, ok := strings.Cut(identity, ":")
			if !ok || !config.ValidID(provider) || subject == "" || strings.ContainsAny(subject, "\x00\r\n") {
				return nil, fmt.Errorf("invalid configured identity")
			}
			if existing, ok := identities[identity]; ok && existing != user {
				return nil, fmt.Errorf("configured identity belongs to multiple users")
			}
			identities[identity] = user
		}
	}
	key, err := os.ReadFile(cfg.Auth.SigningKeyFile)
	if err != nil {
		return nil, fmt.Errorf("cannot read signing key file")
	}
	if len(key) < 32 {
		return nil, fmt.Errorf("signing key requires at least 32 bytes")
	}
	users := map[string]config.User{}
	for id, user := range cfg.Users {
		users[id] = user
	}
	return &Auth{users: users, projects: cfg.Projects, identities: identities, providers: []Provider{provider}, key: key}, nil
}

func (a *Auth) sum(text string) []byte {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(text))
	return m.Sum(nil)
}

func (a *Auth) Identify(r *http.Request) (Identity, bool) {
	identity, err := a.Authenticate(r)
	return identity, err == nil
}

func (a *Auth) Authenticate(r *http.Request) (Identity, error) {
	if identity, ok := a.identifyCredentials(r); ok {
		return identity, nil
	}
	header := r.Header.Get("Authorization")
	if a.platform == nil || !strings.HasPrefix(header, "Bearer ") {
		return Identity{}, ErrUnauthenticated
	}
	return a.authenticateSession(r.Context(), strings.TrimPrefix(header, "Bearer "))
}

func (a *Auth) identifyCredentials(r *http.Request) (Identity, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return Identity{}, false
	}
	token := strings.TrimPrefix(header, "Bearer ")
	for _, provider := range a.providers {
		subject, err := provider.Authenticate(r.Context(), token)
		if err == nil {
			user, ok := a.identities[subject.Provider+":"+subject.Subject]
			if !ok {
				return Identity{}, false
			}
			return Identity{User: user}, true
		}
		if !errors.Is(err, ErrUnauthenticated) {
			return Identity{}, false
		}
	}
	payload, signature, ok := strings.Cut(token, ".")
	if !ok {
		return Identity{}, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(sig, a.sum("room\n"+payload)) {
		return Identity{}, false
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return Identity{}, false
	}
	var identity Identity
	if json.Unmarshal(data, &identity) != nil || identity.User != "" || !config.ValidID(identity.Project) || identity.RoomID == "" || identity.Generation <= 0 || identity.BuildID == "" || time.Now().Unix() >= identity.Expires {
		return Identity{}, false
	}
	if _, ok := a.projects[identity.Project]; !ok {
		return Identity{}, false
	}
	return identity, true
}

func (a *Auth) RoomToken(project, roomID string, generation int64, buildID string, expires time.Time) (string, error) {
	if _, ok := a.projects[project]; !ok || !config.ValidID(project) || roomID == "" || generation <= 0 || buildID == "" || !expires.After(time.Now()) {
		return "", fmt.Errorf("invalid room claims")
	}
	data, err := json.Marshal(Identity{Project: project, RoomID: roomID, Generation: generation, BuildID: buildID, Expires: expires.Unix()})
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(data)
	return payload + "." + base64.RawURLEncoding.EncodeToString(a.sum("room\n"+payload)), nil
}

func (a *Auth) SignURL(base, path string, expires time.Time) (string, error) {
	if !projectDownloadPath(path) || !expires.After(time.Now()) {
		return "", fmt.Errorf("invalid download path or expiration")
	}
	u, err := url.Parse(strings.TrimRight(base, "/") + path)
	if err != nil {
		return "", err
	}
	exp := strconv.FormatInt(expires.Unix(), 10)
	q := u.Query()
	q.Set("exp", exp)
	q.Set("sig", hex.EncodeToString(a.sum("GET\n"+u.EscapedPath()+"\n"+exp)))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (a *Auth) ValidDownload(r *http.Request) bool {
	if r.Method != http.MethodGet || !projectDownloadPath(r.URL.EscapedPath()) {
		return false
	}
	query := r.URL.Query()
	if len(query["exp"]) != 1 || len(query["sig"]) != 1 {
		return false
	}
	exp, err := strconv.ParseInt(query.Get("exp"), 10, 64)
	if err != nil || strconv.FormatInt(exp, 10) != query.Get("exp") || time.Now().Unix() >= exp {
		return false
	}
	sig, err := hex.DecodeString(query.Get("sig"))
	return err == nil && hmac.Equal(sig, a.sum("GET\n"+r.URL.EscapedPath()+"\n"+query.Get("exp")))
}

func projectDownloadPath(path string) bool {
	parts := strings.Split(path, "/")
	return len(parts) >= 6 && parts[0] == "" && parts[1] == "v2" && parts[2] == "projects" && config.ValidID(parts[3]) && !strings.ContainsAny(path, "?#\r\n")
}

func (a *Auth) RequireProject(identity Identity, project string, minimum config.Role) error {
	configured, exists := a.projects[project]
	user, authenticated := a.User(identity.User)
	if !exists || !authenticated || identity.RoomID != "" {
		return model.Failure("not_found", "project not found")
	}
	if user.Admin {
		return nil
	}
	role, member := configured.Members[identity.User]
	if !member {
		return model.Failure("not_found", "project not found")
	}
	if !role.Allows(minimum) {
		return model.Failure("forbidden", "project role does not permit this operation")
	}
	return nil
}

func (a *Auth) VisibleProjects(identity Identity) []string {
	result := []string{}
	for project := range a.projects {
		if a.RequireProject(identity, project, config.Viewer) == nil {
			result = append(result, project)
		}
	}
	sort.Strings(result)
	return result
}

func (a *Auth) User(id string) (model.User, bool) {
	a.mu.RLock()
	value, ok := a.users[id]
	a.mu.RUnlock()
	if !ok {
		return model.User{}, false
	}
	user := model.User{ID: id, Admin: value.Admin, Identities: append([]string{}, value.Identities...), Minecraft: append([]string{}, value.Minecraft...), Projects: map[string]string{}}
	for project, cfg := range a.projects {
		if value.Admin {
			user.Projects[project] = string(config.Owner)
		} else if role, ok := cfg.Members[id]; ok {
			user.Projects[project] = string(role)
		}
	}
	return user, true
}
