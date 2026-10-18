package auth

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
)

func TestGitHubSubjectCreatesRevocableLocalSession(t *testing.T) {
	directory := t.TempDir()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(directory, "signing-key")
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(filepath.Join(directory, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	cfg := config.Defaults()
	cfg.Auth.SigningKeyFile = keyPath
	cfg.Users = map[string]config.User{"reader": {Identities: []string{"github:149769708"}}}
	authentication, err := NewWithSessions(t.Context(), cfg, database)
	if err != nil {
		t.Fatal(err)
	}
	login, err := authentication.issueSubject(t.Context(), Subject{Provider: "github", Subject: "149769708"}, "Vex integration")
	if err != nil || login.User.ID != "reader" || login.Token == "" {
		t.Fatal("linked GitHub identity did not create a session", err)
	}
	identity, err := authentication.authenticateSession(t.Context(), login.Token)
	if err != nil || identity.User != "reader" {
		t.Fatal("issued session did not authenticate", err)
	}
	records, err := store.All[sessionRecord](t.Context(), authentication.platform, "auth_session")
	if err != nil || len(records) != 1 || records[0].TokenSHA256 != tokenDigest(login.Token) || records[0].TokenSHA256 == login.Token {
		t.Fatal("session storage must retain only the local token digest", err)
	}
	if _, err = authentication.issueSubject(t.Context(), Subject{Provider: "github", Subject: "1"}, "Vex integration"); err == nil {
		t.Fatal("unlinked identity was accepted")
	} else {
		var failure *model.Error
		if !errors.As(err, &failure) || failure.Code != "account_not_linked" {
			t.Fatal("unlinked identity did not receive account_not_linked")
		}
	}
	records, err = store.All[sessionRecord](t.Context(), authentication.platform, "auth_session")
	if err != nil || len(records) != 1 {
		t.Fatal("unlinked identity changed persisted sessions", err)
	}
	if err = authentication.Revoke(t.Context(), "reader", records[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = authentication.authenticateSession(t.Context(), login.Token); err == nil {
		t.Fatal("revoked GitHub login session still authenticates")
	}
	records, err = store.All[sessionRecord](t.Context(), authentication.platform, "auth_session")
	if err != nil || len(records) != 0 {
		t.Fatal("revoked session remains in storage", err)
	}
}
