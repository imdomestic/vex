package api

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

func loginEnvironment(t *testing.T) (*Server, *httptest.Server, *store.Store) {
	t.Helper()
	directory := t.TempDir()
	cfg := config.Defaults()
	cfg.Server.Name = "Login integration"
	cfg.Users = map[string]config.User{"admin": {Admin: true}, "member": {}, "other": {}}
	cfg.Projects = map[string]config.Project{}
	cfg.Auth.SigningKeyFile = filepath.Join(directory, "signing.key")
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Auth.SigningKeyFile, key[:], 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(directory, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	identity, err := auth.NewWithSessions(t.Context(), cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	service := New(cfg, map[string]*Server{}, identity, nil)
	server := httptest.NewServer(service.Handler())
	t.Cleanup(server.Close)
	return service, server, db
}

func TestInvitationBodyReadHasRealNetworkDeadline(t *testing.T) {
	_, server, _ := loginEnvironment(t)
	connection, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second)
	if err != nil {
		t.Fatal("cannot open real invitation HTTP connection")
	}
	defer connection.Close()
	if err = connection.SetDeadline(time.Now().Add(13 * time.Second)); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err = io.WriteString(connection, "POST /v2/auth/invites/redeem HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 64\r\n\r\n{"); err != nil {
		t.Fatal("cannot send partial invitation request")
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal("partial invitation body did not receive a bounded HTTP response")
	}
	defer response.Body.Close()
	if response.StatusCode != 400 || time.Since(started) > 12*time.Second {
		t.Fatal("partial invitation body escaped the read deadline")
	}
}

func loginRequest(t *testing.T, server *httptest.Server, method, path, token string, payload any) (int, []byte, http.Header) {
	t.Helper()
	var input io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		input = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, input)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal("login HTTP request failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data, response.Header
}

func TestExpiredInvitationAndSessionUseRealTime(t *testing.T) {
	service, server, db := loginEnvironment(t)
	cfg := service.Global
	cfg.Auth.InviteTTL = "10ms"
	cfg.Auth.SessionTTL = "20ms"
	identity, err := auth.NewWithSessions(t.Context(), cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	invitation, err := identity.Invite(t.Context(), auth.Identity{LocalAdmin: true}, "member", "")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err = identity.Redeem(t.Context(), invitation.Code, "expired invite"); err == nil {
		t.Fatal("expired invitation accepted")
	}
	invitation, err = identity.Invite(t.Context(), auth.Identity{LocalAdmin: true}, "member", "1m")
	if err != nil {
		t.Fatal(err)
	}
	login, err := identity.Redeem(t.Context(), invitation.Code, "expired session")
	if err != nil {
		t.Fatal("fresh invitation failed")
	}
	time.Sleep(30 * time.Millisecond)
	status, _, _ := loginRequest(t, server, "GET", "/v2/me", login.Token, nil)
	if status != 401 {
		t.Fatal("expired session remained valid")
	}
}

func loginUser(t *testing.T, service *Server, server *httptest.Server, user string) auth.Login {
	t.Helper()
	invitation, err := service.Auth.Invite(t.Context(), auth.Identity{LocalAdmin: true}, user, "")
	if err != nil {
		t.Fatal(err)
	}
	status, data, _ := loginRequest(t, server, "POST", "/v2/auth/invites/redeem", "", map[string]string{"code": invitation.Code, "client": "HTTP integration"})
	if status != 200 {
		t.Fatalf("redemption HTTP %d", status)
	}
	var value auth.Login
	if err = json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestInvitationsSessionsAndPublicMetaOverRealHTTP(t *testing.T) {
	service, server, db := loginEnvironment(t)
	status, data, _ := loginRequest(t, server, "GET", "/v2/meta", "", nil)
	var public map[string]json.RawMessage
	if err := json.Unmarshal(data, &public); err != nil || status != 200 || len(public) != 3 || public["auth"] == nil || public["name"] == nil || public["version"] == nil {
		t.Fatal("public meta exposes unexpected fields")
	}
	admin := loginUser(t, service, server, "admin")
	status, _, _ = loginRequest(t, server, "POST", "/v2/auth/invites", admin.Token, map[string]string{"user": "new-user"})
	created, found := service.Auth.User("new-user")
	if status != 201 || !found || created.Admin || len(created.Projects) != 0 {
		t.Fatal("admin invitation did not create an identity without privileges")
	}
	status, data, _ = loginRequest(t, server, "POST", "/v2/auth/invites", admin.Token, map[string]string{"user": "member"})
	var invite auth.Invitation
	if status != 201 || json.Unmarshal(data, &invite) != nil {
		t.Fatal("admin invitation failed")
	}
	status, page, _ := loginRequest(t, server, "GET", "/i/"+invite.Code, "", nil)
	if status != 200 || bytes.Contains(page, []byte(invite.Code)) || !bytes.Contains(page, []byte("vex login")) {
		t.Fatal("invitation explanation page differs")
	}
	status, data, _ = loginRequest(t, server, "POST", "/v2/auth/invites/redeem", "", map[string]string{"code": strings.ToLower(invite.Code), "client": "second device"})
	var member auth.Login
	if status != 200 || json.Unmarshal(data, &member) != nil || member.User.ID != "member" || len(member.Token) != 43 {
		t.Fatal("invitation login failed after prefetch")
	}
	status, data, _ = loginRequest(t, server, "POST", "/v2/auth/invites/redeem", "", map[string]string{"code": invite.Code, "client": "duplicate"})
	if status != 400 || !bytes.Contains(data, []byte("invite_invalid")) {
		t.Fatal("invitation reuse accepted")
	}
	status, _, _ = loginRequest(t, server, "POST", "/v2/auth/invites", member.Token, map[string]string{"user": "other"})
	if status != 403 {
		t.Fatal("member created another user's invitation")
	}
	status, _, _ = loginRequest(t, server, "POST", "/v2/auth/invites", member.Token, map[string]string{})
	if status != 201 {
		t.Fatal("member could not invite own new device")
	}
	status, data, _ = loginRequest(t, server, "GET", "/v2/me/sessions", member.Token, nil)
	var sessions []model.Session
	if status != 200 || json.Unmarshal(data, &sessions) != nil || len(sessions) != 1 || !sessions[0].Current || sessions[0].User != "member" {
		t.Fatal("current session list differs")
	}
	status, _, _ = loginRequest(t, server, "DELETE", "/v2/me/sessions/"+sessions[0].ID, admin.Token, nil)
	if status != 404 {
		t.Fatal("HTTP session endpoint revoked another user")
	}
	status, _, _ = loginRequest(t, server, "DELETE", "/v2/me/sessions/"+sessions[0].ID, member.Token, nil)
	if status != 204 {
		t.Fatal("session revocation failed")
	}
	status, _, _ = loginRequest(t, server, "GET", "/v2/me", member.Token, nil)
	if status != 401 {
		t.Fatal("revoked session remains valid")
	}
	status, _, _ = loginRequest(t, server, "POST", "/v2/auth/logout", admin.Token, nil)
	if status != 204 {
		t.Fatal("logout failed")
	}
	status, _, _ = loginRequest(t, server, "GET", "/v2/me", admin.Token, nil)
	if status != 401 {
		t.Fatal("logged out session remains valid")
	}
	platform, err := db.Platform()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"auth_invite", "auth_session", "auth_session_token"} {
		values, err := platform.List(t.Context(), kind)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			if bytes.Contains(value, []byte(admin.Token)) || bytes.Contains(value, []byte(member.Token)) || bytes.Contains(value, []byte(strings.ReplaceAll(invite.Code, "-", ""))) {
				t.Fatal("database retained a plaintext credential")
			}
		}
	}
}

func TestInvitationConcurrentRedemptionAndRateLimit(t *testing.T) {
	service, server, _ := loginEnvironment(t)
	invitation, err := service.Auth.Invite(t.Context(), auth.Identity{LocalAdmin: true}, "member", "")
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan int, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Go(func() {
			status, _, _ := loginRequest(t, server, "POST", "/v2/auth/invites/redeem", "", map[string]string{"code": invitation.Code, "client": "concurrent device"})
			results <- status
		})
	}
	group.Wait()
	first, second := <-results, <-results
	if !((first == 200 && second == 400) || (first == 400 && second == 200)) {
		t.Fatal("concurrent redemption did not consume exactly one invitation")
	}
	for range 8 {
		status, _, _ := loginRequest(t, server, "POST", "/v2/auth/invites/redeem", "", map[string]string{"code": "invalid", "client": "limit check"})
		if status != 400 {
			t.Fatalf("unexpected pre-limit HTTP %d", status)
		}
	}
	status, data, headers := loginRequest(t, server, "POST", "/v2/auth/invites/redeem", "", map[string]string{"code": "invalid", "client": "limit check"})
	if status != 429 || !bytes.Contains(data, []byte("rate_limited")) || headers.Get("Retry-After") == "" {
		t.Fatal("redemption rate limit did not apply")
	}
}

func TestSessionSlidingExpirationAndPersistentRevocation(t *testing.T) {
	service, server, db := loginEnvironment(t)
	login := loginUser(t, service, server, "member")
	before, err := service.Auth.Sessions(t.Context(), "member", "")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	status, _, _ := loginRequest(t, server, "GET", "/v2/me", login.Token, nil)
	after, err := service.Auth.Sessions(t.Context(), "member", "")
	if err != nil || status != 200 || !after[0].ExpiresAt.After(before[0].ExpiresAt) || !after[0].LastUsedAt.After(before[0].LastUsedAt) {
		t.Fatal("session expiration did not slide")
	}
	reopened, err := auth.NewWithSessions(t.Context(), service.Global, db)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/v2/me", nil)
	request.Header.Set("Authorization", "Bearer "+login.Token)
	if _, err = reopened.Authenticate(request); err != nil {
		t.Fatal("session did not survive authenticator restart")
	}
	if err = reopened.Revoke(t.Context(), "member", after[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Auth.Authenticate(request); err == nil {
		t.Fatal("revocation was not immediately shared")
	}
}

func TestExpirationAfterActualSQLiteTransactionWait(t *testing.T) {
	service, _, db := loginEnvironment(t)
	cfg := service.Global
	cfg.Auth.SessionTTL, cfg.Auth.InviteTTL = "300ms", "300ms"
	identity, err := auth.NewWithSessions(t.Context(), cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	platform, err := db.Platform()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"invitation", "session"} {
		t.Run(kind, func(t *testing.T) {
			invitation, err := identity.Invite(t.Context(), auth.Identity{LocalAdmin: true}, "member", "")
			if err != nil {
				t.Fatal(err)
			}
			token := ""
			if kind == "session" {
				login, err := identity.Redeem(t.Context(), invitation.Code, "SQLite expiration test")
				if err != nil {
					t.Fatal("initial session creation failed")
				}
				token = login.Token
			}
			locked, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				finished <- platform.Update(t.Context(), func(tx *store.Tx) error {
					close(locked)
					<-release
					return nil
				})
			}()
			<-locked
			result := make(chan error, 1)
			go func() {
				if kind == "invitation" {
					_, err := identity.Redeem(t.Context(), invitation.Code, "waiting device")
					result <- err
				} else {
					request := httptest.NewRequest("GET", "/v2/me", nil)
					request.Header.Set("Authorization", "Bearer "+token)
					_, err := identity.Authenticate(request)
					result <- err
				}
			}()
			time.Sleep(400 * time.Millisecond)
			close(release)
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			if err := <-result; err == nil {
				t.Fatal("credential expired while waiting for SQLite was accepted")
			}
		})
	}
}
