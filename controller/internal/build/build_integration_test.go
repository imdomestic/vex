//go:build integration

package build

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
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

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
)

func TestLiveBuildManifestDownloadsAndCache(t *testing.T) {
	base, tokenPath, sha, project := os.Getenv("VEX_IT_URL"), os.Getenv("VEX_IT_TOKEN_FILE"), os.Getenv("VEX_IT_BUILD_SHA"), os.Getenv("VEX_IT_PROJECT")
	if base == "" || tokenPath == "" || sha == "" || project == "" {
		t.Skip("requires VEX_IT_URL, VEX_IT_TOKEN_FILE, VEX_IT_BUILD_SHA and VEX_IT_PROJECT")
	}
	if !config.ValidID(project) || len(sha) != 40 {
		t.Fatal("invalid live project or commit")
	}
	loader := os.Getenv("VEX_IT_LOADER")
	if loader == "" {
		loader = "fabric"
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
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
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
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
	prefix := "/v2/projects/" + project
	id := loader + "-" + sha[:12]
	status, data := request(http.MethodGet, prefix+"/builds/"+id, nil, true)
	if status != http.StatusOK {
		t.Fatalf("GET build status %d", status)
	}
	var manifest model.Build
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Project != project || manifest.Source.Kind != "host" || manifest.ID != id || manifest.SHA != sha || manifest.Status != "ready" || manifest.Attempt < 1 || manifest.Minecraft == nil || manifest.LoaderVersion == nil || manifest.Java == nil || manifest.BuiltAt == nil || manifest.StartedAt == nil || manifest.FinishedAt == nil || manifest.DurationSec == nil || *manifest.DurationSec <= 0 || len(manifest.Files) == 0 || manifest.LastError != nil {
		t.Fatal("incomplete ready manifest")
	}
	if manifest.Log != prefix+"/builds/"+id+"/log" {
		t.Fatal("unexpected log URL")
	}
	modIDs := map[string]bool{}
	for _, file := range manifest.Files {
		if filepath.Base(file.Name) != file.Name || !strings.HasPrefix(file.Path, "builds/"+id+"/attempt-") || !strings.HasPrefix(file.URL, prefix+"/builds/"+id+"/files/") {
			t.Fatal("manifest file has an invalid name, relative path or project URL")
		}
		status, content := request(http.MethodGet, file.URL, nil, true)
		if status != http.StatusOK {
			t.Fatalf("download %s status %d", file.Name, status)
		}
		one, two, five := sha1.Sum(content), sha256.Sum256(content), sha512.Sum512(content)
		if int64(len(content)) != file.Size || hex.EncodeToString(one[:]) != file.SHA1 || hex.EncodeToString(two[:]) != file.SHA256 || hex.EncodeToString(five[:]) != file.SHA512 {
			t.Fatalf("download integrity mismatch: %s", file.Name)
		}
		path := filepath.Join(t.TempDir(), file.Name)
		if err = os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		ids, err := JarModIDs(path, manifest.Loader)
		if err != nil {
			t.Fatal(err)
		}
		for _, modID := range ids {
			modIDs[modID] = true
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
	if !reflect.DeepEqual(observed, manifest.ModIDs) {
		t.Fatal("modIds differ from real jar metadata")
	}
	status, data = request(http.MethodGet, manifest.Log, nil, true)
	if status != http.StatusOK || !bytes.Contains(data, []byte("BUILD SUCCESSFUL")) {
		t.Fatalf("successful Gradle log missing, HTTP %d", status)
	}
	for range 3 {
		status, data = request(http.MethodPost, prefix+"/builds", map[string]any{"sha": sha, "loader": loader}, true)
		var cached model.Build
		if err = json.Unmarshal(data, &cached); err != nil {
			t.Fatal(err)
		}
		if status != http.StatusOK || !reflect.DeepEqual(cached, manifest) {
			t.Fatalf("cache request changed published attempt, HTTP %d", status)
		}
	}
	t.Logf("verified project=%s build=%s attempt=%d files=%d modIds=%d; SHA1/SHA256/SHA512 and cache identity passed", project, id, manifest.Attempt, len(manifest.Files), len(manifest.ModIDs))
}

func TestRealAgentBuildFilePreservesIndependentContent(t *testing.T) {
	jar := os.Getenv("VEX_IT_AGENT_JAR")
	if jar == "" {
		t.Skip("requires VEX_IT_AGENT_JAR from the real Gradle build")
	}
	m, commits := realManager(t)
	source := filepath.Join(t.TempDir(), filepath.Base(jar))
	if err := copyFile(jar, source); err != nil {
		t.Fatal(err)
	}
	value, _, err := m.Request(t.Context(), commits[0], "fabric", false)
	if err != nil {
		t.Fatal(err)
	}
	version := "26.3"
	value.Minecraft = &version
	loader := m.cfg.Loaders["fabric"]
	loader.Agent = "fabric-26-3"
	m.cfg.Loaders["fabric"] = loader
	m.cfg.Agents = map[string]config.Agent{"fabric-26-3": {Minecraft: version, Loader: "fabric", Version: "1.0.0", File: source}}
	entry, configured, err := m.agentArtifact(value)
	if err != nil || entry == nil {
		t.Fatalf("inspect real agent: %v", err)
	}
	if entry.URL != m.apiPrefix()+"/builds/"+value.ID+"/files/"+entry.Name {
		t.Fatal("agent is not published through its Build")
	}
	target := filepath.Join(m.attemptDir(value), entry.Name)
	if err = copyFile(configured, target); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(source); err != nil {
		t.Fatal(err)
	}
	actual, err := m.buildFile(target, value, "server")
	if err != nil || !reflect.DeepEqual(*entry, actual) {
		t.Fatal("configured source removal changed the Build file")
	}
	value.Status, value.Files = "ready", []model.BuildFile{actual}
	if err = m.db.Put(t.Context(), "build", value.ID, value); err != nil {
		t.Fatal(err)
	}
	file, err := m.OpenFile(t.Context(), value.ID, actual.Name)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
}
