package migration

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/pelletier/go-toml/v2"
)

func TestTokenMigrationPreservesStaticIdentityWithoutPlaintext(t *testing.T) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret)
	data, err := toml.Marshal(map[string]any{"users": map[string]string{"operator": token}})
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "users.toml")
	if err = os.WriteFile(filename, data, 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := TokenSettings(filename)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(settings, token) {
		t.Fatal("migration exposed a plaintext token")
	}
	var result struct {
		Users map[string]config.User `toml:"users"`
	}
	if err = toml.Unmarshal([]byte(settings), &result); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(token))
	user := result.Users["operator"]
	if user.TokenSHA256 != hex.EncodeToString(digest[:]) || len(user.Identities) != 1 || user.Identities[0] != "static:operator" {
		t.Fatal("migrated static credential or identity differs")
	}
}
