package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/go-github/v88/github"
	"github.com/google/uuid"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"golang.org/x/oauth2"
	githuboauth "golang.org/x/oauth2/github"
)

type DeviceChallenge struct {
	Handle          string    `json:"handle"`
	UserCode        string    `json:"userCode"`
	VerificationURI string    `json:"verificationUri"`
	ExpiresAt       time.Time `json:"expiresAt"`
	Interval        int64     `json:"interval"`
}

// deviceRecord 是数据库中等待授权的登录，handle 字段与记录键都是 handle 的摘要。完成、取消或到期时删除。
type deviceRecord struct {
	Handle     string       `json:"handle"`
	DeviceCode string       `json:"deviceCode"`
	Interval   int64        `json:"interval"`
	ExpiresAt  time.Time    `json:"expiresAt"`
	Client     string       `json:"client"`
	Subject    *Subject     `json:"subject"`
	Failure    *model.Error `json:"failure"`
}

const deviceKind = "device_attempt"

type DeviceFlow struct {
	mu       sync.Mutex
	ctx      context.Context
	auth     *Auth
	oauth    oauth2.Config
	http     *http.Client
	nextPoll map[string]time.Time
	cancels  map[string]context.CancelFunc
}

// NewDeviceFlow 创建 GitHub 登录服务，并继续等待重启前尚未完成的授权。
func NewDeviceFlow(ctx context.Context, auth *Auth, clientID string) (*DeviceFlow, error) {
	if auth == nil || auth.platform == nil || clientID == "" {
		return nil, fmt.Errorf("device login requires session storage and GitHub client ID")
	}
	d := &DeviceFlow{ctx: ctx, auth: auth, oauth: oauth2.Config{ClientID: clientID, Endpoint: githuboauth.Endpoint},
		http: &http.Client{Timeout: 20 * time.Second}, nextPoll: map[string]time.Time{}, cancels: map[string]context.CancelFunc{}}
	pending, err := d.sweep(ctx)
	if err != nil {
		return nil, err
	}
	for key, record := range pending {
		if record.Subject == nil && record.Failure == nil {
			d.watch(key, record)
		}
	}
	return d, nil
}

func validClient(client string) bool {
	return strings.TrimSpace(client) != "" && utf8.RuneCountInString(client) <= 200 && strings.IndexFunc(client, unicode.IsControl) < 0
}

// sweep 删除已经到期的记录，返回仍在等待的记录。
func (d *DeviceFlow) sweep(ctx context.Context) (map[string]deviceRecord, error) {
	pending := map[string]deviceRecord{}
	err := d.auth.platform.Update(ctx, func(tx *store.Tx) error {
		records, err := store.All[deviceRecord](ctx, tx, deviceKind)
		if err != nil {
			return err
		}
		now := time.Now()
		for _, record := range records {
			if !record.ExpiresAt.After(now) {
				if err = tx.Delete(ctx, deviceKind, record.Handle); err != nil {
					return err
				}
				continue
			}
			pending[record.Handle] = record
		}
		return nil
	})
	return pending, err
}

func (d *DeviceFlow) Start(ctx context.Context) (DeviceChallenge, error) {
	pending, err := d.sweep(ctx)
	if err != nil {
		return DeviceChallenge{}, err
	}
	if len(pending) >= 1024 {
		return DeviceChallenge{}, model.Failure("rate_limited", "等待授权的请求过多")
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return DeviceChallenge{}, err
	}
	id := base64.RawURLEncoding.EncodeToString(random[:])
	key := tokenDigest(id)
	now := time.Now().UTC()
	response, err := d.oauth.DeviceAuth(context.WithValue(ctx, oauth2.HTTPClient, d.http))
	if err != nil {
		return DeviceChallenge{}, model.Failure("provider_unavailable", "GitHub 登录暂时不可用，请检查 App 的 Device Flow 设置")
	}
	if response.DeviceCode == "" || response.UserCode == "" || response.VerificationURI != "https://github.com/login/device" || !response.Expiry.After(now) || response.Expiry.After(now.Add(20*time.Minute)) {
		return DeviceChallenge{}, model.Failure("provider_unavailable", "GitHub 登录响应无效")
	}
	record := deviceRecord{Handle: key, DeviceCode: response.DeviceCode, Interval: max(response.Interval, 5), ExpiresAt: response.Expiry.UTC()}
	if err = d.auth.platform.Put(ctx, deviceKind, key, record); err != nil {
		return DeviceChallenge{}, err
	}
	d.watch(key, record)
	return DeviceChallenge{Handle: id, UserCode: response.UserCode, VerificationURI: response.VerificationURI, ExpiresAt: record.ExpiresAt, Interval: record.Interval}, nil
}

// watch 在后台向 GitHub 查询授权结果，并把结果写回记录；取消或到期时停止。
func (d *DeviceFlow) watch(key string, record deviceRecord) {
	ctx, cancel := context.WithDeadline(d.ctx, record.ExpiresAt)
	d.mu.Lock()
	d.cancels[key] = cancel
	d.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			d.mu.Lock()
			delete(d.cancels, key)
			d.mu.Unlock()
		}()
		subject, failure := d.authenticate(ctx, record)
		// 取消登录或服务停止时不改写记录；服务停止时记录保留，下次启动继续等待。
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		update, done := context.WithTimeout(context.WithoutCancel(d.ctx), 10*time.Second)
		defer done()
		err := d.auth.platform.Update(update, func(tx *store.Tx) error {
			var current deviceRecord
			if err := tx.Get(update, deviceKind, key, &current); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return nil
				}
				return err
			}
			if failure != nil && failure.Code == "expired_token" {
				return tx.Delete(update, deviceKind, key)
			}
			current.Subject, current.Failure = subject, failure
			return tx.Put(update, deviceKind, key, current)
		})
		if err != nil {
			slog.Error("device login result could not be saved", "error", err)
		}
	}()
}

func (d *DeviceFlow) authenticate(ctx context.Context, record deviceRecord) (*Subject, *model.Error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, d.http)
	challenge := &oauth2.DeviceAuthResponse{DeviceCode: record.DeviceCode, Interval: record.Interval, Expiry: record.ExpiresAt}
	token, err := d.oauth.DeviceAccessToken(ctx, challenge)
	if err != nil {
		return nil, deviceFailure(err)
	}
	client, err := github.NewClient(github.WithHTTPClient(d.http), github.WithAuthToken(token.AccessToken))
	if err != nil {
		return nil, model.Failure("provider_unavailable", "无法连接 GitHub")
	}
	user, _, err := client.Users.Get(ctx, "")
	if err != nil || user.GetID() <= 0 {
		return nil, model.Failure("provider_unavailable", "无法确认 GitHub 账号")
	}
	return &Subject{Provider: "github", Subject: strconv.FormatInt(user.GetID(), 10)}, nil
}

func deviceFailure(err error) *model.Error {
	var retrieve *oauth2.RetrieveError
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &retrieve) && retrieve.ErrorCode == "expired_token" {
		return model.Failure("expired_token", "GitHub 登录请求已过期")
	}
	if retrieve != nil && retrieve.ErrorCode == "access_denied" {
		return model.Failure("access_denied", "GitHub 授权已取消")
	}
	return model.Failure("provider_unavailable", "GitHub 登录暂时不可用")
}

func validHandle(id string) bool {
	_, err := base64.RawURLEncoding.DecodeString(id)
	return len(id) == 43 && err == nil
}

func (d *DeviceFlow) Poll(ctx context.Context, id, client string) (*Login, int64, error) {
	if !validHandle(id) || !validClient(client) {
		return nil, 0, model.Failure("invalid_request", "登录请求编号无效")
	}
	key := tokenDigest(id)
	var record deviceRecord
	var result *model.Error
	err := d.auth.platform.Update(ctx, func(tx *store.Tx) error {
		if err := tx.Get(ctx, deviceKind, key, &record); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				result = model.Failure("expired_token", "GitHub 登录请求已过期")
				return nil
			}
			return err
		}
		if !record.ExpiresAt.After(time.Now()) {
			result = model.Failure("expired_token", "GitHub 登录请求已过期")
			return tx.Delete(ctx, deviceKind, key)
		}
		if record.Client != "" && record.Client != client {
			result = model.Failure("invalid_request", "客户端名称与此登录请求不一致")
			return nil
		}
		if record.Failure != nil {
			result = record.Failure
			return tx.Delete(ctx, deviceKind, key)
		}
		if record.Client == "" {
			record.Client = client
			return tx.Put(ctx, deviceKind, key, record)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	if result != nil {
		d.forget(key)
		return nil, 0, result
	}
	d.mu.Lock()
	if d.nextPoll[key].After(time.Now()) {
		d.mu.Unlock()
		return nil, record.Interval, model.Failure("rate_limited", "请按指定间隔查询登录结果")
	}
	d.nextPoll[key] = time.Now().Add(time.Duration(record.Interval) * time.Second)
	d.mu.Unlock()
	if record.Subject == nil {
		return nil, record.Interval, nil
	}
	login, err := d.auth.issueSubject(ctx, *record.Subject, record.Client)
	if deleteErr := d.auth.platform.Delete(ctx, deviceKind, key); deleteErr != nil {
		return nil, 0, errors.Join(err, deleteErr)
	}
	d.forget(key)
	if err != nil {
		return nil, 0, err
	}
	return &login, 0, nil
}

// Cancel 删除等待中的登录并停止后台查询；不存在的 handle 同样视为已取消。
func (d *DeviceFlow) Cancel(ctx context.Context, id string) error {
	if !validHandle(id) {
		return model.Failure("invalid_request", "登录请求编号无效")
	}
	key := tokenDigest(id)
	if err := d.auth.platform.Delete(ctx, deviceKind, key); err != nil {
		return err
	}
	d.forget(key)
	return nil
}

func (d *DeviceFlow) forget(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.nextPoll, key)
	if cancel, ok := d.cancels[key]; ok {
		cancel()
		delete(d.cancels, key)
	}
}

func (a *Auth) issueSubject(ctx context.Context, subject Subject, client string) (Login, error) {
	if !validClient(client) || a.platform == nil {
		return Login{}, model.Failure("invalid_request", "客户端名称或会话存储无效")
	}
	a.mu.RLock()
	userID, mapped := a.identities[subject.Provider+":"+subject.Subject]
	a.mu.RUnlock()
	user, exists := a.User(userID)
	if !mapped || !exists {
		return Login{}, model.Failure("account_not_linked", "GitHub 账号尚未关联 Vex 用户，请联系管理员")
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Login{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	now := time.Now().UTC()
	record := sessionRecord{Session: model.Session{ID: uuid.NewString(), User: userID, Client: client, CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(a.sessionTTL)}, TokenSHA256: tokenDigest(token)}
	err := a.platform.Update(ctx, func(tx *store.Tx) error {
		if err := tx.Put(ctx, "auth_session", record.ID, record); err != nil {
			return err
		}
		return tx.Put(ctx, "auth_session_token", record.TokenSHA256, record.ID)
	})
	if err != nil {
		return Login{}, err
	}
	return Login{Token: token, User: user, ExpiresAt: record.ExpiresAt}, nil
}
