//go:build integration

package build

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"testing"

	"github.com/imdomestic/vex/controller/internal/filehash"
)

func TestRealNeoForgeMetadataAndEmbeddedLibraries(t *testing.T) {
	path := os.Getenv("VEX_IT_NEOFORGE_JAR")
	if path == "" {
		t.Skip("requires VEX_IT_NEOFORGE_JAR from an actual NeoForge universal release")
	}
	ids, err := JarModIDs(path, "neoforge")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"neoforge"}) {
		t.Fatalf("unexpected release mod IDs: %v", ids)
	}
	if _, err = JarModIDs(path, "fabric"); err == nil {
		t.Fatal("NeoForge release was accepted as Fabric metadata")
	}
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, present, err := jarMetadata(&reader.Reader, "META-INF/jarjar/metadata.json")
	if err != nil || !present {
		t.Fatal("release lacks actual JarJar metadata")
	}
	var metadata struct {
		Jars []struct {
			Path string `json:"path"`
		} `json:"jars"`
	}
	if err = json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	if len(metadata.Jars) == 0 {
		t.Fatal("release contains no embedded dependencies")
	}
	for _, entry := range metadata.Jars {
		file, err := reader.Open(entry.Path)
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(file)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatal("cannot read actual embedded JAR")
		}
		embedded, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
		if err != nil {
			t.Fatal(err)
		}
		libraries := map[string]bool{}
		if err = collectModIDs(embedded, "neoforge", libraries, 1, false); err != nil {
			t.Fatal(err)
		}
		if len(libraries) != 0 {
			t.Fatal("ordinary Java library incorrectly created a Minecraft mod ID")
		}
	}
	hashes, err := filehash.File(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual NeoForge release sha256=%s size=%d; TOML mod ID and %d embedded library JARs passed", hashes.SHA256, hashes.Size, len(metadata.Jars))
}
