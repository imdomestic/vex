package build

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/imdomestic/vex/controller/internal/model"
)

func TestClientFingerprintCanonicalSet(t *testing.T) {
	minecraft, loader := "26.3", "0.19.5"
	one, two := strings.Repeat("1", 128), strings.Repeat("2", 128)
	value := model.Build{Minecraft: &minecraft, Loader: "fabric", LoaderVersion: &loader, Files: []model.BuildFile{
		{Name: "two.jar", Side: "both", SHA512: two},
		{Name: "one.jar", Side: "client", SHA512: one},
		{Name: "duplicate.jar", Side: "client", SHA512: one},
		{Name: "agent.jar", Side: "server", SHA512: strings.Repeat("3", 128)},
	}}
	actual, err := ClientFingerprint(value)
	if err != nil {
		t.Fatal(err)
	}
	canonical := `{"minecraft":"26.3","loader":"fabric","loaderVersion":"0.19.5","sha512":["` + one + `","` + two + `"]}`
	expected := sha256.Sum256([]byte(canonical))
	if actual != hex.EncodeToString(expected[:]) {
		t.Fatalf("canonical mismatch: %s", actual)
	}
	if actual != "e6913c10065a3d1efd42dec55173b7cf8373a9299f438b601611b45ab128d3a9" {
		t.Fatal("cross-language fingerprint vector differs")
	}
	value.Files = []model.BuildFile{{Name: "renamed.jar", Side: "both", SHA512: one}, {Name: "second.jar", Side: "client", SHA512: two}}
	second, err := ClientFingerprint(value)
	if err != nil || second != actual {
		t.Fatal("filename, duplicate or server-only files changed the fingerprint")
	}
	value.Files[0].SHA512 = strings.ToUpper(strings.Repeat("a", 128))
	if _, err = ClientFingerprint(value); err == nil {
		t.Fatal("noncanonical checksum accepted")
	}
}
