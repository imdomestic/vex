package auth

import (
	"crypto/rand"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSignedPathsAndRoomGeneration(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(key)
	if err := os.WriteFile(filepath.Join(dir, "users.toml"), []byte("[users]\nlinwhite = \""+token+"\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key"), key, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := Load(filepath.Join(dir, "users.toml"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	u, err := a.SignURL("http://localhost", "/v1/deps/a%20b", time.Now().Add(time.Hour))
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
	r.URL.Path = "/v1/deps/other"
	r.URL.RawPath = ""
	if a.ValidDownload(r) {
		t.Fatal("signature accepted for another file")
	}
	u, err = a.SignURL("http://localhost", "/v1/deps/a", time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if a.ValidDownload(httptest.NewRequest("GET", u, nil)) {
		t.Fatal("expired signature accepted")
	}
	roomToken, err := a.RoomToken("main-fabric", 7, "fabric-123456789012", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("GET", "http://localhost/v1/rooms/main-fabric", nil)
	r.Header.Set("Authorization", "Bearer "+roomToken)
	identity, ok := a.Identify(r)
	if !ok || identity.Generation != 7 || identity.RoomID != "main-fabric" || identity.BuildID != "fabric-123456789012" || identity.User != "" {
		t.Fatal("room identity not preserved")
	}
	r.Header.Set("Authorization", "Bearer "+token)
	identity, ok = a.Identify(r)
	if !ok || identity.User != "linwhite" {
		t.Fatal("user authentication failed")
	}
}
