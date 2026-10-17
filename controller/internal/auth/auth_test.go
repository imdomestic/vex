package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
)

func TestSignedPathsAndRoomGeneration(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(key)
	if err := os.WriteFile(filepath.Join(dir, "key"), key, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(token))
	a, err := New(config.Config{
		Auth:     config.Auth{SigningKeyFile: filepath.Join(dir, "key")},
		Users:    map[string]config.User{"operator": {TokenSHA256: hex.EncodeToString(digest[:])}},
		Projects: map[string]config.Project{"alpha": {}, "beta": {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	u, err := a.SignURL("http://localhost", "/v2/projects/alpha/deps/a%20b", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", u, nil)
	if !a.ValidDownload(r) {
		t.Fatal("signed path rejected")
	}
	r.Method = "POST"
	if a.ValidDownload(r) {
		t.Fatal("signature accepted for POST")
	}
	r.Method = "GET"
	r.URL.Path = "/v2/projects/beta/deps/a b"
	r.URL.RawPath = ""
	if a.ValidDownload(r) {
		t.Fatal("signature accepted for another file")
	}
	if _, err = a.SignURL("http://localhost", "/v2/projects/alpha/deps/a", time.Now().Add(-time.Second)); err == nil {
		t.Fatal("expired signature created")
	}
	roomToken, err := a.RoomToken("alpha", "main-fabric", 7, "fabric-123456789012", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("GET", "http://localhost/v2/projects/alpha/rooms/main-fabric", nil)
	r.Header.Set("Authorization", "Bearer "+roomToken)
	identity, ok := a.Identify(r)
	if !ok || identity.Project != "alpha" || identity.Generation != 7 || identity.RoomID != "main-fabric" || identity.BuildID != "fabric-123456789012" || identity.User != "" {
		t.Fatal("room identity not preserved")
	}
	r.Header.Set("Authorization", "Bearer "+token)
	identity, ok = a.Identify(r)
	if !ok || identity.User != "operator" {
		t.Fatal("user authentication failed")
	}
	r.Header.Set("Authorization", "Bearer "+hex.EncodeToString(digest[:]))
	if _, ok = a.Identify(r); ok {
		t.Fatal("stored verifier accepted as a bearer token")
	}
}

func TestRoleVisibilityAndIdentityMapping(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Auth:  config.Auth{SigningKeyFile: keyPath},
		Users: map[string]config.User{"reader": {Identities: []string{"github:123"}}, "admin": {Admin: true}, "writer": {}},
		Projects: map[string]config.Project{
			"alpha": {Members: map[string]config.Role{"reader": config.Viewer, "writer": config.Developer}},
			"beta":  {},
		},
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		user    string
		project string
		role    config.Role
		code    string
	}{
		{"reader", "alpha", config.Viewer, ""},
		{"reader", "alpha", config.Developer, "forbidden"},
		{"reader", "beta", config.Viewer, "not_found"},
		{"writer", "alpha", config.Developer, ""},
		{"writer", "alpha", config.Owner, "forbidden"},
		{"admin", "beta", config.Owner, ""},
		{"admin", "absent", config.Owner, "not_found"},
	} {
		err := a.RequireProject(Identity{User: item.user}, item.project, item.role)
		if item.code == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			var failure *model.Error
			if !errors.As(err, &failure) || failure.Code != item.code {
				t.Fatalf("role permission: %v", err)
			}
		}
	}
	if visible := a.VisibleProjects(Identity{User: "reader"}); len(visible) != 1 || visible[0] != "alpha" {
		t.Fatal("project visibility includes an inaccessible project")
	}
	if visible := a.VisibleProjects(Identity{Project: "alpha", RoomID: "main-fabric"}); len(visible) != 0 {
		t.Fatal("room principal received user project permissions")
	}
	cfg.Users["writer"] = config.User{Identities: []string{"github:123"}}
	if _, err = New(cfg); err == nil {
		t.Fatal("duplicate provider identity accepted")
	}
}
