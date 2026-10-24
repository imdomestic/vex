// Package testproject 在测试过程中建立被测试项目：自建的 Git 远端、固定的 vex.toml，
// 以及由固定测试文件经真实 CI bundle 导入得到的 Build。测试不读取任何实际项目仓库的状态。
package testproject

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/imdomestic/vex/controller/internal/build"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/gitmirror"
	"github.com/imdomestic/vex/controller/internal/manifest"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/store"
	"github.com/imdomestic/vex/controller/internal/testfixture"
)

// Manifest 是测试项目的 vex.toml：示例模组与 Fabric API 都作为项目自己的文件发布，不需要下载依赖。
const Manifest = `schema = 1

[minecraft]
version = "26.3"
java = 25

[loaders.fabric]
loader_version = "0.19.5"
agent = true

[[loaders.fabric.files]]
glob = "build/libs/*.jar"
side = "both"

[[loaders.fabric.files]]
glob = "libs/*.jar"
side = "both"

[rooms]
memory = "4G"
`

const (
	Repository = "vex-fixture/project"
	Workflow   = ".github/workflows/vex.yml"
	AgentID    = "fabric-26-3"
)

var runs atomic.Int64

// Project 是测试中自建的 Git 远端（bare）与推送用的工作副本。
type Project struct {
	Remote string
	Work   string
}

// New 建立远端与工作副本，并在 main 上提交 vex.toml。
func New(t testing.TB, root string) (*Project, string) {
	t.Helper()
	p := &Project{Remote: filepath.Join(root, "remote.git"), Work: filepath.Join(root, "work")}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	Git(t, root, "init", "--bare", "--initial-branch=main", p.Remote)
	Git(t, root, "clone", p.Remote, p.Work)
	sha := p.Commit(t, "main", "declare the Vex fixture project", map[string]string{"vex.toml": Manifest})
	return p, sha
}

// Git 以固定身份运行 git，不读取全局配置与钩子。
func Git(t testing.TB, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "user.name=Vex Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "-c", "init.defaultBranch=main"}, args...)...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// Commit 在 branch 上写入文件、提交并推送，返回提交 SHA。
func (p *Project) Commit(t testing.TB, branch, message string, files map[string]string) string {
	t.Helper()
	if Git(t, p.Work, "rev-parse", "--is-inside-work-tree") != "true" {
		t.Fatal("project work tree is invalid")
	}
	if current := Git(t, p.Work, "symbolic-ref", "--short", "HEAD"); current != branch {
		if Git(t, p.Work, "branch", "--list", branch) == "" {
			Git(t, p.Work, "checkout", "-b", branch)
		} else {
			Git(t, p.Work, "checkout", branch)
		}
	}
	for name, content := range files {
		filename := filepath.Join(p.Work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		Git(t, p.Work, "add", "--", name)
	}
	Git(t, p.Work, "commit", "--allow-empty", "-m", message)
	Git(t, p.Work, "push", "origin", "HEAD:refs/heads/"+branch)
	return Git(t, p.Work, "rev-parse", "HEAD")
}

// ForcePush 把远端 branch 强制改写到 target，模拟被测试项目改写历史。
func (p *Project) ForcePush(t testing.TB, branch, target string) {
	t.Helper()
	Git(t, p.Work, "push", "--force", "origin", target+":refs/heads/"+branch)
}

// Configure 返回使用自建远端与固定 agent 的项目配置，并把 agent 写入全局配置。
func Configure(t testing.TB, global *config.Config, remote string, members map[string]config.Role) config.Project {
	t.Helper()
	if global.Agents == nil {
		global.Agents = map[string]config.Agent{}
	}
	global.Agents[AgentID] = config.Agent{Minecraft: "26.3", Loader: "fabric", Version: "1.0.0", File: testfixture.File(t, "vex-agent-26.3-1.0.0.jar")}
	return config.Project{
		Remote: remote, DefaultBranch: "main", Trust: "untrusted",
		Git:       config.ProjectGit{PollInterval: "1m"},
		Minecraft: config.Target{Version: "26.3", Java: 25},
		Build:     config.ProjectBuild{Source: "ci", Timeout: "30m"},
		Loaders:   map[string]config.Loader{"fabric": {Enabled: true, Agent: AgentID}},
		Members:   members,
		Quota:     config.Quota{MaxRooms: 1, RoomMemoryMax: "6G", BuildCache: 8},
		GitHub:    config.ProjectGitHub{Repository: Repository},
		CI:        config.ProjectCI{Mode: "pull", Workflow: Workflow, Artifact: "vex-build", PollInterval: "1m", ForkPreviews: "off", PreviewAccess: "members", CommentLanguage: "zh-CN", PackLinkTTL: "168h"},
		Policy:    config.ProjectPolicy{OnNewHead: "when_empty"},
	}
}

// Policy 建立测试项目，返回其 main 提交的元数据与解析后的清单，供不需要 Build 的世界与归档测试使用。
func Policy(t testing.TB, root string) (manifest.Document, model.CommitInfo) {
	t.Helper()
	project, sha := New(t, root)
	doc, err := manifest.Parse([]byte(Manifest))
	if err != nil {
		t.Fatal(err)
	}
	mirror, err := gitmirror.New(project.Remote, filepath.Join(root, "policy-mirror.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = mirror.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	commit, err := mirror.CommitInfo(t.Context(), sha)
	if err != nil {
		t.Fatal(err)
	}
	return doc, commit
}

// CIImport 用固定测试文件经 CollectBundle 组成真实 CI bundle，返回导入 sha 时使用的请求。
func CIImport(t testing.TB, sha string) build.CIImport {
	t.Helper()
	doc, err := manifest.Parse([]byte(Manifest))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	output := filepath.Join(root, "output")
	testfixture.Copy(t, "vex-example-fabric-1.1.1.jar", filepath.Join(output, "build", "libs", "vex-example-fabric-1.1.1.jar"))
	testfixture.Copy(t, "fabric-api-0.161.0+26.3.jar", filepath.Join(output, "libs", "fabric-api-0.161.0+26.3.jar"))
	bundle, err := build.CollectBundle(t.Context(), doc, output, filepath.Join(root, "bundle"), []string{"fabric"})
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "bundle.zip")
	if err = build.WriteBundleZIP(bundle, filepath.Join(root, "bundle"), archive); err != nil {
		t.Fatal(err)
	}
	return build.CIImport{SHA: sha, RunID: runs.Add(1), RunAttempt: 1, Repository: Repository, HeadRepository: Repository, Workflow: Workflow, Event: "push", BundlePath: archive}
}

// Fixture 是只需要构建记录的测试项目：自建远端、镜像、SQLite 与导入的 Build。
type Fixture struct {
	Config  config.ProjectConfig
	Store   *store.Store
	DB      *store.Store
	Mirror  *gitmirror.Mirror
	Builder *build.Manager
	Project *Project
	Build   model.Build
}

// NewFixture 建立项目 id 的完整构建环境，数据目录位于 t.TempDir()。
func NewFixture(t testing.TB, id string) *Fixture {
	t.Helper()
	root := t.TempDir()
	project, sha := New(t, filepath.Join(root, "project"))
	global := config.Config{Server: config.Server{DataDir: filepath.Join(root, "data"), Zone: "integration.vex.internal"}, Runtimes: map[string]config.JavaRuntime{"java25": {}}}
	global.Projects = map[string]config.Project{id: Configure(t, &global, project.Remote, map[string]config.Role{})}
	cfg, err := global.ForProject(id)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Close() })
	db, err := owner.WithProject(id)
	if err != nil {
		t.Fatal(err)
	}
	mirror, err := gitmirror.New(project.Remote, filepath.Join(root, "mirror.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = mirror.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	builder, err := build.New(cfg, db, mirror)
	if err != nil {
		t.Fatal(err)
	}
	return &Fixture{Config: cfg, Store: owner, DB: db, Mirror: mirror, Builder: builder, Project: project, Build: ImportBuild(t, builder, sha)}
}

// ImportBuild 用固定测试文件组成真实 CI bundle，并经 ImportCI 导入为 sha 的 Build；镜像需要已经包含 sha。
func ImportBuild(t testing.TB, builder *build.Manager, sha string) model.Build {
	t.Helper()
	values, _, err := builder.ImportCI(t.Context(), CIImport(t, sha))
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].Status != "ready" || values[0].Loader != "fabric" || values[0].SHA != sha || values[0].Commit == nil || values[0].Manifest == nil {
		t.Fatalf("fixture build import: %+v", values)
	}
	return values[0]
}
