package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"golang.org/x/time/rate"
)

type Invitation struct {
	Link      string    `json:"link"`
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Login struct {
	Token     string     `json:"token"`
	User      model.User `json:"user"`
	ExpiresAt time.Time  `json:"expiresAt"`
}

type invitationRecord struct {
	User      string    `json:"user"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type sessionRecord struct {
	model.Session
	TokenSHA256 string `json:"tokenSHA256"`
}

func NewWithSessions(ctx context.Context, cfg config.Config, db *store.Store) (*Auth, error) {
	a, err := New(cfg)
	if err != nil {
		return nil, err
	}
	a.platform, err = db.Platform()
	if err != nil {
		return nil, err
	}
	a.sessionTTL, err = time.ParseDuration(cfg.Auth.SessionTTL)
	if err != nil || a.sessionTTL <= 0 {
		return nil, fmt.Errorf("invalid auth.session_ttl")
	}
	a.inviteTTL, err = time.ParseDuration(cfg.Auth.InviteTTL)
	if err != nil || a.inviteTTL <= 0 {
		return nil, fmt.Errorf("invalid auth.invite_ttl")
	}
	a.baseURL = strings.TrimRight(cfg.Server.PublicBaseURL, "/")
	a.limits = &redeemLimits{global: rate.NewLimiter(rate.Every(600*time.Millisecond), 100), ips: map[string]*ipLimit{}}
	users, err := store.All[model.User](ctx, a.platform, "auth_user")
	if err != nil {
		return nil, err
	}
	for _, user := range users {
		if !config.ValidID(user.ID) {
			return nil, fmt.Errorf("invalid stored authentication user")
		}
		if _, configured := a.users[user.ID]; !configured {
			a.users[user.ID] = config.User{Identities: user.Identities, Minecraft: user.Minecraft}
		}
	}
	return a, nil
}

func tokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

var invitationEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

func normalizeCode(code string) (string, error) {
	code = strings.NewReplacer("-", "", "O", "0", "I", "1", "L", "1").Replace(strings.ToUpper(strings.TrimSpace(code)))
	if len(code) != 8 {
		return "", model.Failure("invite_invalid", "邀请无效或已过期")
	}
	if _, err := invitationEncoding.DecodeString(code); err != nil {
		return "", model.Failure("invite_invalid", "邀请无效或已过期")
	}
	return code, nil
}

func (a *Auth) Invite(ctx context.Context, actor Identity, target, ttl string) (Invitation, error) {
	if a.platform == nil {
		return Invitation{}, fmt.Errorf("session storage is unavailable")
	}
	if target == "" {
		target = actor.User
	}
	user, known := a.User(actor.User)
	admin := actor.LocalAdmin || known && user.Admin
	if !known && !actor.LocalAdmin {
		return Invitation{}, model.Failure("unauthorized", "需要登录")
	}
	if target != actor.User && !admin {
		return Invitation{}, model.Failure("forbidden", "只能为自己创建邀请")
	}
	if !config.ValidID(target) {
		return Invitation{}, model.Failure("invalid_request", "用户名称无效")
	}
	_, existing := a.User(target)
	if !existing && !admin {
		return Invitation{}, model.Failure("not_found", "用户不存在")
	}
	duration := a.inviteTTL
	if ttl != "" {
		var err error
		duration, err = time.ParseDuration(ttl)
		if err != nil || duration <= 0 {
			return Invitation{}, model.Failure("invalid_request", "邀请有效期必须是正数时间")
		}
	}
	var random [5]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Invitation{}, err
	}
	code := invitationEncoding.EncodeToString(random[:])
	record := invitationRecord{User: target, ExpiresAt: time.Now().UTC().Add(duration)}
	err := a.platform.Update(ctx, func(tx *store.Tx) error {
		var previous invitationRecord
		if err := tx.Get(ctx, "auth_invite", tokenDigest(code), &previous); err == nil {
			return fmt.Errorf("invitation random collision")
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if !existing {
			created := model.User{ID: target, Identities: []string{}, Minecraft: []string{}, Projects: map[string]string{}}
			if err := tx.Put(ctx, "auth_user", target, created); err != nil {
				return err
			}
		}
		return tx.Put(ctx, "auth_invite", tokenDigest(code), record)
	})
	if err != nil {
		return Invitation{}, err
	}
	if !existing {
		a.mu.Lock()
		a.users[target] = config.User{}
		a.mu.Unlock()
	}
	code = code[:4] + "-" + code[4:]
	return Invitation{Link: a.baseURL + "/i/" + code, Code: code, ExpiresAt: record.ExpiresAt}, nil
}

func (a *Auth) Redeem(ctx context.Context, code, client string) (Login, error) {
	if a.platform == nil {
		return Login{}, fmt.Errorf("session storage is unavailable")
	}
	code, err := normalizeCode(code)
	if err != nil {
		return Login{}, err
	}
	if strings.TrimSpace(client) == "" || utf8.RuneCountInString(client) > 200 || strings.IndexFunc(client, unicode.IsControl) >= 0 {
		return Login{}, model.Failure("invalid_request", "客户端名称无效")
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return Login{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	value := sessionRecord{Session: model.Session{ID: uuid.NewString(), Client: client}, TokenSHA256: tokenDigest(token)}
	err = a.platform.Update(ctx, func(tx *store.Tx) error {
		var invitation invitationRecord
		if err := tx.Get(ctx, "auth_invite", tokenDigest(code), &invitation); errors.Is(err, store.ErrNotFound) {
			return model.Failure("invite_invalid", "邀请无效或已过期")
		} else if err != nil {
			return err
		}
		now := time.Now().UTC()
		if !invitation.ExpiresAt.After(now) {
			return model.Failure("invite_invalid", "邀请无效或已过期")
		}
		if _, ok := a.User(invitation.User); !ok {
			return model.Failure("invite_invalid", "邀请无效或已过期")
		}
		value.User = invitation.User
		value.CreatedAt, value.LastUsedAt, value.ExpiresAt = now, now, now.Add(a.sessionTTL)
		if err := tx.Put(ctx, "auth_session", value.ID, value); err != nil {
			return err
		}
		if err := tx.Put(ctx, "auth_session_token", value.TokenSHA256, value.ID); err != nil {
			return err
		}
		return tx.Delete(ctx, "auth_invite", tokenDigest(code))
	})
	if err != nil {
		return Login{}, err
	}
	user, _ := a.User(value.User)
	return Login{Token: token, User: user, ExpiresAt: value.ExpiresAt}, nil
}

func (a *Auth) authenticateSession(ctx context.Context, token string) (Identity, error) {
	if len(token) != 43 {
		return Identity{}, ErrUnauthenticated
	}
	var value sessionRecord
	err := a.platform.Update(ctx, func(tx *store.Tx) error {
		var id string
		if err := tx.Get(ctx, "auth_session_token", tokenDigest(token), &id); err != nil {
			return err
		}
		if err := tx.Get(ctx, "auth_session", id, &value); err != nil {
			return err
		}
		now := time.Now().UTC()
		if !value.ExpiresAt.After(now) || value.TokenSHA256 != tokenDigest(token) {
			return ErrUnauthenticated
		}
		if _, ok := a.User(value.User); !ok {
			return ErrUnauthenticated
		}
		value.LastUsedAt, value.ExpiresAt = now, now.Add(a.sessionTTL)
		return tx.Put(ctx, "auth_session", id, value)
	})
	if errors.Is(err, store.ErrNotFound) {
		return Identity{}, ErrUnauthenticated
	}
	if err != nil {
		return Identity{}, err
	}
	return Identity{User: value.User, SessionID: value.ID}, nil
}

func (a *Auth) Sessions(ctx context.Context, user, current string) ([]model.Session, error) {
	if a.platform == nil {
		return nil, fmt.Errorf("session storage is unavailable")
	}
	if _, ok := a.User(user); !ok {
		return nil, model.Failure("not_found", "用户不存在")
	}
	records, err := store.All[sessionRecord](ctx, a.platform, "auth_session")
	if err != nil {
		return nil, err
	}
	result := []model.Session{}
	for _, value := range records {
		if value.User == user && value.ExpiresAt.After(time.Now()) {
			value.Current = value.ID == current
			result = append(result, value.Session)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result, nil
}

func (a *Auth) Revoke(ctx context.Context, user, id string) error {
	if a.platform == nil {
		return fmt.Errorf("session storage is unavailable")
	}
	return a.platform.Update(ctx, func(tx *store.Tx) error {
		var value sessionRecord
		if err := tx.Get(ctx, "auth_session", id, &value); err != nil {
			return err
		}
		if value.User != user {
			return model.Failure("not_found", "会话不存在")
		}
		if err := tx.Delete(ctx, "auth_session_token", value.TokenSHA256); err != nil {
			return err
		}
		return tx.Delete(ctx, "auth_session", id)
	})
}

type ipLimit struct {
	limiter *rate.Limiter
	seen    time.Time
}

type redeemLimits struct {
	mu     sync.Mutex
	global *rate.Limiter
	ips    map[string]*ipLimit
}

func (a *Auth) AllowRedemption(r *http.Request) bool {
	if a.limits == nil {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil {
		return false
	}
	now := time.Now()
	a.limits.mu.Lock()
	defer a.limits.mu.Unlock()
	for key, entry := range a.limits.ips {
		if now.Sub(entry.seen) > 5*time.Minute {
			delete(a.limits.ips, key)
		}
	}
	entry, ok := a.limits.ips[host]
	if !ok {
		if len(a.limits.ips) >= 4096 {
			return false
		}
		entry = &ipLimit{limiter: rate.NewLimiter(rate.Every(6*time.Second), 10)}
		a.limits.ips[host] = entry
	}
	entry.seen = now
	return entry.limiter.AllowN(now, 1) && a.limits.global.AllowN(now, 1)
}
