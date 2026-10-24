package build

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/manifest"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/pelletier/go-toml/v2"
)

func writeBundleJar(t *testing.T, filename string, comment ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		t.Fatal(err)
	}
	output, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	if err = writer.SetComment(strings.Join(comment, "")); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ name, data string }{
		{"fabric.mod.json", `{"schemaVersion":1,"id":"bundle_test","version":"1.0.0","environment":"*"}`},
		{"META-INF/neoforge.mods.toml", "modLoader = 'javafml'\nloaderVersion = '[1,)'\nlicense = 'MIT'\n[[mods]]\nmodId = 'bundle_test'\nversion = '1.0.0'\n"},
	} {
		file, err := writer.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(file, entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err = errors.Join(writer.Close(), output.Close()); err != nil {
		t.Fatal(err)
	}
}

func realBundle(t *testing.T) (*Manager, string, Bundle, string, string) {
	t.Helper()
	m, _ := realManager(t)
	m.cfg.Build.Source = "ci"
	m.cfg.Trust = "untrusted"
	m.cfg.GitHub.Repository = "test/project"
	m.cfg.CI.Workflow = "build.yml"
	m.cfg.Runtimes = map[string]config.JavaRuntime{"java25": {}}
	m.cfg.Loaders["neoforge"] = config.Loader{Enabled: true}
	doc := manifest.Document{
		Schema: 1, Minecraft: manifest.Minecraft{Version: "26.3", Java: 25},
		World: manifest.World{ExtraRootEntries: []string{"project-state.json"}}, Rooms: manifest.Rooms{Memory: "4G"},
		Loaders: map[string]manifest.Loader{
			"fabric":   {LoaderVersion: "0.19.5", Files: []manifest.File{{Glob: "fabric/**/*.jar", Exclude: []string{"*-sources.jar"}, Side: "both"}}},
			"neoforge": {LoaderVersion: "26.3.0.45-beta", Files: []manifest.File{{Glob: "neoforge/**/*.jar", Side: "both"}}},
		},
	}
	data, err := toml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(m.cfg.Git.Remote, "vex.toml"), data, 0600); err != nil {
		t.Fatal(err)
	}
	repositoryCommand(t, m.cfg.Git.Remote, "add", "vex.toml")
	repositoryCommand(t, m.cfg.Git.Remote, "commit", "-m", "declare runtime artifacts")
	sha := repositoryCommand(t, m.cfg.Git.Remote, "rev-parse", "HEAD")
	if err = m.mirror.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fabric", "neoforge"} {
		writeBundleJar(t, filepath.Join(m.cfg.Git.Remote, name, "build", "libs", "bundle.jar"))
	}
	writeBundleJar(t, filepath.Join(m.cfg.Git.Remote, "fabric", "build", "libs", "bundle-sources.jar"))
	directory := filepath.Join(t.TempDir(), "bundle")
	bundle, err := CollectBundle(t.Context(), doc, m.cfg.Git.Remote, directory, doc.LoaderNames())
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "bundle.zip")
	if err = WriteBundleZIP(bundle, directory, archive); err != nil {
		t.Fatal(err)
	}
	return m, sha, bundle, directory, archive
}

func TestRealCIBundleAllLoadersAttemptsAndAuthoritativeIdentity(t *testing.T) {
	m, sha, _, _, archive := realBundle(t)
	queued, created, err := m.Request(t.Context(), sha, "fabric", false)
	if err != nil || !created || queued.Source.Kind != "ci" || queued.Status != "queued" {
		t.Fatalf("CI waiting record: %+v %v", queued, err)
	}
	if err = m.step(t.Context()); err != nil {
		t.Fatal("CI waiting loop attempted host execution", err)
	}
	input := CIImport{SHA: sha, RunID: 100, RunAttempt: 1, Repository: m.cfg.GitHub.Repository, HeadRepository: m.cfg.GitHub.Repository, Workflow: m.cfg.CI.Workflow, Event: "pull_request", HTMLURL: "https://github.com/test/project/actions/runs/100", PRNumber: 7, BundlePath: archive}
	values, created, err := m.ImportCI(t.Context(), input)
	if err != nil || !created || len(values) != 2 {
		t.Fatalf("import: %+v %t %v", values, created, err)
	}
	for _, value := range values {
		if value.Attempt != 1 || value.Status != "ready" || value.SHA != sha || value.Source.PRNumber != 7 || value.Source.RunID != 100 || value.Source.RunAttempt != 1 || value.Source.Event != input.Event || value.Source.HTMLURL != input.HTMLURL || len(value.Files) != 1 || value.Files[0].Origin != "build" || value.ClientFingerprint == "" {
			t.Fatalf("incomplete CI build: %+v", value)
		}
		file, err := m.OpenFile(t.Context(), value.ID, value.Files[0].Name)
		if err != nil {
			t.Fatal(err)
		}
		if err = file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	repeated, created, err := m.ImportCI(t.Context(), input)
	if err != nil || created || !reflect.DeepEqual(values, repeated) {
		t.Fatal("same CI run did not remain idempotent", err)
	}
	input.RunAttempt = 2
	values, created, err = m.ImportCI(t.Context(), input)
	if err != nil || !created || values[0].Attempt != 2 || values[1].Attempt != 2 {
		t.Fatal("CI rerun did not allocate new attempts", err)
	}
	input.RunID, input.RunAttempt = 99, 5
	stale, created, err := m.ImportCI(t.Context(), input)
	if err != nil || created || len(stale) != 2 || stale[0].Attempt != 2 {
		t.Fatal("older CI run replaced current content", err)
	}
	input.RunID, input.RunAttempt, input.PRNumber = 100, 1, 8
	if repeated, fresh, err := m.ImportCI(t.Context(), input); err != nil || fresh || len(repeated) != 2 {
		t.Fatal("additional PR association changed a CI run identity", err)
	}
	input.HeadRepository = "another/project"
	if _, _, err = m.ImportCI(t.Context(), input); err == nil {
		t.Fatal("changed authoritative head repository accepted")
	}
	input.HeadRepository = m.cfg.GitHub.Repository
	_, _, err = m.Request(t.Context(), sha, "fabric", true)
	requireCode(t, err, "conflict")
	input.RunID, input.RunAttempt, input.PRNumber = 101, 1, 7
	if err = m.db.Put(t.Context(), "room", "reserved", model.Room{ID: "reserved", BuildID: "fabric-" + sha[:12], State: "stopped"}); err != nil {
		t.Fatal(err)
	}
	if err = m.db.Put(t.Context(), "room", "other", model.Room{ID: "other", BuildID: "neoforge-" + sha[:12], State: "ready"}); err != nil {
		t.Fatal(err)
	}
	input.PRNumber = 9
	reused, created, err := m.ImportCI(t.Context(), input)
	if err != nil || created || len(reused) != 2 || reused[0].Attempt != 2 || reused[0].Source.RunID != 100 || reused[0].Source.PRNumber != 7 {
		t.Fatal("identical live CI content did not preserve its existing attempt and source", err)
	}
	if repeated, fresh, err := m.ImportCI(t.Context(), input); err != nil || fresh || !reflect.DeepEqual(repeated, reused) {
		t.Fatal("reused CI run lost its independent authoritative identity", err)
	}
	writeBundleJar(t, filepath.Join(m.cfg.Git.Remote, "fabric", "build", "libs", "bundle.jar"), "different build bytes")
	doc, err := m.commitManifest(t.Context(), sha)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "changed")
	changed, err := CollectBundle(t.Context(), doc, m.cfg.Git.Remote, directory, doc.LoaderNames())
	if err != nil {
		t.Fatal(err)
	}
	input.RunID, input.BundlePath = 102, filepath.Join(t.TempDir(), "changed.zip")
	if err = WriteBundleZIP(changed, directory, input.BundlePath); err != nil {
		t.Fatal(err)
	}
	_, _, err = m.ImportCI(t.Context(), input)
	requireCode(t, err, "conflict")
}

func TestBundleRejectsArchiveBoundariesAndUnverifiedBytes(t *testing.T) {
	_, _, bundle, directory, archive := realBundle(t)
	if _, err := UnpackBundle(archive, filepath.Join(t.TempDir(), "contents")); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"traversal", "duplicate", "case", "symlink", "extra", "checksum", "agent", "unknown-json"} {
		t.Run(kind, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "unsafe.zip")
			output, err := os.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			writer := zip.NewWriter(output)
			write := func(header *zip.FileHeader, data []byte) {
				t.Helper()
				entry, err := writer.CreateHeader(header)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = entry.Write(data); err != nil {
					t.Fatal(err)
				}
			}
			encoded := metadata
			if kind == "agent" || kind == "unknown-json" {
				var changed map[string]any
				if err = json.Unmarshal(metadata, &changed); err != nil {
					t.Fatal(err)
				}
				if kind == "unknown-json" {
					changed["sha"] = strings.Repeat("a", 40)
				} else {
					changed["loaders"].(map[string]any)["fabric"].(map[string]any)["files"].([]any)[0].(map[string]any)["origin"] = "agent"
				}
				encoded, err = json.Marshal(changed)
				if err != nil {
					t.Fatal(err)
				}
			}
			write(&zip.FileHeader{Name: "vex-build.json"}, encoded)
			for _, build := range bundle.LoaderBuilds() {
				for _, file := range build.Files {
					data, err := os.ReadFile(filepath.Join(directory, filepath.FromSlash(file.Path)))
					if err != nil {
						t.Fatal(err)
					}
					if kind == "checksum" {
						data[0] ^= 1
					}
					header := &zip.FileHeader{Name: file.Path}
					if kind == "symlink" {
						header.SetMode(os.ModeSymlink | 0777)
					}
					write(header, data)
					if kind == "duplicate" {
						write(&zip.FileHeader{Name: file.Path}, data)
					}
					if kind == "case" {
						write(&zip.FileHeader{Name: strings.ToUpper(file.Path)}, data)
					}
				}
			}
			if kind == "traversal" {
				write(&zip.FileHeader{Name: "../escape.jar"}, []byte("reject"))
			}
			if kind == "extra" {
				write(&zip.FileHeader{Name: "unlisted.jar"}, []byte("reject"))
			}
			if err = errors.Join(writer.Close(), output.Close()); err != nil {
				t.Fatal(err)
			}
			if _, err = UnpackBundle(name, filepath.Join(t.TempDir(), "contents")); err == nil {
				t.Fatal("unsafe ZIP accepted")
			}
		})
	}
}

func TestActionBundleUsesSameManifestAndActualBytes(t *testing.T) {
	if os.Getenv("VEX_IT_ACTION_GO") == "" {
		t.Skip("requires the independent Action Go executable")
	}
	m, sha, expected, _, _ := realBundle(t)
	command := exec.Command(os.Getenv("VEX_IT_ACTION_GO"), "--root", m.cfg.Git.Remote, "--manifest", "vex.toml", "--output", ".work/action-bundle")
	outputFile := filepath.Join(t.TempDir(), "action-output")
	if err := os.WriteFile(outputFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	command.Env = append(os.Environ(), "GITHUB_OUTPUT="+outputFile)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("actual Go Action: %v %s", err, output)
	}
	directory := filepath.Join(m.cfg.Git.Remote, ".work", "action-bundle")
	data, err := os.ReadFile(filepath.Join(directory, "vex-build.json"))
	if err != nil {
		t.Fatal(err)
	}
	var actual Bundle
	if err = json.Unmarshal(data, &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("Action and controller bundle fields differ: %+v %+v", expected, actual)
	}
	archive := filepath.Join(t.TempDir(), "action.zip")
	if err = WriteBundleZIP(actual, directory, archive); err != nil {
		t.Fatal(err)
	}
	values, created, err := m.ImportCI(t.Context(), CIImport{SHA: sha, RunID: 123, RunAttempt: 1, Repository: m.cfg.GitHub.Repository, Workflow: m.cfg.CI.Workflow, BundlePath: archive})
	if err != nil || !created || len(values) != 2 {
		t.Fatal("Go Action bundle did not import through real Git and SQLite", err)
	}
	if _, err = store.All[model.Build](t.Context(), m.db, "build"); err != nil {
		t.Fatal(err)
	}
}

func TestBundleRejectsDeclaredOversizedZIPFile(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "oversized.zip")
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	if _, err = writer.CreateRaw(&zip.FileHeader{Name: "files/fabric/large.jar", Method: zip.Store, UncompressedSize64: uint64(MaxFileBytes + 1)}); err != nil {
		t.Fatal(err)
	}
	if err = errors.Join(writer.Close(), file.Close()); err != nil {
		t.Fatal(err)
	}
	if _, err = UnpackBundle(filename, filepath.Join(t.TempDir(), "contents")); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatal("oversized ZIP entry accepted", err)
	}
}

func TestCIRejectsActualJarWithoutModMetadata(t *testing.T) {
	m, sha, bundle, directory, _ := realBundle(t)
	selected := bundle.Loaders["fabric"]
	filename := filepath.Join(directory, selected.Files[0].Path)
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("README.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(entry, "A regular ZIP without Minecraft mod metadata."); err != nil {
		t.Fatal(err)
	}
	if err = errors.Join(writer.Close(), file.Close()); err != nil {
		t.Fatal(err)
	}
	actual, err := describeFile(filename, "both")
	if err != nil {
		t.Fatal(err)
	}
	selected.Files[0].Size, selected.Files[0].SHA1, selected.Files[0].SHA256, selected.Files[0].SHA512 = actual.Size, actual.SHA1, actual.SHA256, actual.SHA512
	bundle.Loaders["fabric"] = selected
	archive := filepath.Join(t.TempDir(), "missing-metadata.zip")
	if err = WriteBundleZIP(bundle, directory, archive); err != nil {
		t.Fatal(err)
	}
	if _, _, err = m.ImportCI(t.Context(), CIImport{SHA: sha, RunID: 301, RunAttempt: 1, Repository: m.cfg.GitHub.Repository, Workflow: m.cfg.CI.Workflow, BundlePath: archive}); err == nil {
		t.Fatal("JAR without mod metadata accepted")
	}
	values, err := store.All[model.Build](t.Context(), m.db, "build")
	if err != nil || len(values) != 0 {
		t.Fatal("invalid import published a build", err)
	}
}
