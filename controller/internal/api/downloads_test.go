package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/auth"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/rooms"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/worlds"
)

func TestPackCanonicalRouteUsesAuthenticatedWorldStorage(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	projectDB, err := db.WithProject("example")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.ProjectConfig{ID: "example", DataDir: filepath.Join(root, "data")}
	manager, err := worlds.New(cfg, projectDB)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(key)
	digest := sha256.Sum256([]byte(token))
	keyPath := filepath.Join(root, "signing.key")
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	global := config.Config{Auth: config.Auth{SigningKeyFile: keyPath}, Users: map[string]config.User{"integration": {TokenSHA256: hex.EncodeToString(digest[:])}}, Projects: map[string]config.Project{"example": {Members: map[string]config.Role{"integration": config.Viewer}}}}
	identity, err := auth.New(global)
	if err != nil {
		t.Fatal(err)
	}
	api := New(global, map[string]*Server{"example": {Cfg: cfg, DB: projectDB, Rooms: &rooms.Manager{Worlds: manager}}}, identity, nil)
	server := httptest.NewServer(api.Handler())
	defer server.Close()
	path := "/v2/projects/example/packs/spawn/" + strings.Repeat("a", 64) + ".zip"
	signed, err := identity.SignURL(server.URL, path, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		url, token string
		status     int
		code       string
	}{
		{server.URL + path, "", 401, "unauthorized"},
		{server.URL + path, token, 404, "not_found"},
		{signed, "", 404, "not_found"},
		{server.URL + "/v2/projects/example/packs/spawn/invalid.zip", token, 400, "invalid_request"},
		{server.URL + strings.TrimSuffix(path, ".zip") + ".jar", token, 400, "invalid_request"},
	} {
		request, err := http.NewRequest(http.MethodGet, test.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if test.token != "" {
			request.Header.Set("Authorization", "Bearer "+test.token)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var failure struct {
			Error model.Error `json:"error"`
		}
		err = json.NewDecoder(response.Body).Decode(&failure)
		response.Body.Close()
		if err != nil || response.StatusCode != test.status || failure.Error.Code != test.code {
			t.Fatalf("pack route HTTP %d code=%s: %v", response.StatusCode, failure.Error.Code, err)
		}
	}
}
