# vex 设计

状态：草案 v0.3（2026-10-18）。

v0.3 相对 v0.2 的变化：

- 明确部署拓扑：一个部署者一个 vexd，客户端同时连接多个 vexd；
- 新增邀请登录与会话；
- GitHub 登录提前到 P2。

v0.2 相对 v0.1 的变化：

- 吸收了开源化评审意见：运行后端抽象、CI 推送产物、PR 预览服、mrpack、存储后端、泛域名、OIDC、EULA、许可证；
- 新增游戏内大厅（lobby）；
- 新增 ruri 上游化路线。

现状依据：提交 `f035614` 的[盘点报告](../../STATE-REPORT.md)。每一轮的执行范围以 `docs/directives/` 中的指令为准。设计原文保存在 [v0.3 原文](../directives/vex-v0.3-original.md)、[v0.2 原文](../directives/vex-v0.2-original.md)和 [v0.1 原文](../directives/vex-v0.1-original.md)，本文使用通用项目示例。

## 1. 定位

vex 是 Minecraft 模组开发者自托管的持续部署测试平台。核心卖点是：**每个 PR 都有一个能直接进去玩的预览服。**

- **构建**：项目 CI 构建完成后把产物交给 vex；vex 内置的构建器只是备选。
- **运行**：vex 为分支、PR、提交和世界开房，经 mc-router 路由。
- **运行后端**：首选单机 Docker（兼容 Podman），其次是 Kubernetes（k3s）。
- **客户端**：
  - 任何启动器都能用：下载房间的 mrpack，复制服务器地址，就能进入；
  - 游戏内大厅提供浏览和操作界面，不受平台限制；
  - ruri 开启实验性功能后，可以一键进入。

部署者（operator）可信。项目代码、CI 产物、仓库内的清单、上传的世界都不可信。

**部署拓扑**：

- 一个部署者（一台机器或一个集群）只运行一个 vexd，项目都放在这个 vexd 里。
- 不同部署者各自运行自己的 vexd。客户端（ruri、vex CLI）可以同时连接多个 vexd，并把它们汇总显示。
- 同一台机器上也可以运行多个实例，只要数据目录、端口、zone 和 namespace 前缀互不冲突。
- 只有一个项目时，CLI 和 ruri 自动选中它。

项目通过配置接入 vex，平台代码保持项目无关。

## 2. 命名

| 对象 | 名称 |
| --- | --- |
| 守护进程 | `vexd` |
| 命令行 | `vex` |
| 游戏内模组 | vex-agent。mod ID 为 `vex_agent`（NeoForge 的 mod ID 不允许连字符），Java 包为 `com.imdomestic.vex.agent`。同一个 jar 用于项目房间和大厅 |
| GitHub Action | `imdomestic/vex-action` |
| 运维配置 | `/etc/vex/vexd.toml` |
| 仓库清单 | 模组仓库根目录 `vex.toml` |
| CLI 用户配置 | `~/.config/vex/config.toml` |
| 环境变量前缀 | `VEX_` |
| 数据目录 | 默认 `/var/lib/vex`；我们的部署为 `/srv/vex` |
| 房间地址 | `<room>--<project>.<zone>:25565`。只有一级，部署者配一条 `*.<zone>` 泛解析即可。ID 中不允许出现 `--` |
| 大厅地址 | `lobby.<zone>` |
| 我们的 zone | `vex.internal` |
| Kubernetes 后端 | 系统 namespace 为 `vex-system`，项目 namespace 为 `vex-p-<project>`，标签前缀为 `vex.imdomestic.com/` |
| 仓库 | `imdomestic/vex` |

提示：在 Minecraft 里，vex 也是恼鬼这种生物的名字，搜索 "minecraft vex" 结果基本都是它。名字保留没问题，但文档和仓库描述里要写全称，比如 "vex — Minecraft mod preview servers"。

## 3. 信任模型

可信：

- operator 及其配置；
- vexd 主机和运行后端的控制面；
- 归档存储；
- **大厅**：它只运行 vex 发布的代码，不加载任何项目模组。

不可信：

- 项目仓库的全部内容，包括构建脚本、Gradle 插件、`vex.toml`；
- CI 产物，包括 fork PR 构建出来的东西；
- 在项目房间 JVM 中运行的项目模组；
- 上传的世界和资源包。

由此得出以下约束：

1. **构建不在 vexd 主机上运行项目代码。** 主路径是由项目自己的 CI 构建。内置构建器只在容器或 Job 中运行；宿主机构建只允许 `trust = "trusted"` 的项目使用，现有项目改用 CI 后删除宿主机构建路径。
2. **项目房间按"运行不可信代码"来隔离。** Kubernetes 后端用 NetworkPolicy 实现。单机 Docker 后端的网络隔离较弱，文档中必须写清楚，并提供可选的 nftables 规则。
3. **三类 token 的权限：**
   - 房间 token：只能操作本项目、本房间、本 generation；
   - 大厅 token：可以代表"已绑定账号且当前在大厅里"的玩家操作；
   - CI 凭据：只能为指定仓库、指定工作流提交构建。
4. **vexd 不去访问不可信输入里给出的 URL**，避免 SSRF。依赖由 CI 或沙箱下载后一并提交给 vexd。
5. **无权访问的项目一律返回 404**，不暴露它是否存在。

## 4. 后端抽象

| 层 | 接口 | 实现与顺序 |
| --- | --- | --- |
| 运行 | `runtime.Backend`：EnsureProject、EnsureRoom、ObserveRoom、StopRoom（保存后停止）、ArchiveWorld、DeleteRoom、Logs、RCON、Capacity | `kubernetes`（现有代码迁入，P1）；`docker`（通过 Docker API，兼容 Podman，P5，文档中的默认方案） |
| 路由 | `router.Registrar` | mc-router 的 Kubernetes 注解（P1）；mc-router 的 Docker 标签或路由 API（P5）。具体方式需要实测 |
| 存储 | `archive.Store`：Put、Verify、Get、Stat，key 为 `projects/<p>/…` 形式的相对路径 | `local`、`rsync-ssh`（P1，现有）；`s3`（P5） |
| 构建 | `build.Source` | `ci`（P2，主路径）；`container`（P5，在 Docker 容器或 Kubernetes Job 里构建）；`host`（P1 过渡用，仅限 trusted 项目） |
| 认证 | `auth.Provider`，输出身份 `provider:subject`。无论通过哪种方式登录，最后都签发 vexd 自己的会话 | `invite`：一次性邀请链接，P1；`static`：服务账号令牌，只给自动化和 CI 用，P1；`github`：用 P2 建的 GitHub App 执行 device flow，P2；`oidc`：P3 |

单机 Docker 后端的做法：

- 世界和模组目录都是 vexd 数据目录下的普通目录，以 bind mount 挂进容器；
- 模组由 vexd 直接写入，不需要 initContainer；
- 归档时由 vexd 直接打包目录；
- 只有一个节点，没有调度问题。

Kubernetes 后端保留现有的 StatefulSet、PVC、initContainer 和归档 Job，这些都属于后端内部细节，不暴露给调和逻辑。

## 5. 配置系统

### 5.1 来源与优先级

优先级从高到低为：**命令行 > 环境变量 > 配置文件 > 内置默认值**。vexd 和 vex CLI 共用同一套实现。

- **配置文件的位置**：依次取 `--config`、`VEX_CONFIG`，都没有时用默认路径。
- **键的映射**：同一个配置项在三处的写法如下：
  - 文件：`[rooms.idle] commit = "10m"`
  - 环境变量：`VEX_ROOMS__IDLE__COMMIT=10m`
  - 命令行：`--rooms.idle.commit=10m`
- **集合用"以 ID 为键的表"**，例如 `[projects.example]`。这样环境变量和命令行也能单独定位某一个条目。
- **ID 规则**：`[a-z][a-z0-9-]{0,22}`，不能以 `-` 结尾，也不能包含 `--`。环境变量里，ID 段中的 `_` 解码为 `-`。
- **列表**：在环境变量和命令行中用逗号分隔；如果值以 `[` 开头，就按 TOML 字面量解析。
- **秘密**：
  - 只能用 `*_file` 字段引用文件；
  - 命令行直接给出秘密值一律拒绝；
  - 任何一层出现未知键都报错。
- **辅助命令**：
  - `vexd config show --sources`：打印最终生效的配置，并标出每个值来自哪一层；
  - `vexd config check`：只做校验。
- **生效方式**：改配置后需要重启。

### 5.2 Nix

- vex 的 flake 输出 `nixosModules.vexd` 和 `packages.vex`。
- `services.vexd.settings` 是自由格式，通过 `pkgs.formats.toml` 生成 `vexd.toml`。
- nix-config 只负责 import 这个模块并填写 settings。

### 5.3 运维配置示例

```toml
[server]
listen = ["192.0.2.10:8790"]
public_base_url = "http://vex.example.com:8790"
internal_base_url = "http://192.0.2.10:8790"
data_dir = "/srv/vex"
zone = "vex.internal"
router_address = "192.0.2.10:25565"

[minecraft]
accept_eula = true           # 部署者明确同意 Minecraft EULA 后才允许开房；默认 false

[runtime]
backend = "kubernetes"       # docker | kubernetes

[runtime.kubernetes]
kubeconfig_file = "/var/lib/vex/kubeconfig"
storage_class = "local-path"

# [runtime.docker]
# host = "unix:///run/podman/podman.sock"

[runtime.kubernetes.nodes.node-a]
k8s_name = "node-a"
roles = ["persistent", "ephemeral", "build"]
max_rooms = 2
priority = 10

[runtime.kubernetes.nodes.node-b]
k8s_name = "node-b"
roles = ["ephemeral", "build"]
max_rooms = 4
priority = 20

[runtimes.java25]
server_image = "itzg/minecraft-server:java25@sha256:7462fe34…"
build_image = "eclipse-temurin:25-jdk@sha256:…"     # container 构建器使用

[archive]
backend = "rsync-ssh"        # local | rsync-ssh | s3

[archive.rsync_ssh]
target = "vex-archive@archive.example.com:/srv/archive/vex"
identity_file = "/var/lib/vex/ssh/id_ed25519"
known_hosts_file = "/var/lib/vex/ssh/known_hosts"
bwlimit = "5M"

[auth]
signing_key_file = "/var/lib/vex/secrets/signing.key"
session_ttl = "2160h"        # 会话有效期，每次使用后顺延
invite_ttl = "24h"
admin_socket = "/run/vex/admin.sock"   # 本机管理入口，可直接执行 vexd user invite 等命令

[auth.providers.github]      # P2：直接使用 [github.app] 执行 device flow
type = "github"

[github.app]                 # P2：拉取 CI 产物、写 PR 评论和 check、GitHub 登录
app_id = 0
client_id = "…"
private_key_file = "/var/lib/vex/secrets/github-app.pem"
# webhook_secret_file = "…"  # vexd 能被公网访问时才配置，否则改为轮询

[users.operator]
admin = true
token_sha256 = "…"           # 可选：服务账号令牌，只给自动化和 CI 用；人工使用一律通过登录
identities = ["github:<数字 ID>"]
minecraft = ["<uuid>"]       # 也可以在大厅用 /vex link 绑定，绑定结果存 DB

[users.developer]
token_sha256 = "…"

[lobby]                      # P4
enabled = true
minecraft_version = "26.3"
loader_version = "0.19.5"
access = "linked"            # linked | public

[projects.example]
remote = "https://git.example.com/example/mod.git"
default_branch = "main"
trust = "trusted"
manifest_fallback = "/etc/vex/manifests/example.toml"

[projects.example.github]
repository = "example/mod"
derive_roles = true          # P3：按 GitHub 仓库权限推导角色，admin→owner，write→developer，read→viewer

[projects.example.build]
source = "ci"                # ci | container | host

[projects.example.ci]         # P2
mode = "pull"                # pull：vexd 轮询 GitHub 产物；push：CI 用 OIDC 推送
workflow = ".github/workflows/vex.yml"
artifact = "vex-build"
pr_previews = true
fork_previews = "approval"   # off | approval（维护者评论 /vex preview）| auto
preview_access = "members"   # members | linked | public

[projects.example.members]
operator = "owner"
developer = "developer"

[projects.example.policy]     # P3
auto_build = ["main", "dev"] # 只对 container/host 构建生效；ci 模式由 CI 自己触发
on_new_head = "when_empty"
agent_delegation = true

[projects.example.quota]
max_rooms = 4
room_memory_max = "6Gi"
build_cache = 20
snapshot_keep = 5
snapshot_keep_days = 14
```

## 6. 对象模型与身份

### 6.1 角色

- 全局角色只有一个：`admin`。
- 项目内角色从高到低为 owner、developer、viewer：
  - viewer：可以查看；可以启动或进入已有的房间；
  - developer：在 viewer 之外，还可以构建、新建房间、停止房间、发布世界；
  - owner：在 developer 之外，还可以做破坏性操作。
- 房间白名单 = 已绑定 MC 账号的项目成员 + `projects.<p>.whitelist`。
- PR 房间的白名单另外由 `preview_access` 决定。

### 6.2 ID 与存储

- 所有记录都带项目前缀：`(kind, "<project>/<id>")`。counter 也按项目区分。
- Build ID 格式为 `<loader>-<sha12>-<recipe8>`，P1 先用 `<loader>-<sha12>`。
  - Build 记录 `source = {kind: ci|container|host, …}`。
  - 每个文件都要记录 sha1、sha256、sha512 三种哈希，因为 mrpack 需要 sha1 和 sha512。
- Room ID 按房间类型区分：

  | 房间类型 | ID 格式 |
  | --- | --- |
  | 分支 | `<slug>-<loader>` |
  | PR | `pr<number>-<loader>` |
  | 提交 | `c<sha8>-<loader>` |
  | 世界 | `w-<world>-<sha8>-<loader>` |
  | 大厅 | `lobby`（不属于任何项目） |

- 文件路径和后端对象名都直接记录在对象里，运行时不从 ID 推导。
- 原 asset 改名为 world，pack 保持不变。
- 数据布局：
  - `<data>/state/vexd.db`
  - `<data>/projects/<p>/{git.git, builds, snapshots, worlds, packs, cache, rooms}`
  - 归档端：`projects/<p>/{snapshots, worlds, packs}` 和 `legacy/`

## 7. 构建：CI 为主

### 7.1 vex.toml（P2）

这份清单同时供 CI Action 和内置构建器读取：

```toml
schema = 1

[minecraft]
version = "26.3"
java = 25

[loaders.fabric]
loader_version = "0.19.5"
build = ["./gradlew", ":fabric:build", "--no-daemon"]   # 仅内置构建器使用
agent = true

[[loaders.fabric.files]]
glob = "fabric/build/libs/*.jar"
exclude = ["*-sources.jar", "*-dev.jar", "*-javadoc.jar"]
side = "both"

[[loaders.fabric.deps]]
url = "https://cdn.modrinth.com/…/fabric-api-0.161.0+26.3.jar"
sha1 = "…"
sha512 = "…"
side = "both"

[world]
extra_root_entries = ["example-checkpoint.json"]

[rooms]
memory = "4G"
```

清单中的取值都要在 operator 给定的范围内校验。没有 `vex.toml` 的提交改用 `manifest_fallback`。

### 7.2 CI 产物交付（P2）

`vex-action` 读取 `vex.toml`，收集文件，下载依赖并校验哈希，最后生成一个 build bundle，内容为 `vex-build.json` 加全部文件。

bundle 有两种交付方式。

**pull 模式（先做）：**

- CI 只执行 `actions/upload-artifact`。
- vexd 使用 GitHub App 的 installation token 做三件事：
  - 轮询 `ci.workflow` 下已完成的 run；
  - 下载名为 `ci.artifact` 的产物；
  - 从 GitHub API 读取 `head_sha` 和 PR 信息，而不是相信 bundle 里自己写的值。
- vexd 不需要被公网访问。我们的 encore 只在内网，所以先做这个模式。

**push 模式：**

- CI 申请 `id-token: write`，用 GitHub Actions 的 OIDC JWT 调用 `POST …/ci/builds`。
- vexd 校验以下内容：
  - JWT 的签名、issuer 和 audience；
  - `repository` 是否等于 `projects.<p>.github.repository`；
  - `workflow_ref`、`sha`、`event_name`。
- 这个模式要求 vexd 能被公网访问（HTTPS）。

**fork PR 的处理：** 来自 fork 的 `pull_request` 工作流拿不到 OIDC token，也拿不到 secrets。

- push 模式下采用 `workflow_run` 两段式：
  - 第一段在 PR 上下文中构建，只上传产物；
  - 第二段在基础仓库上下文中运行，负责转交给 vexd。
  - `vex-action` 用 `mode: build` 和 `mode: relay` 两种模式支持这两段。
- pull 模式不受这个限制，因为下载产物的是 vexd 自己。
- 无论哪种模式，fork 预览都按 `fork_previews` 的设置处理。

### 7.3 PR 预览服（P2）

**生命周期**

- PR 打开或有新提交时：
  1. 新的 Build 就绪；
  2. PR 房间按 `on_new_head` 的设置滚动到新 generation；
  3. 首次进入时才真正启动，无人时按 idle 规则停止。
- PR 关闭或合并时，房间关闭并归档。vexd 通过轮询发现，配置了 webhook 时由 webhook 通知。

**PR 评论**

vexd 通过 GitHub App 维护一条 PR 评论，房间状态变化时就地更新。评论包含：

- 服务器地址：`pr12-fabric--example.mc.example.com`；
- mrpack 下载链接；
- 当前提交和构建状态；
- 房间状态；
- 开启了 ruri 实验性功能时，附上 ruri 打开链接。

同时 vexd 写入一个 check run，供 PR 页面展示。

### 7.4 内置构建器（P5）

内置构建器会在 Docker 容器或 Kubernetes Job 里构建，方式如下：

- **源码**：由 vexd 打成 `git archive` 包，通过 build token 下载。
- **构建环境**：非 root 运行，根文件系统只读。
- **缓存**：每个项目独立的 Gradle 缓存。
- **网络**：禁止访问 LAN、tailnet 和集群内部，只放行回传给 vexd 的端口。
- **产物**：打成和 CI 相同格式的 build bundle 交回 vexd。

宿主机构建只在过渡期使用。

## 8. 房间运行时

- **服务端 jar**：vex 不分发服务端 jar，由 itzg 镜像在运行时下载。
  - 只有 `minecraft.accept_eula = true` 时，vexd 才会传 `EULA=TRUE`；
  - 未同意时，开房请求返回 `eula_not_accepted`。
- **镜像与类型**：服务端镜像按 Java 版本取自 `runtimes`。itzg 的 `TYPE` 由 loader 决定。
- **房间 token 的权限**：
  - 上传本房间本 generation 的快照；
  - 下载本 Build 的文件和本房间的 seed；
  - 查询和申请 hop；
  - 以 `Vex-On-Behalf-Of` 发起的少量委托操作（见 §11）。
- **Kubernetes 后端**：
  - 每个项目一个 namespace，配有 ResourceQuota、LimitRange 和 NetworkPolicy；
  - 入站只允许来自 mc-router；
  - 出站只允许 DNS、vexd 的 API 端口和公网；
  - 调度依据节点的 roles 和 priority；
  - 磁盘指标取自 kubelet stats summary。
- **Docker 后端**：
  - 每个项目一个 bridge 网络；
  - 文档提供 nftables 规则，用来禁止房间访问私有地址段。

## 9. 房间描述：mrpack（P2）

每个 Build 和每个房间都能导出一个 mrpack：

- `GET …/builds/{id}/pack.mrpack`
- `GET …/rooms/{id}/pack.mrpack`

mrpack 的内容如下：

- **`modrinth.index.json`**：
  - `dependencies` 写明 `minecraft` 和 loader（`fabric-loader` 或 `neoforge`）的版本；
  - 来源是公开 CDN 且哈希已知的依赖写进 `files`，并带上 `env`。
- **项目自己的 jar**：放在 `overrides/mods/`（客户端专用的放在 `client-overrides/mods/`）。这样包是自包含的，不依赖会过期的签名 URL。
- **`overrides/servers.dat`**（房间包才有）：写入一条房间地址，导入后多人游戏列表里会直接出现这个服务器。
- **`versionId`**：分别取 `<build id>`（Build 包）或 `<room>-g<generation>`（房间包）。

客户端指纹的定义改为"mrpack 里客户端侧文件的哈希集合，加上 MC 版本、loader 和 loader 版本"。指纹相同才允许游戏内直接跳转（P4）。

需要实测的点：

- Prism、HMCL 和上游 ruri 能否导入这个 mrpack；
- 它们是否限制下载域名；
- `servers.dat` 是否生效。

## 10. 世界发布（P1 起通用化）

- **CLI 命令**：
  - `vex world publish <目录或 tar.gz> -p <project> --world <id>`
  - `vex world promote`
  - `vex pack publish <zip> -p <project> --pack <id>`
  - 对应的 API 是 `…/worlds/*` 和 `…/packs/*`。
- **d2map**：改为调用 `vex` CLI，不再直接请求 API。
- **世界扫描器**：从 Python 改写成 Go，使用已有的 go-mc 依赖，作为 vexd 的一部分，部署者因此不再需要 Python 环境（P5）。

## 11. 游戏内前端：大厅与 vex-agent（P4）

### 11.1 大厅

大厅是平台自带的房间，地址为 `lobby.<zone>`，具体如下：

- **运行环境**：Fabric 服务端，跑 operator 指定的 MC 版本，加载处于大厅模式的 vex-agent，不加载任何项目模组。
- **客户端要求**：原版客户端就能进入。玩家只要一个对应版本的原版实例加上大厅地址，就能看到所有内容。
- **界面**：使用原版 1.21.6 起的服务端 Dialog（26.3 上需要实测）。Dialog 不可用时，退回可点击的聊天组件。
- **页面结构**：项目列表 → 项目页（分支、PR、最近提交及构建状态）→ 条目页（构建日志的末尾部分；构建、开房、获取整合包等操作）→ 房间就绪页。
- **房间就绪页的按钮**：
  - 「直接进入」：只有客户端指纹与当前客户端一致时才显示，使用 transfer，要求 MC 1.20.5 及以上，目标服务器需开启 `accepts-transfers`；
  - 「下载整合包」：`open_url` 打开 mrpack 链接；
  - 「复制地址」：`copy_to_clipboard`；
  - 「在 ruri 中打开」：仅当该用户的 ruri 在线时显示，通过 `launch_intent` 实现。
- **账号绑定**：玩家执行 `/vex link`，vexd 发起 OAuth device flow，Dialog 显示验证网址和用户码。授权完成后，这个 MC UUID 就绑定到对应的 vex 用户。账号绑定只能在大厅里完成，因为大厅可信，而且服务器处于 online-mode，UUID 已经过验证。

### 11.2 项目房间里的 vex-agent

- **可用操作**：查看房间信息；跳转到指纹兼容的房间（`/server`）；返回大厅。
- **委托操作**：只开放构建、启动提交房间、hop 三项，并且必须同时满足以下条件：
  - 玩家已经绑定账号；
  - 玩家当前在这个房间里（依据 mc-router 的 connect 事件和 SLP sample）；
  - 项目开启了 `agent_delegation`。
- **原因**：项目房间里的 agent 和不可信的项目模组跑在同一个 JVM 里，能拿到的权限必须比大厅少。

### 11.3 委托的统一形式

大厅 token 或房间 token 加上请求头 `Vex-On-Behalf-Of: <uuid>`，可以调用普通的项目端点。vexd 按该玩家对应用户的角色来授权，并在此基础上再加两条限制：

1. 房间 token 只能调用白名单里的端点；
2. 玩家必须当前在线于调用方所在的房间。

### 11.4 客户端模组没法热替换

切换到客户端模组不同的房间时，只能通过启动器重启游戏：

- 用 mrpack 导入：任何启动器都可以；
- 用 `launch_intent`：ruri 收到后会关闭游戏、同步模组，再用 QuickPlay 重新启动。

以后可以考虑一个客户端 companion 模组：在 MC 版本和 loader 都不变的前提下，退出游戏时替换 mods 目录，下次启动后直接连进目标房间。这项不在当前路线内。

## 12. ruri 实验性功能（独立路线 R）

目标是让 ruri 的 fork 和上游只差一个"默认关闭、自成一体"的 vex 功能，然后合回上游，随上游发布进入 nixpkgs。

- **入口**：设置页新增「实验性功能」，其中有 vex 开关。开关关闭时，GUI 不显示 vex 页面，CLI 的 `vex` 命令组不出现在帮助、命令发现和 schema 中，也不发起任何网络请求或钥匙串读取。
- **多服务器**：开关下方是一个「Vex 服务器」列表，每个服务器单独记录地址和登录状态。服务器页和 Git 页按「服务器 → 项目」汇总显示。
- **登录**：GUI 不再出现 token 输入框。添加服务器时可以直接粘贴邀请链接，一步完成添加和登录；服务器支持的话，也可以点击「使用 GitHub 登录」。登录后显示「已登录：用户名」，并提供「退出登录」。服务账号令牌只能在 CLI 中通过 `--token-stdin` 使用。
- **去掉 fork 自己的差异**：fork 现有的独立应用改动全部撤销，包括 bundle id、数据目录、钥匙串、CLI 名称、Sparkle、签名、窗口标题、语言设置。
- **模组同步**：改为调用上游已有的 `MRPack.swift` 做导入和更新，删除 fork 自己的 manifest 同步代码（R2，在 vex P2 之后）。
- **通用修复单独提交**：比如 26.3 SDL 窗口的退出修复，先作为独立 PR 提交给上游。
- **提交时机**：vex API v2 冻结后再向上游提交 PR。在此之前，用户先通过 issue 或 discussion 和上游维护者确认是否接受。

## 13. API v2 草案

**通用约定**

- 全部路径以 `/v2` 为前缀，下表中 `…` 代表 `/v2/projects/{p}`。
- 错误结构沿用 v1 的 `{error:{code,message,details?}}`。
- 请求无权访问的项目返回 404。

**权限缩写**

| 缩写 | 含义 |
| --- | --- |
| — | 无需认证 |
| U | 任意登录用户 |
| V / D / O | 项目 viewer / developer / owner 及以上 |
| A | admin |
| R | 房间 token |
| L | 大厅 token |
| C | CI 凭据（GitHub OIDC） |
| B | 内置构建器的 build token |
| S | 签名 URL |
| I | mc-router |
| G | GitHub webhook 的 HMAC 签名 |

**平台级端点**

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/v2/health` | — | |
| GET | `/v2/meta` | — | 服务器名称、版本、可用的登录方式；登录后额外返回 zone、运行时目录、agent 版本 |
| POST | `/v2/auth/invites` | U/A | 普通用户给自己发，admin 可以给任何用户发；返回链接 `<public_base_url>/i/<code>`，只显示这一次（P1） |
| GET | `/i/{code}` | — | 静态说明页，告诉对方在哪里粘贴链接；不会兑换邀请（P1） |
| POST | `/v2/auth/invites/redeem` | — | 请求体 `{code, client}`，换取会话 token；按来源 IP 和全局限速（P1） |
| POST | `/v2/auth/device`、`/v2/auth/device/poll` | — | GitHub device flow 在 P2，OIDC 在 P3 |
| GET、DELETE | `/v2/me/sessions`、`/v2/me/sessions/{id}` | U | 查看或吊销自己已登录的设备（P1） |
| POST | `/v2/auth/logout` | U | 吊销当前会话（P1） |
| GET | `/v2/me` | U | 用户、角色、绑定的 MC 账号 |
| GET | `/v2/projects`、`/v2/projects/{p}` | U、V | |
| GET | `/v2/nodes` | A | |
| GET | `/v2/events?project=` | U | SSE；P4 起增加 `launch_intent` |
| POST | `/v2/lobby/link` | L | 为大厅里的玩家发起账号绑定（P4） |
| POST | `/v2/hooks/router` | I | |
| POST | `/v2/hooks/github` | G | 可选（P2） |

**项目级端点**

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| POST | `…/git/refresh` | D | |
| GET | `…/branches`、`…/commits`、`…/pulls` | V | `pulls` 为新增（P2） |
| GET、POST | `…/builds`（POST 仅 container/host 构建） | V、D | |
| GET | `…/builds/{id}`、`…/builds/{id}/log` | V、D | |
| GET | `…/builds/{id}/files/{name}` | V/R/S | |
| GET | `…/builds/{id}/pack.mrpack` | V/S | P2 |
| POST | `…/ci/builds` | C | push 模式（P2） |
| GET | `…/builds/{id}/source` | B | P5 |
| POST | `…/builds/{id}/result` | B | P5 |
| GET、POST | `…/rooms` | V、D | |
| GET | `…/rooms/{id}` | V/R | |
| PATCH | `…/rooms/{id}` | D | |
| DELETE | `…/rooms/{id}` | O | |
| POST | `…/rooms/{id}/start` | V | |
| POST | `…/rooms/{id}/stop`、`…/rooms/{id}/restart` | D | |
| GET | `…/rooms/{id}/pack.mrpack` | V/S | P2 |
| DELETE | `…/rooms/{id}/leases/{lease}` | U/R | |
| GET | `…/rooms/{id}/log`、`…/rooms/{id}/events` | D、V | |
| GET | `…/snapshots…` | | 见 [v0.1 原文](../directives/vex-v0.1-original.md) |
| GET | `…/worlds…`、`…/packs…` | | 见 [v0.1 原文](../directives/vex-v0.1-original.md) |
| GET | `…/hop/destinations` | R/L | |
| POST | `…/hop` | R/L | |

带 `Vex-On-Behalf-Of` 请求头的 R/L 请求，规则见 §11.3。

构建依赖和平台 agent 统一使用项目级 Build 文件路由。

## 14. 路线

| 阶段 | 内容 | 验收 |
| --- | --- | --- |
| P0 | 清理 encore 磁盘，核对 main 快照，仓库改名 | 根盘余量回升；main 快照可以恢复 |
| P1 | 重命名；配置系统；项目维度；权限；API v2；数据迁移；第二个项目；后端接口（Kubernetes 实现）；存储接口；认证接口；邀请登录与会话；单级房间地址；EULA；通用的世界发布 CLI | 两个项目并行运行；迁移项目数据完整；后端边界检查通过；成员通过邀请链接登录 |
| P2 | vex.toml；vex-action；CI pull 模式（GitHub App），之后做 push 模式；PR 房间、PR 评论和 check；mrpack；现有项目改用 CI 构建；GitHub 登录（device flow） | 在 example 项目打开 PR，几分钟内评论里出现地址和 mrpack，用 Prism 导入后能进入；ruri 点击「使用 GitHub 登录」能完成登录 |
| P3 | `on_new_head` 自动滚动；OIDC 登录；按 GitHub 权限推导角色 | push 后无人操作，房间自动滚动 |
| P4 | 大厅；vex-agent 界面；账号绑定；按指纹跳转；`launch_intent` | 原版客户端进入大厅，开一个 PR 房间，复制地址后用 Prism 进入；ruri 能自动重启进入 |
| P5 | Docker/Podman 后端；S3 存储；container 构建器；NeoForge；世界扫描器改写为 Go | 在一台只装了 Docker 的单机上，从零部署并跑通示例项目 |
| P6 | 公开发布 | 见 §15 |
| R1–R3 | ruri：实验性开关和上游对齐（与 P1 同期）；mrpack 同步（P2 之后）；`launch_intent`（P4 之后）；API 冻结后向上游提交 PR | 见 ruri 的执行指令 |

## 15. 公开发布前

- **许可证**：待定。
  - vexd 在 AGPL-3.0 和 Apache-2.0 之间选。如果不希望别人拿去做闭源托管服务，选 AGPL-3.0。
  - vex-agent 和 vex-action 建议用 MIT 或 Apache-2.0，方便模组作者直接引入。
  - ruri 中的代码随上游，为 GPL-3.0。
- **EULA**：文档里写明 vex 不分发服务端 jar，部署者需要自行同意 EULA，并配置 `accept_eula`。
- **仓库历史**：历史中有内网 IP、主机名、用户名和项目资产路径。公开前先审查历史，或者从一个干净的提交重新开始。
- **TLS**：为 API 配置 TLS，或者在文档中要求使用反向代理。push 模式必须启用 TLS。
- **交付物**：
  - 静态二进制；
  - systemd 单元示例；
  - Docker Compose 示例；
  - vex-tools 镜像，用 Nix dockerTools 构建，发布到 ghcr；
  - vex-action 发布到 GitHub Marketplace。
- **文档**：operator 指南（单机 Docker 起步）、vex.toml 参考、API 参考、ruri 实验性功能说明。
