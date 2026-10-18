package build

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/gitmirror"
	"github.com/imdomestic/vex/controller/internal/manifest"
	"github.com/pelletier/go-toml/v2"
)

func TestJobRejectsCrossProjectPathsAndCredentialFields(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "projects", "alpha")
	id := "fabric-123456789abc"
	work := filepath.Join(project, "work", id, "attempt-1")
	job := Job{
		Version: 2, Project: "alpha", BuildID: id, Attempt: 1,
		Worktree: filepath.Join(work, "source"), TempDir: filepath.Join(work, ".work"),
		GradleHome: filepath.Join(project, "cache", "gradle"),
		JDKHome:    filepath.Join(root, "jdk"), LockFile: filepath.Join(root, "build.lock"),
		Loader: "fabric", Timeout: "5m",
		Manifest: manifest.Document{Schema: 1, Minecraft: manifest.Minecraft{Version: "26.3", Java: 25}, Loaders: map[string]manifest.Loader{
			"fabric": {LoaderVersion: "0.19.5", Build: []string{"./gradlew", "assemble"}, Files: []manifest.File{{Glob: "build/libs/*.jar", Side: "both"}}},
		}},
	}
	path := filepath.Join(root, "jobs", "alpha--"+id+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	decoded, err := readJob(path)
	if err != nil || !reflect.DeepEqual(decoded, job) {
		t.Fatal("valid project job did not round trip")
	}
	foreign := job
	foreign.GradleHome = filepath.Join(root, "projects", "beta", "cache", "gradle")
	if err = foreign.validate(path); err == nil {
		t.Fatal("cross-project Gradle cache accepted")
	}
	foreign = job
	foreign.Project = "beta"
	if err = foreign.validate(path); err == nil {
		t.Fatal("job file accepted another project identity")
	}
	foreign = job
	foreign.Attempt++
	if err = foreign.validate(path); err == nil {
		t.Fatal("job accepted another attempt's worktree")
	}
	foreign = job
	foreign.Worktree = filepath.Join(filepath.Dir(work), "source")
	if err = foreign.validate(path); err == nil {
		t.Fatal("job accepted a worktree shared across attempts")
	}
	var document map[string]any
	if err = json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	value := hex.EncodeToString(secret)
	document["token"] = value
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readJob(path); err == nil || strings.Contains(err.Error(), value) {
		t.Fatal("worker accepted a credential field or exposed its value")
	}
}

func TestHostJobUsesActualCommitManifestWithoutLegacyFields(t *testing.T) {
	m, _ := realManager(t)
	doc := manifest.Document{Schema: 1, Minecraft: manifest.Minecraft{Version: "26.3", Java: 25}, Loaders: map[string]manifest.Loader{
		"fabric": {LoaderVersion: "0.19.5", Build: []string{"./gradlew", "assemble", "--no-daemon"}, Files: []manifest.File{{Glob: "build/libs/*.jar", Side: "both"}}},
	}}
	encoded, err := toml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(m.cfg.Git.Remote, "vex.toml"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	repositoryCommand(t, m.cfg.Git.Remote, "add", "vex.toml")
	repositoryCommand(t, m.cfg.Git.Remote, "commit", "-m", "declare host build command")
	sha := repositoryCommand(t, m.cfg.Git.Remote, "rev-parse", "HEAD")
	if err = m.mirror.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	m.cfg.Runtimes = map[string]config.JavaRuntime{"java25": {JDKHome: t.TempDir()}}
	if m.cfg.Minecraft.Version != "" || m.cfg.Minecraft.Java != 0 || m.cfg.Loaders["fabric"].GradleTask != "" || m.cfg.Loaders["fabric"].ArtifactGlob != "" || m.cfg.Loaders["fabric"].LoaderVersionProperty != "" {
		t.Fatal("test configuration unexpectedly contains legacy target fields")
	}
	actual, err := m.Manifest(t.Context(), sha)
	if err != nil || actual.Minecraft != doc.Minecraft || actual.Loaders["fabric"].LoaderVersion != doc.Loaders["fabric"].LoaderVersion || !reflect.DeepEqual(actual.Loaders["fabric"].Build, doc.Loaders["fabric"].Build) || len(actual.Loaders["fabric"].Files) != 1 || actual.Loaders["fabric"].Files[0].Glob != "build/libs/*.jar" || actual.Loaders["fabric"].Files[0].Side != "both" {
		t.Fatal("actual commit manifest differs", err)
	}
	value, _, err := m.Request(t.Context(), sha, "fabric", false)
	if err != nil {
		t.Fatal(err)
	}
	job, err := m.workerJob(value, actual, m.cfg.Runtimes["java25"].JDKHome, filepath.Join(m.workDir(value), "source"))
	if err != nil || !reflect.DeepEqual(job.Manifest, actual) || job.JDKHome != m.cfg.Runtimes["java25"].JDKHome {
		t.Fatal("worker job did not use commit manifest and configured runtime", err)
	}
	selected := actual.Loaders["fabric"]
	selected.Build = nil
	actual.Loaders["fabric"] = selected
	if _, err = m.workerJob(value, actual, job.JDKHome, job.Worktree); err == nil {
		t.Fatal("host job without a manifest command accepted")
	}
}

func TestProjectBuildCacheAndAttemptIsolation(t *testing.T) {
	a, commits := realManager(t)
	scoped, err := a.db.WithProject("beta")
	if err != nil {
		t.Fatal(err)
	}
	cfg := a.cfg
	cfg.ID = "beta"
	cfg.DataDir = filepath.Join(cfg.Server.DataDir, "projects", "beta")
	mirror, err := gitmirror.New(cfg.Git.Remote, filepath.Join(cfg.DataDir, "git.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = mirror.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	b, err := New(cfg, scoped, mirror)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := a.Request(t.Context(), commits[0], "fabric", false)
	if err != nil {
		t.Fatal(err)
	}
	second, created, err := b.Request(t.Context(), commits[0], "fabric", false)
	if err != nil || !created || second.Attempt != 1 || second.Project != "beta" || first.Project != "alpha" || first.ID != second.ID || a.attemptDir(first) == b.attemptDir(second) {
		t.Fatal("same commit reused another project's cache or attempt counter")
	}
	b.cfg.Trust = "untrusted"
	_, _, err = b.Request(t.Context(), commits[1], "fabric", false)
	requireCode(t, err, "sandbox_unavailable")
}

func TestBuildFileHashesAndProjectRelativePath(t *testing.T) {
	m, commits := realManager(t)
	value, _, err := m.Request(t.Context(), commits[0], "fabric", false)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(m.attemptDir(value), "artifact.jar")
	if err = os.WriteFile(path, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := m.buildFile(path, value, "both")
	if err != nil {
		t.Fatal(err)
	}
	if file.SHA1 != "a9993e364706816aba3e25717850c26c9cd0d89d" || file.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" || file.SHA512 != "ddaf35a193617abacc417349ae20413112e6fa4e89a97ea20a9eeee64b55d39a2192992a274fc1a836ba3c23a3feebbd454d4423643ce80e2a9ac94fa54ca49f" || file.Size != 3 {
		t.Fatal("standard digest vectors differ")
	}
	if file.Path != "builds/"+value.ID+"/attempt-1/artifact.jar" || file.URL != "/v2/projects/alpha/builds/"+value.ID+"/files/artifact.jar" {
		t.Fatal("build file path is not project-relative")
	}
}
