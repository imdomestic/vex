package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/imdomestic/vex/controller/internal/api"
	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

func TestAdminCommandsOverRealUnixSocket(t *testing.T) {
	directory, err := os.MkdirTemp(os.Getenv("TMPDIR"), "admin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Auth.AdminSocket = filepath.Join(directory, "admin.sock")
	cfg.Auth.SigningKeyFile = filepath.Join(directory, "signing.key")
	if err = os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if unexpected, err := auth.ListenAdminSocket(cfg.Auth.AdminSocket, os.Getegid()); err == nil {
		unexpected.Close()
		t.Fatal("admin socket accepted a parent directory accessible to other users")
	}
	if err = os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	if _, err = rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.Auth.SigningKeyFile, key[:], 0600); err != nil {
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
	listener, err := auth.ListenAdminSocket(cfg.Auth.AdminSocket, os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cfg.Auth.AdminSocket)
	if err != nil || info.Mode().Perm() != 0660 || info.Mode()&os.ModeSocket == 0 {
		t.Fatal("admin socket type or permissions differ")
	}
	if duplicate, err := auth.ListenAdminSocket(cfg.Auth.AdminSocket, os.Getegid()); err == nil {
		duplicate.Close()
		t.Fatal("second listener acquired an active admin socket")
	}
	service := api.New(cfg, map[string]*api.Server{}, identity, nil)
	server := &http.Server{Handler: service.AdminHandler()}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Close()
		<-done
	})
	var output bytes.Buffer
	if err = userCommand(t.Context(), []string{"invite", "new-user", "--socket", cfg.Auth.AdminSocket}, &output); err != nil {
		t.Fatal("admin invitation command failed")
	}
	var invitation auth.Invitation
	if json.Unmarshal(output.Bytes(), &invitation) != nil || invitation.Code == "" {
		t.Fatal("admin invitation response is incomplete")
	}
	login, err := identity.Redeem(t.Context(), invitation.Code, "Unix management test")
	if err != nil || login.User.ID != "new-user" || login.User.Admin || len(login.User.Projects) != 0 {
		t.Fatal("new local identity received unexpected permissions")
	}
	output.Reset()
	if err = userCommand(t.Context(), []string{"sessions", "new-user", "--socket", cfg.Auth.AdminSocket}, &output); err != nil {
		t.Fatal("admin session listing failed")
	}
	var sessions []model.Session
	if json.Unmarshal(output.Bytes(), &sessions) != nil || len(sessions) != 1 {
		t.Fatal("admin session list differs")
	}
	output.Reset()
	if err = userCommand(t.Context(), []string{"sessions", "new-user", "--socket", cfg.Auth.AdminSocket, "--revoke", sessions[0].ID}, &output); err != nil {
		t.Fatal("admin session revocation failed")
	}
	request := httptest.NewRequest(http.MethodGet, "/v2/me", nil)
	request.Header.Set("Authorization", "Bearer "+login.Token)
	if _, err = identity.Authenticate(request); err == nil {
		t.Fatal("admin-revoked session remains usable")
	}
	if bytes.Contains(output.Bytes(), []byte(login.Token)) {
		t.Fatal("session command printed a credential")
	}
}
