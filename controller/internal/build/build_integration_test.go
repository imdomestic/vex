//go:build integration

package build

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/pelletier/go-toml/v2"
)

func TestLiveBuildManifestDownloadsAndCache(t *testing.T) {
	base, usersPath, sha := os.Getenv("CHORUS_IT_URL"), os.Getenv("CHORUS_IT_USERS_FILE"), os.Getenv("CHORUS_IT_BUILD_SHA")
	if base == "" || usersPath == "" || sha == "" {
		t.Skip("requires CHORUS_IT_URL, CHORUS_IT_USERS_FILE and CHORUS_IT_BUILD_SHA")
	}
	usersData, err := os.ReadFile(usersPath)
	if err != nil {
		t.Fatal(err)
	}
	var users struct {
		Users map[string]string `toml:"users"`
	}
	if err = toml.Unmarshal(usersData, &users); err != nil {
		t.Fatal("invalid users TOML")
	}
	token := users.Users["linwhite"]
	if token == "" {
		t.Fatal("linwhite user token is required")
	}
	client := &http.Client{Timeout: time.Minute}
	request := func(method, path string, body any, authenticated bool) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = bytes.NewReader(data)
		}
		req, err := http.NewRequest(method, strings.TrimRight(base, "/")+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		if authenticated {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 256*1024*1024))
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, data
	}
	id := "fabric-" + sha[:12]
	status, data := request(http.MethodGet, "/v1/builds/"+id, nil, true)
	if status != http.StatusOK {
		t.Fatalf("GET build status %d: %s", status, data)
	}
	var manifest model.Build
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ID != id || manifest.SHA != sha || manifest.Status != "ready" || manifest.Attempt < 1 || manifest.Minecraft == nil || manifest.LoaderVersion == nil || manifest.Java == nil || manifest.BuiltAt == nil || manifest.StartedAt == nil || manifest.FinishedAt == nil || manifest.DurationSec == nil || *manifest.DurationSec <= 0 || len(manifest.Files) < 2 || manifest.LastError != nil {
		t.Fatalf("incomplete ready manifest: %+v", manifest)
	}
	if manifest.Log != "/v1/builds/"+id+"/log" {
		t.Fatal("unexpected log URL", manifest.Log)
	}
	modIDs := map[string]bool{}
	for _, file := range manifest.Files {
		if filepath.Base(file.Name) != file.Name {
			t.Fatal("invalid manifest filename")
		}
		status, content := request(http.MethodGet, file.URL, nil, true)
		if status != http.StatusOK {
			t.Fatalf("download %s status %d", file.Name, status)
		}
		digest := sha256.Sum256(content)
		if int64(len(content)) != file.Size || hex.EncodeToString(digest[:]) != file.SHA256 {
			t.Fatalf("download integrity mismatch: %s", file.Name)
		}
		path := filepath.Join(t.TempDir(), file.Name)
		if err = os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		ids, err := JarModIDs(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, modID := range ids {
			modIDs[modID] = true
		}
		status, alias := request(http.MethodGet, "/v1/builds/"+id+"/files/"+file.Name, nil, true)
		if status != http.StatusOK || !bytes.Equal(alias, content) {
			t.Fatalf("build file alias mismatch: %s status %d", file.Name, status)
		}
		status, _ = request(http.MethodGet, file.URL, nil, false)
		if status != http.StatusUnauthorized {
			t.Fatalf("unsigned anonymous download returned %d", status)
		}
	}
	observed := make([]string, 0, len(modIDs))
	for modID := range modIDs {
		observed = append(observed, modID)
	}
	sort.Strings(observed)
	if !reflect.DeepEqual(observed, manifest.ModIDs) || !modIDs["chorus"] || !modIDs["fabric-api"] {
		t.Fatalf("modIds differ from real jar metadata: %v != %v", observed, manifest.ModIDs)
	}
	status, data = request(http.MethodGet, manifest.Log, nil, true)
	if status != http.StatusOK || !bytes.Contains(data, []byte("BUILD SUCCESSFUL")) {
		t.Fatalf("successful Gradle log missing, HTTP %d", status)
	}
	for range 3 {
		status, data = request(http.MethodPost, "/v1/builds", map[string]any{"sha": sha, "loader": "fabric"}, true)
		var cached model.Build
		if err = json.Unmarshal(data, &cached); err != nil {
			t.Fatal(err)
		}
		if status != http.StatusOK || !reflect.DeepEqual(cached, manifest) {
			t.Fatalf("cache request changed published attempt, HTTP %d", status)
		}
	}
	status, data = request(http.MethodGet, "/v1/rooms", nil, true)
	if status != http.StatusOK {
		t.Fatalf("GET rooms HTTP %d", status)
	}
	var rooms []model.Room
	if err = json.Unmarshal(data, &rooms); err != nil {
		t.Fatal(err)
	}
	for _, room := range rooms {
		if room.BuildID != id || room.State == "closed" {
			continue
		}
		status, data = request(http.MethodPost, "/v1/builds", map[string]any{"sha": sha, "loader": "fabric", "force": true}, true)
		var failure struct {
			Error model.Error `json:"error"`
		}
		if err = json.Unmarshal(data, &failure); err != nil {
			t.Fatal(err)
		}
		if status != http.StatusConflict || failure.Error.Code != "conflict" {
			t.Fatalf("referenced force returned HTTP %d: %s", status, data)
		}
		break
	}
	t.Logf("verified build=%s attempt=%d files=%d modIds=%d; all bytes, cache requests and registered-room protection checked", id, manifest.Attempt, len(manifest.Files), len(manifest.ModIDs))
}

func TestRealHopPublicationPreservesIndependentContent(t *testing.T) {
	jar := os.Getenv("CHORUS_IT_HOP_JAR")
	if jar == "" {
		t.Skip("requires CHORUS_IT_HOP_JAR from the real Gradle build")
	}
	m, _ := realManager(t)
	directory := filepath.Join(m.cfg.Server.DataDir, "mods", "chorus-hop", "26.3")
	if err := os.MkdirAll(directory, 0750); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(directory, filepath.Base(jar))
	if err := copyFile(jar, source); err != nil {
		t.Fatal(err)
	}
	entry, published, err := m.hopArtifact("26.3")
	if err != nil || entry == nil {
		t.Fatalf("publish real hop: %v", err)
	}
	if entry.URL != "/v1/mods/chorus-hop/26.3/"+entry.SHA256+".jar" || published != filepath.Join(directory, "published", entry.SHA256+".jar") {
		t.Fatal("hop URL is not fixed by its digest")
	}
	second, secondPath, err := m.hopArtifact("26.3")
	if err != nil || !reflect.DeepEqual(entry, second) || published != secondPath {
		t.Fatalf("repeat publication: %+v %v", second, err)
	}
	alias := filepath.Join(t.TempDir(), entry.Name)
	if err = copyFile(published, alias); err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	publishedInfo, err := os.Stat(published)
	if err != nil {
		t.Fatal(err)
	}
	aliasInfo, err := os.Stat(alias)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(sourceInfo, publishedInfo) || os.SameFile(publishedInfo, aliasInfo) {
		t.Fatal("configured, published and attempt files must have independent storage")
	}
	if err = os.Remove(source); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{published, alias} {
		digest, size, err := hashFile(path)
		if err != nil || digest != entry.SHA256 || size != entry.Size {
			t.Fatalf("configured source removal changed %s: %v", path, err)
		}
	}
	if err = os.Chmod(published, 0640); err != nil {
		t.Fatal(err)
	}
	if err = os.Truncate(published, entry.Size-1); err != nil {
		t.Fatal(err)
	}
	if err = copyFile(jar, source); err != nil {
		t.Fatal(err)
	}
	if _, _, err = m.hopArtifact("26.3"); err == nil {
		t.Fatal("accepted a damaged published hop artifact")
	}
}
