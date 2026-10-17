package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const projectDocument = `
[runtimes.java25]
server_image = "itzg/minecraft-server:java25"
jdk_home = "/opt/jdk25"
[users.reader]
minecraft = ["00000000-0000-4000-8000-000000000001"]
[projects.example-mod]
remote = "/srv/repositories/example.git"
trust = "trusted"
whitelist = ["00000000-0000-4000-8000-000000000001"]
[projects.example-mod.minecraft]
version = "26.3"
java = 25
[projects.example-mod.members]
reader = "viewer"
[projects.example-mod.loaders.fabric]
enabled = true
gradle_task = ":fabric:build"
artifact_glob = "fabric/build/libs/*.jar"
loader_version_property = "fabric_loader_version"
`

func writeConfig(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultSeedConfiguration(t *testing.T) {
	path := writeConfig(t, projectDocument)
	resolved, err := LoadDaemon(Options{Args: []string{"--config", path}, Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	project, err := resolved.Config.ForProject("example-mod")
	if err != nil || project.Rooms.DefaultSeed != "empty" {
		t.Fatalf("project default seed: %q %v", project.Rooms.DefaultSeed, err)
	}
	for _, seed := range []string{"", "unknown:main", "branch:", "snapshot:"} {
		_, err = LoadDaemon(Options{Args: []string{"--config", path, "--rooms.default_seed=" + seed}, Environ: []string{}})
		if err == nil || !strings.Contains(err.Error(), "rooms.default_seed") {
			t.Fatalf("invalid seed accepted: %q %v", seed, err)
		}
	}
	for _, seed := range []string{"empty", "branch:main", "snapshot:main-fabric-g1", "world:sample"} {
		_, err = LoadDaemon(Options{Args: []string{"--config", path, "--rooms.default_seed=" + seed}, Environ: []string{}})
		if err != nil {
			t.Fatalf("valid seed rejected: %q %v", seed, err)
		}
	}
}

func TestFourLayersAndProjectIDs(t *testing.T) {
	path := writeConfig(t, "[rooms.idle]\ncommit = '12m'\n"+projectDocument)
	resolved, err := LoadDaemon(Options{
		Args:    []string{"--rooms.idle.commit=14m", "--projects.example-mod.quota.max_rooms=7", "--minecraft.accept_eula"},
		Environ: []string{"VEX_CONFIG=" + path, "VEX_ROOMS__IDLE__COMMIT=13m", "VEX_PROJECTS__EXAMPLE_MOD__DEFAULT_BRANCH=dev"},
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err := resolved.Config.ForProject("example-mod")
	if err != nil {
		t.Fatal(err)
	}
	if project.Rooms.CommitIdle != "14m" || project.Rooms.BranchIdle != "10m" || project.Git.DefaultBranch != "dev" || project.Quota.MaxRooms != 7 || !project.Minecraft.AcceptEULA {
		t.Fatal("configuration precedence or project decoding failed")
	}
	if project.DataDir != "/var/lib/vex/projects/example-mod" || project.Trust != "trusted" || project.Minecraft.Image != "itzg/minecraft-server:java25" || len(project.Minecraft.Whitelist) != 1 {
		t.Fatal("project context did not preserve runtime or isolate data")
	}
	for path, layer := range map[string]string{
		"rooms.idle.commit": "command", "rooms.idle.branch": "default", "projects.example-mod.default_branch": "environment",
		"projects.example-mod.remote": "file", "projects.example-mod.build.source": "default",
	} {
		if resolved.Sources[path].Layer != layer {
			t.Fatalf("source for %s: %v", path, resolved.Sources[path])
		}
	}
}

func TestEachValueShapeAcrossAllFourLayers(t *testing.T) {
	baseline := writeConfig(t, projectDocument)
	file := writeConfig(t, projectDocument+`
[server]
zone = "file.internal"
listen = ["127.0.0.1:8791"]
[projects.example-mod.quota]
max_rooms = 5
[rooms.idle]
commit = "11m"
`)
	for stage := range 4 {
		t.Run([]string{"default", "file", "environment", "command"}[stage], func(t *testing.T) {
			options := Options{Args: []string{"--config", baseline}, Environ: []string{}}
			if stage >= 1 {
				options.Args = []string{"--config", file}
			}
			if stage >= 2 {
				options.Environ = []string{
					"VEX_SERVER__ZONE=environment.internal", "VEX_SERVER__LISTEN=127.0.0.1:8792",
					"VEX_PROJECTS__EXAMPLE_MOD__QUOTA__MAX_ROOMS=6", "VEX_ROOMS__IDLE__COMMIT=12m",
				}
			}
			if stage == 3 {
				options.Args = append(options.Args, "--server.zone=command.internal", `--server.listen=["127.0.0.1:8793"]`, "--projects.example-mod.quota.max_rooms=7", "--rooms.idle.commit=13m")
			}
			result, err := LoadDaemon(options)
			if err != nil {
				t.Fatal(err)
			}
			zones := []string{"vex.internal", "file.internal", "environment.internal", "command.internal"}
			listen := []string{"127.0.0.1:8790", "127.0.0.1:8791", "127.0.0.1:8792", "127.0.0.1:8793"}
			idle := []string{"10m", "11m", "12m", "13m"}
			if result.Config.Server.Zone != zones[stage] || len(result.Config.Server.Listen) != 1 || result.Config.Server.Listen[0] != listen[stage] || result.Config.Projects["example-mod"].Quota.MaxRooms != stage+4 || result.Config.Rooms.Idle.Commit != idle[stage] {
				t.Fatal("scalar, list, collection or nested precedence differs")
			}
			layer := []string{"default", "file", "environment", "command"}[stage]
			for _, key := range []string{"server.zone", "server.listen", "projects.example-mod.quota.max_rooms", "rooms.idle.commit"} {
				if result.Sources[key].Layer != layer {
					t.Fatalf("incorrect source for %s", key)
				}
			}
		})
	}
}

func TestUnknownKeysRejectedInEveryLayer(t *testing.T) {
	valid := writeConfig(t, projectDocument)
	for _, item := range []struct {
		name    string
		options Options
	}{
		{"file", Options{Args: []string{"--config", writeConfig(t, projectDocument+"\nmisspelled = true\n")}, Environ: []string{}}},
		{"environment", Options{Args: []string{"--config", valid}, Environ: []string{"VEX_PROJECTS__EXAMPLE_MOD__BUILDD__SOURCE=host"}}},
		{"command", Options{Args: []string{"--config", valid, "--git.remote=/srv/repositories/example.git"}, Environ: []string{}}},
		{"direct-secret", Options{Args: []string{"--config", valid, "--auth.token=private-value-never-print"}, Environ: []string{}}},
		{"file-type-overridden", Options{Args: []string{"--config", writeConfig(t, "[rooms.idle]\ncommit = 42\n"), "--rooms.idle.commit=1m"}, Environ: []string{}}},
		{"invalid-id", Options{Args: []string{"--config", valid, "--projects.example--mod.remote=remote"}, Environ: []string{}}},
	} {
		t.Run(item.name, func(t *testing.T) {
			_, err := LoadDaemon(item.options)
			if err == nil {
				t.Fatal("invalid layer accepted")
			}
			if strings.Contains(err.Error(), "private-value-never-print") {
				t.Fatal("configuration error disclosed a value")
			}
		})
	}
}

func TestListSyntaxAndConfigSelection(t *testing.T) {
	file := writeConfig(t, projectDocument)
	for _, value := range []string{"-Dfirst=true,-Dsecond=true", `["-Dfirst=true", "-Dsecond=true"]`} {
		resolved, err := LoadDaemon(Options{
			Args:    []string{"--config=" + file, "--projects.example-mod.build.gradle_args=" + value},
			Environ: []string{"VEX_CONFIG=/missing/ignored.toml"},
		})
		if err != nil {
			t.Fatal(err)
		}
		args := resolved.Config.Projects["example-mod"].Build.GradleArgs
		if len(args) != 2 || args[1] != "-Dsecond=true" {
			t.Fatal("list syntax did not preserve values")
		}
		if len(resolved.Config.Warnings()) != 1 {
			t.Fatal("EULA warning missing")
		}
	}
	if _, err := LoadDaemon(Options{Args: []string{"--config=/missing/explicit.toml"}, Environ: []string{}}); err == nil {
		t.Fatal("explicit missing configuration file accepted")
	}
}

func TestClientSourcesGolden(t *testing.T) {
	file := writeConfig(t, "url = 'http://localhost:8790'\ntimeout = '1m'\n")
	resolved, err := LoadClient(Options{
		Args:    []string{"--config", file, "-p", "example-mod", "--url=http://127.0.0.1:8791"},
		Environ: []string{"VEX_TIMEOUT=2m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := resolved.Render(true)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile("testdata/client-sources.golden.toml")
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(expected) {
		t.Fatalf("source output differs from golden:\n%s", actual)
	}
}

func TestIDAndAgentCompatibility(t *testing.T) {
	for _, id := range []string{"-bad", "bad-", "bad--id", "Bad", "this-id-is-over-twenty-three-characters", "bad_id"} {
		if ValidID(id) {
			t.Fatal("invalid ID accepted")
		}
	}
	file := writeConfig(t, projectDocument+`
agent = "fabric-26-3"
[agents.fabric-26-3]
minecraft = "26.3"
loader = "fabric"
version = "1.0.0"
file = "/opt/vex-agent.jar"
`)
	resolved, err := LoadDaemon(Options{Args: []string{"--config", file}, Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	agent := resolved.Config.Agents["fabric-26-3"]
	agent.Minecraft = "1.21"
	resolved.Config.Agents["fabric-26-3"] = agent
	if err = resolved.Config.Validate(); err == nil {
		t.Fatal("agent from another Minecraft version accepted")
	}
}

func TestUnsupportedLoaderRejectedBeforeRuntime(t *testing.T) {
	file := writeConfig(t, projectDocument+`
[projects.example-mod.loaders.unsupported]
enabled = true
gradle_task = ":other:build"
artifact_glob = "other/build/libs/*.jar"
loader_version_property = "other_loader_version"
`)
	if _, err := LoadDaemon(Options{Args: []string{"--config", file}, Environ: []string{}}); err == nil || !strings.Contains(err.Error(), "P1 supports fabric and neoforge") {
		t.Fatal("configuration accepted an unsupported runtime loader")
	}
}
