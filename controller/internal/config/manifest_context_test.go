package config_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/gitmirror"
	"github.com/imdomestic/vex/controller/internal/manifest"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/pelletier/go-toml/v2"
)

func TestHostConfigurationUsesCommitManifestWithoutLegacyBuildFields(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(t.Context(), "git", args...)
		command.Dir = source
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("actual Git command failed: %v", err)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Manifest verification")
	git("config", "user.email", "manifest@example.invalid")
	git("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("Manifest configuration verification\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("commit", "-m", "Create project")
	withoutManifest := git("rev-parse", "HEAD")
	document := manifest.Document{Schema: 1, Minecraft: manifest.Minecraft{Version: "26.3", Java: 25}, Loaders: map[string]manifest.Loader{
		"fabric": {LoaderVersion: "0.19.5", Build: []string{"./gradlew", "build", "--no-daemon"}, Files: []manifest.File{{Glob: "build/libs/*.jar", Side: "both"}}},
	}, Rooms: manifest.Rooms{Memory: "4G"}}
	encoded, err := toml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	fallback := filepath.Join(root, "fallback.toml")
	for _, path := range []string{fallback, filepath.Join(source, "vex.toml")} {
		if err = os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "vex.toml")
	git("commit", "-m", "Declare commit build manifest")
	withManifest := git("rev-parse", "HEAD")
	operator := map[string]any{
		"server": map[string]any{"data_dir": filepath.Join(root, "data")},
		"runtimes": map[string]any{"java25": map[string]any{
			"server_image": "itzg/minecraft-server:java25", "jdk_home": filepath.Join(root, "jdk25"),
		}},
		"projects": map[string]any{"example": map[string]any{
			"remote": source, "trust": "trusted", "manifest_fallback": fallback,
			"build":   map[string]any{"source": "host"},
			"loaders": map[string]any{"fabric": map[string]any{"enabled": true}},
		}},
	}
	encoded, err = toml.Marshal(operator)
	if err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(root, "operator.toml")
	if err = os.WriteFile(settings, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	resolved, err := config.LoadDaemon(config.Options{Args: []string{"--config", settings}, Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	project, err := resolved.Config.ForProject("example")
	if err != nil || project.Minecraft.Java != 0 || project.Minecraft.Version != "" || project.Build.JDKHome != "" || project.Runtimes["java25"].JDKHome == "" || project.Loaders["fabric"].GradleTask != "" {
		t.Fatal("host configuration did not defer build target selection to the manifest", err)
	}
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	scoped, err := db.WithProject(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	mirror, err := gitmirror.New(source, filepath.Join(root, "mirror.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = mirror.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	manager, err := build.New(project, scoped, mirror)
	if err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{withManifest, withoutManifest} {
		actual, err := manager.Manifest(t.Context(), sha)
		loader := actual.Loaders["fabric"]
		if err != nil || actual.Minecraft != document.Minecraft || len(actual.Loaders) != 1 || loader.LoaderVersion != "0.19.5" || !slices.Equal(loader.Build, document.Loaders["fabric"].Build) || len(loader.Files) != 1 || loader.Files[0].Glob != "build/libs/*.jar" || loader.Files[0].Side != "both" || actual.Rooms != document.Rooms {
			t.Fatal("actual commit or explicit fallback did not determine host build metadata", err)
		}
	}
	for _, change := range []string{"runtime", "loader", "memory", "missing-manifest"} {
		t.Run(change, func(t *testing.T) {
			limited := project
			sha := withManifest
			switch change {
			case "runtime":
				limited.Runtimes = map[string]config.JavaRuntime{"java21": {JDKHome: filepath.Join(root, "jdk21"), ServerImage: "itzg/minecraft-server:java21"}}
			case "loader":
				limited.Loaders = map[string]config.Loader{"neoforge": {Enabled: true}}
			case "memory":
				limited.Quota.RoomMemoryMax = "1Gi"
			case "missing-manifest":
				limited.ManifestFallback, sha = "", withoutManifest
			}
			restricted, err := build.New(limited, scoped, mirror)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = restricted.Manifest(t.Context(), sha); err == nil {
				t.Fatal("host manifest validation accepted an unavailable target or missing manifest")
			}
		})
	}
}
