package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imdomestic/vex/controller/internal/api"
	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

func cliLoginServer(t *testing.T, name string) (*api.Server, *httptest.Server, string) {
	t.Helper()
	directory := t.TempDir()
	var key, tokenBytes [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes[:])
	digest := sha256.Sum256([]byte(token))
	cfg := config.Defaults()
	cfg.Server.Name = name
	cfg.Users = map[string]config.User{"admin": {Admin: true, TokenSHA256: hex.EncodeToString(digest[:])}, "member": {}}
	cfg.Projects = map[string]config.Project{"example": {Members: map[string]config.Role{"member": config.Viewer}}}
	cfg.Auth.SigningKeyFile = filepath.Join(directory, "signing.key")
	if err := os.WriteFile(cfg.Auth.SigningKeyFile, key[:], 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(directory, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	server := httptest.NewUnstartedServer(nil)
	cfg.Server.PublicBaseURL = "http://" + server.Listener.Addr().String()
	identity, err := auth.NewWithSessions(t.Context(), cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	projects := map[string]*api.Server{"example": {Cfg: config.ProjectConfig{ID: "example", Members: cfg.Projects["example"].Members}}}
	service := api.New(cfg, projects, identity, nil)
	server.Config.Handler = service.Handler()
	server.Start()
	t.Cleanup(server.Close)
	return service, server, token
}

func TestCLILoginTwoRealServersAndSystemCredentials(t *testing.T) {
	cleanClientEnvironment(t)
	first, firstHTTP, static := cliLoginServer(t, "First server")
	second, secondHTTP, _ := cliLoginServer(t, "Second server")
	file := filepath.Join(t.TempDir(), "client.toml")
	credentials := auth.CredentialStore{Directory: filepath.Join(filepath.Dir(file), "credentials")}
	t.Cleanup(func() {
		credentials.Delete(firstHTTP.URL)
		credentials.Delete(secondHTTP.URL)
	})
	var output bytes.Buffer
	invoke := func(input string, args ...string) error {
		output.Reset()
		return runWithInput(t.Context(), append([]string{"--config", file}, args...), strings.NewReader(input), &output)
	}
	if err := invoke(static, "--server", "first", "login", firstHTTP.URL, "--token-stdin"); err != nil {
		t.Fatal("service account CLI login failed")
	}
	if bytes.Contains(output.Bytes(), []byte(static)) {
		t.Fatal("login printed its credential")
	}
	if err := invoke("", "user", "invite", "member"); err != nil {
		t.Fatal("admin CLI invitation failed")
	}
	var invitation auth.Invitation
	if json.Unmarshal(output.Bytes(), &invitation) != nil || invitation.Code == "" {
		t.Fatal("invitation response is incomplete")
	}
	if err := invoke("", "login", invitation.Link); err != nil {
		t.Fatal("CLI invitation login failed")
	}
	if bytes.Contains(output.Bytes(), []byte(invitation.Code)) || bytes.Contains(output.Bytes(), []byte(invitation.Link)) {
		t.Fatal("login printed invitation contents")
	}
	if err := invoke("", "login", invitation.Link); err == nil || !strings.Contains(err.Error(), "invite_invalid") {
		t.Fatal("CLI accepted reused invitation")
	}
	secondInvite, err := second.Auth.Invite(t.Context(), auth.Identity{LocalAdmin: true}, "member", "")
	if err != nil {
		t.Fatal("second invitation failed")
	}
	if err = invoke("", "--server", "second", "login", secondInvite.Link); err != nil {
		t.Fatal("second server CLI login failed")
	}
	firstToken, err := credentials.Get(firstHTTP.URL)
	if err != nil {
		t.Fatal("first credential unavailable")
	}
	secondToken, err := credentials.Get(secondHTTP.URL)
	if err != nil || firstToken == secondToken {
		t.Fatal("server credentials were mixed")
	}
	request := httptest.NewRequest(http.MethodGet, "/v2/me", nil)
	request.Header.Set("Authorization", "Bearer "+firstToken)
	if _, err = second.Auth.Authenticate(request); err == nil {
		t.Fatal("second server accepted first server session")
	}
	for _, name := range []string{"first", "second"} {
		if err = invoke("", "--server", name, "whoami"); err != nil {
			t.Fatal("selected server whoami failed")
		}
		var user model.User
		if json.Unmarshal(output.Bytes(), &user) != nil || user.ID != "member" || user.Projects["example"] != "viewer" {
			t.Fatal("selected server returned incorrect identity")
		}
	}
	resolved, err := config.LoadClient(config.Options{Args: []string{"--config", file}, Environ: []string{}})
	if err != nil || len(resolved.Config.Servers) != 2 || resolved.Config.DefaultServer != "first" {
		t.Fatal("server registration or default changed incorrectly")
	}
	selected, err := resolved.Config.Selected()
	if err != nil || selectProject(t.Context(), &selected) != nil || selected.Project != "example" {
		t.Fatal("single accessible project was not selected")
	}
	if err = invoke("", "--server", "first", "auth", "invite"); err != nil {
		t.Fatal("self invitation CLI failed")
	}
	if err = invoke("", "--server", "first", "logout"); err != nil {
		t.Fatal("CLI logout failed")
	}
	if _, err = first.Auth.Authenticate(request); err == nil {
		t.Fatal("logout did not revoke server session")
	}
	if _, err = credentials.Get(firstHTTP.URL); err == nil {
		t.Fatal("logout retained local credential")
	}
	if err = invoke("", "--server", "second", "whoami"); err != nil {
		t.Fatal("logout affected another server")
	}
	paths, err := filepath.Glob(filepath.Join(credentials.Directory, "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatal("unexpected credential record count")
	}
	data, err := os.ReadFile(paths[0])
	info, statErr := os.Stat(paths[0])
	if err != nil || statErr != nil || info.Mode().Perm() != 0600 {
		t.Fatal("credential record permissions differ")
	}
	var record struct {
		Backend string `json:"backend"`
		Token   string `json:"token"`
	}
	if json.Unmarshal(data, &record) != nil || record.Backend != "keyring" && record.Backend != "file" {
		t.Fatal("credential storage backend is invalid")
	}
	if record.Backend == "keyring" && record.Token != "" || record.Backend == "file" && record.Token != secondToken {
		t.Fatal("credential storage contents differ from selected backend")
	}
	t.Logf("real credential backend=%s; two independent HTTP services and same project ID verified", record.Backend)
}

func TestInvitationAddressNormalization(t *testing.T) {
	for _, item := range []struct{ input, server, code string }{
		{"HTTPS://EXAMPLE.ORG:443/", "https://example.org", ""},
		{"http://EXAMPLE.ORG:80/vex/i/abcd-efgh", "http://example.org/vex", "abcd-efgh"},
		{"http://[::1]:80/", "http://[::1]", ""},
	} {
		server, code, err := invitationAddress(item.input)
		if err != nil || server != item.server || code != item.code {
			t.Fatal("server or invitation URL normalization failed")
		}
	}
	for _, input := range []string{"https://user@example.org", "https://example.org/i/code?token=anything", "https://example.org/i/code/more"} {
		if _, _, err := invitationAddress(input); err == nil {
			t.Fatal("unsafe invitation URL accepted")
		}
	}
}
