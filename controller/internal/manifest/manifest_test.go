package manifest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imdomestic/vex/controller/internal/gitmirror"
	"github.com/imdomestic/vex/controller/internal/manifest"
)

const validManifest = `schema = 1
[minecraft]
version = "26.3"
java = 25
[loaders.fabric]
loader_version = "0.19.5"
build = ["./gradlew", ":fabric:build", "--no-daemon"]
agent = true
[[loaders.fabric.files]]
glob = "fabric/build/libs/*.jar"
exclude = ["*-sources.jar"]
side = "both"
[world]
extra_root_entries = ["project-state.json"]
[rooms]
memory = "4G"
`

func TestStrictManifestAndOperatorPolicy(t *testing.T) {
	doc, err := manifest.Parse([]byte(validManifest))
	if err != nil {
		t.Fatal(err)
	}
	policy := manifest.Policy{Minecraft: "26.3", Java: map[int]bool{25: true}, Loaders: map[string]bool{"fabric": true}, MaxMemory: "5Gi", ExtraRootEntries: []string{"project-state.json"}}
	if err = manifest.Validate(doc, policy); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"minecraft", "java", "loader", "memory", "root"} {
		t.Run(field, func(t *testing.T) {
			limited := policy
			switch field {
			case "minecraft":
				limited.Minecraft = "1.21.1"
			case "java":
				limited.Java = map[int]bool{21: true}
			case "loader":
				limited.Loaders = map[string]bool{"neoforge": true}
			case "memory":
				limited.MaxMemory = "3Gi"
			case "root":
				limited.ExtraRootEntries = []string{}
			}
			if err := manifest.Validate(doc, limited); err == nil {
				t.Fatal("manifest escaped operator policy")
			}
		})
	}
	for _, input := range []string{
		"token = 'rejected'\n" + validManifest,
		strings.Replace(validManifest, "schema = 1", "schema = 2", 1),
		strings.Replace(validManifest, "fabric/build/libs/*.jar", "../outside/*.jar", 1),
		strings.Replace(validManifest, "fabric/build/libs/*.jar", "/outside/*.jar", 1),
		strings.Replace(validManifest, "side = \"both\"", "side = \"unknown\"", 1),
		strings.Replace(validManifest, "java = 25", "java = '25'", 1),
		strings.Replace(validManifest, "memory = \"4G\"", "memory = \"-4G\"", 1),
	} {
		if _, err = manifest.Parse([]byte(input)); err == nil {
			t.Fatal("invalid manifest accepted")
		}
	}
}

func TestFallbackOnlyForAbsentCommitFile(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	git := func(directory string, args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-c", "user.name=Vex Manifest Test", "-c", "user.email=vex@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
		command.Dir = directory
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git operation failed: %v %s", err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git(root, "init", "--initial-branch=main", source)
	git(source, "commit", "--allow-empty", "-m", "commit without manifest")
	absent := git(source, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(source, "vex.toml"), []byte("schema = 'invalid'"), 0600); err != nil {
		t.Fatal(err)
	}
	git(source, "add", "vex.toml")
	git(source, "commit", "-m", "invalid manifest")
	invalid := git(source, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(source, "vex.toml"), []byte(validManifest), 0600); err != nil {
		t.Fatal(err)
	}
	git(source, "add", "vex.toml")
	git(source, "commit", "-m", "valid manifest")
	valid := git(source, "rev-parse", "HEAD")
	mirror, err := gitmirror.New(source, filepath.Join(root, "mirror.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = mirror.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	fallback := filepath.Join(root, "fallback.toml")
	if err = os.WriteFile(fallback, []byte(validManifest), 0600); err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{absent, valid} {
		doc, provenance, err := manifest.ReadCommit(t.Context(), mirror, sha, fallback)
		if err != nil || doc.Minecraft.Version != "26.3" || provenance.Fallback != (sha == absent) || provenance.Commit != sha {
			t.Fatalf("incorrect manifest source: %+v %v", provenance, err)
		}
	}
	if _, _, err = manifest.ReadCommit(t.Context(), mirror, invalid, fallback); err == nil {
		t.Fatal("invalid commit manifest used fallback")
	}
	if _, _, err = manifest.ReadCommit(t.Context(), mirror, absent, ""); err == nil {
		t.Fatal("missing manifest accepted without fallback")
	}
	link := filepath.Join(root, "linked.toml")
	if err = os.Symlink(fallback, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err = manifest.ReadCommit(t.Context(), mirror, absent, link); err == nil {
		t.Fatal("fallback symbolic link accepted")
	}
}
