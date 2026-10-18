package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
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

type deviceAttempt struct {
	client   string
	expires  time.Time
	interval int64
	nextPoll time.Time
	subject  *Subject
	failure  *model.Error
}

type DeviceFlow struct {
	mu       sync.Mutex
	ctx      context.Context
	auth     *Auth
	oauth    oauth2.Config
	http     *http.Client
	attempts map[string]*deviceAttempt
}

func NewDeviceFlow(ctx context.Context, auth *Auth, clientID string) (*DeviceFlow, error) {
	if auth == nil || auth.platform == nil || clientID == "" {
		return nil, fmt.Errorf("device login requires session storage and GitHub client ID")
	}
	return &DeviceFlow{ctx: ctx, auth: auth, oauth: oauth2.Config{ClientID: clientID, Endpoint: githuboauth.Endpoint},
		http: &http.Client{Timeout: 20 * time.Second}, attempts: map[string]*deviceAttempt{}}, nil
}

func validClient(client string) bool {
	return strings.TrimSpace(client) != "" && utf8.RuneCountInString(client) <= 200 && strings.IndexFunc(client, unicode.IsControl) < 0
}

func (d *DeviceFlow) Start(ctx context.Context) (DeviceChallenge, error) {
	now := time.Now().UTC()
	d.mu.Lock()
	for key, attempt := range d.attempts {
		if !attempt.expires.After(now) {
			delete(d.attempts, key)
		}
	}
	if len(d.attempts) >= 1024 {
		d.mu.Unlock()
		return DeviceChallenge{}, model.Failure("rate_limited", "等待授权的请求过多")
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		d.mu.Unlock()
		return DeviceChallenge{}, err
	}
	id := base64.RawURLEncoding.EncodeToString(random[:])
	key := tokenDigest(id)
	d.attempts[key] = &deviceAttempt{expires: now.Add(time.Minute)}
	d.mu.Unlock()
	response, err := d.oauth.DeviceAuth(context.WithValue(ctx, oauth2.HTTPClient, d.http))
	if err != nil {
		d.remove(key)
		return DeviceChallenge{}, model.Failure("provider_unavailable", "GitHub 登录暂时不可用，请检查 App 的 Device Flow 设置")
	}
	if response.DeviceCode == "" || response.UserCode == "" || response.VerificationURI != "https://github.com/login/device" || !response.Expiry.After(now) || response.Expiry.After(now.Add(20*time.Minute)) {
		d.remove(key)
		return DeviceChallenge{}, model.Failure("provider_unavailable", "GitHub 登录响应无效")
	}
	interval := max(response.Interval, 5)
	d.mu.Lock()
	d.attempts[key] = &deviceAttempt{expires: response.Expiry, interval: interval}
	d.mu.Unlock()
	go d.authenticate(key, response)
	return DeviceChallenge{Handle: id, UserCode: response.UserCode, VerificationURI: response.VerificationURI, ExpiresAt: response.Expiry, Interval: interval}, nil
}

func (d *DeviceFlow) remove(key string) {
	d.mu.Lock()
	delete(d.attempts, key)
	d.mu.Unlock()
}

func (d *DeviceFlow) authenticate(key string, challenge *oauth2.DeviceAuthResponse) {
	ctx, cancel := context.WithDeadline(d.ctx, challenge.Expiry)
	defer cancel()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, d.http)
	token, err := d.oauth.DeviceAccessToken(ctx, challenge)
	var subject *Subject
	var failure *model.Error
	if err != nil {
		failure = deviceFailure(err)
	} else {
		client, clientErr := github.NewClient(github.WithHTTPClient(d.http), github.WithAuthToken(token.AccessToken))
		if clientErr != nil {
			failure = model.Failure("provider_unavailable", "无法连接 GitHub")
		} else {
			user, _, lookupErr := client.Users.Get(ctx, "")
			if lookupErr != nil || user.GetID() <= 0 {
				failure = model.Failure("provider_unavailable", "无法确认 GitHub 账号")
			} else {
				subject = &Subject{Provider: "github", Subject: strconv.FormatInt(user.GetID(), 10)}
			}
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if attempt, ok := d.attempts[key]; ok {
		attempt.subject, attempt.failure = subject, failure
	}
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

func (d *DeviceFlow) Poll(ctx context.Context, id, client string) (*Login, int64, error) {
	if len(id) != 43 || !validClient(client) {
		return nil, 0, model.Failure("invalid_request", "登录请求编号无效")
	}
	key := tokenDigest(id)
	d.mu.Lock()
	defer d.mu.Unlock()
	attempt, ok := d.attempts[key]
	if !ok || !attempt.expires.After(time.Now()) {
		delete(d.attempts, key)
		return nil, 0, model.Failure("expired_token", "GitHub 登录请求已过期")
	}
	if attempt.client != "" && attempt.client != client {
		return nil, 0, model.Failure("invalid_request", "客户端名称与此登录请求不一致")
	}
	attempt.client = client
	if attempt.failure != nil {
		delete(d.attempts, key)
		return nil, 0, attempt.failure
	}
	if attempt.nextPoll.After(time.Now()) {
		return nil, attempt.interval, model.Failure("rate_limited", "请按指定间隔查询登录结果")
	}
	attempt.nextPoll = time.Now().Add(time.Duration(attempt.interval) * time.Second)
	if attempt.subject == nil {
		return nil, attempt.interval, nil
	}
	login, err := d.auth.issueSubject(ctx, *attempt.subject, attempt.client)
	delete(d.attempts, key)
	if err != nil {
		return nil, 0, err
	}
	return &login, 0, nil
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
