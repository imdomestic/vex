# vex 设计

状态：v0.5（2026-10-24）。P0、P1、P2 已完成；P3–P6 与 Ruri R3 暂停，等待新的需求和规划。本文标明现有实现与暂停阶段的设计；实际 HTTP 契约见 [API 文档](../api.md)，测试约束见 [开发说明](../development.md)。

## 1. 定位

vex 是 Minecraft 模组开发者自托管的持续部署测试平台。核心卖点是：**每个 PR 都有一个能直接进去玩的预览服。**

- **构建**：项目 CI 构建完成后把产物交给 vex；vex 内置的构建器只是备选。
- **运行**：vex 为分支、PR、提交和世界开房，经 mc-router 路由。
- **运行后端**：当前使用 Kubernetes（k3s）；Docker/Podman 属于暂停的 P5。
- **客户端**：
  - 任何启动器都能用：下载房间的 mrpack，复制服务器地址，就能进入；
  - 游戏内大厅属于暂停的 P4；
  - ruri 开启实验性功能后，可以一键进入。

部署者（operator）可信。项目代码、CI 产物、仓库内的清单、上传的世界都不可信。

**部署拓扑**：

- 一个部署者（一台机器或一个集群）只运行一个 vexd，项目都放在这个 vexd 里。
- 不同部署者各自运行自己的 vexd。客户端（ruri、vex CLI）可以同时连接多个 vexd，并把它们汇总显示。
- 同一台机器上也可以跑多个实例，只要数据目录、端口、zone 和 namespace 前缀互不冲突。
- 只有一个项目时，CLI 和 ruri 自动选中它。

chorus 是 vex 的第一个用户项目，vex 中不保留任何 chorus 专属代码。

## 2. 命名

| 对象 | 名称 |
| --- | --- |
| 守护进程 | `vexd` |
| 命令行 | `vex`，取代 chorusctl |
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
| 仓库 | `imdomestic/vex`，由 chorus-testbed 改名而来 |

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

1. **项目 CI 是构建主路径。** 当前保留的 `host` 构建只允许 `trust = "trusted"` 且 `build.source = "host"` 的项目使用。容器或 Job 构建器属于暂停的 P5。
2. **项目房间按运行不可信代码隔离。** Kubernetes 后端用 NetworkPolicy 实现；未来 Docker 后端需要明确网络隔离范围，并提供对应的 nftables 规则。
3. **三类 token 的权限：**
   - 房间 token：只能操作本项目、本房间、本 generation；
   - 大厅 token（P4 规划）：可以代表已绑定账号且当前在大厅里的玩家操作；
   - CI 推送凭据（P5 规划）：只能为指定仓库、指定工作流提交构建。现有 CI pull 使用平台 GitHub App 的 installation token 下载产物。
4. **vexd 不去访问不可信输入里给出的 URL**，避免 SSRF。依赖由 CI 或沙箱下载后一并提交给 vexd。
5. **无权访问的项目一律返回 404**，不暴露它是否存在。

## 4. 后端抽象

| 层 | 接口 | 实现与顺序 |
| --- | --- | --- |
| 运行 | `runtime.Backend`：Reference、EnsureProject、EnsureRoom、ObserveRoom、StopRoom（保存后停止）、ArchiveWorld、DeleteRoom、DeleteWorld、Logs、RCON、Capacity | `kubernetes` 已实现；`docker`（通过 Docker API，兼容 Podman）属于 P5 |
| 路由 | `router.Registrar` | mc-router 的 Kubernetes 注解（P1）；mc-router 的 Docker 标签或路由 API（P5）。具体方式需要实测 |
| 存储 | `archive.Store`：Put、Verify、Get、Stat，key 为 `projects/<p>/…` 形式的相对路径 | `local`、`rsync-ssh`（P1，现有）；`s3`（P5） |
| 构建 | Build 的 `source` 记录来源及执行身份 | `ci`（P2，主路径）；`host`（P1，仅限 trusted 项目）；`container` 属于 P5 |
| 认证 | 身份格式为 `provider:subject`，人工登录签发 vexd 自己的会话 | 邀请登录、静态服务账号凭据、GitHub App device flow 已实现；OIDC 属于 P3 |

单机 Docker 后端的规划（P5，暂停）：

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
- **集合用"以 ID 为键的表"**，例如 `[projects.chorus]`。这样环境变量和命令行也能单独定位某一个条目。
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
listeners = [
  { address = "100.64.0.43:8790", tls = true, scope = "public" },
  { address = "10.1.2.137:8790", tls = false, scope = "internal" },
]
tls_cert_file = "/var/lib/acme/vex.inner.imdomestic.com/fullchain.pem"
tls_key_file = "/var/lib/acme/vex.inner.imdomestic.com/key.pem"
public_base_url = "https://vex.inner.imdomestic.com:8790"
internal_base_url = "http://10.1.2.137:8790"
data_dir = "/srv/vex"
zone = "vex.internal"
router_address = "10.1.2.137:25565"

[minecraft]
accept_eula = true           # 部署者明确同意 Minecraft EULA 后才允许开房；默认 false

[runtime]
backend = "kubernetes"

[runtime.kubernetes]
kubeconfig_file = "/var/lib/vex/kubeconfig"
storage_class = "local-path"

[runtime.kubernetes.nodes.encore]
k8s_name = "encore"
roles = ["persistent", "ephemeral", "build"]
max_rooms = 2
priority = 10

[runtime.kubernetes.nodes.seraph]
k8s_name = "seraph"
roles = ["ephemeral", "build"]
max_rooms = 4
priority = 20

[runtimes.java25]
server_image = "itzg/minecraft-server:java25@sha256:7462fe34…"

[agents.fabric-26-3]
minecraft = "26.3"
loader = "fabric"
version = "0.3.0"
file = "/var/lib/vex/agents/vex-agent-fabric.jar"

[archive]
backend = "rsync-ssh"        # 当前支持 local、rsync-ssh

[archive.rsync_ssh]
target = "vex-archive@tank.inner.imdomestic.com:/srv/archive/vex"
identity_file = "/var/lib/vex/ssh/id_ed25519"
known_hosts_file = "/var/lib/vex/ssh/known_hosts"
bwlimit = "5M"

[auth]
signing_key_file = "/var/lib/vex/secrets/signing.key"
session_ttl = "2160h"        # 会话有效期，每次使用后顺延
invite_ttl = "24h"
admin_socket = "/run/vex/admin.sock"   # 本机管理入口，可直接执行 vexd user invite 等命令

[auth.providers.github]
type = "github"

[github.app]                 # 拉取 CI 产物、写 PR 评论和 check、GitHub 登录
app_id = 0
client_id = "…"
private_key_file = "/var/lib/vex/secrets/github-app.pem"

[users.linwhite]
admin = true
token_sha256 = "…"           # 可选：服务账号令牌，只给自动化和 CI 用；人工使用一律走登录
identities = ["github:<数字 ID>"]
minecraft = ["<uuid>"]

[users.hank]
token_sha256 = "…"

[projects.chorus]
remote = "https://github.com/imdomestic/chorus.git"
default_branch = "main"
trust = "trusted"
manifest_fallback = "/etc/vex/manifests/chorus.toml"

[projects.chorus.github]
repository = "imdomestic/chorus"
derive_roles = false         # 角色由 members 配置；GitHub 权限推导属于 P3

[projects.chorus.loaders.fabric]
enabled = true
agent = "fabric-26-3"         # 引用平台 agents 目录中的 ID

[projects.chorus.build]
source = "ci"                # 当前支持 ci、host

[projects.chorus.ci]
mode = "pull"                # vexd 轮询 GitHub 产物
workflow = ".github/workflows/vex.yml"
artifact = "vex-build"
pr_previews = true
fork_previews = "approval"   # off | approval（维护者评论 /vex preview）| auto
preview_access = "members"   # members | linked | public

[projects.chorus.members]
linwhite = "owner"
hank = "developer"

[projects.chorus.policy]
on_new_head = "when_empty"

[projects.chorus.quota]
max_rooms = 4
room_memory_max = "6Gi"
build_cache = 20
snapshot_keep = 5
snapshot_keep_days = 14
```

`agents.<id>` 是平台级目录，声明 Minecraft、loader、agent 版本及绝对 JAR 路径。项目通过 `projects.<project>.loaders.<loader>.agent` 引用目录 ID；仓库清单的 `agent = true` 声明该 loader 需要平台 agent。构建时校验版本和 loader，并把平台 JAR 及三个文件摘要写入 Build，客户端与服务端按文件的 side 选择文件。

### 5.4 HTTPS 与内部入口

公开客户端使用 `scope = "public"` 的 HTTPS 监听器；房间 Pod、归档 Job 和 mc-router 使用独立的 `scope = "internal"` HTTP 入口。内部入口核验真实来源及 Kubernetes 对象身份，随后继续执行对应路由的凭据和项目权限检查。宿主防火墙与项目 NetworkPolicy 同时限制访问；转发头不改变来源判定。

当前部署由 NixOS ACME 使用 Cloudflare DNS-01 签发并续期证书，DNS 凭据通过受限文件提供。证书签发不要求开放公网 HTTP 入口。vexd 启动时校验证书与私钥，每次完整 TLS 握手重新读取并校验；续期后通过 `systemctl reload vexd` 立即检查新文件，已有 API 与 SSE 连接继续运行。公开地址、邀请和下载链接使用 `public_base_url`，Pod 下载使用 `internal_base_url`。

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
- Build ID 格式固定为 `<loader>-<sha12>`。同一提交的多次构建（CI 重跑、force 构建、平台注入的 agent 升级）都记为同一 ID 的新 attempt，构建来源记在 `source` 中。每个项目只使用一种构建来源，因此不需要 recipe 段。
  - Build 记录 `source = {kind: ci|host, …}`；container 来源属于 P5 规划。
  - 每个文件都要记录 sha1、sha256、sha512 三种哈希，因为 mrpack 需要 sha1 和 sha512。
- Room ID 按房间类型区分：

  | 房间类型 | ID 格式 |
  | --- | --- |
  | 分支 | `<slug>-<loader>` |
  | PR | `pr<number>-<loader>` |
  | 提交 | `c<sha8>-<loader>` |
  | 世界 | `w-<world>-<sha8>-<loader>` |
  | 大厅（P4 规划） | `lobby`（不属于任何项目） |

- 文件路径和后端对象名都直接记录在对象里，运行时不从 ID 推导。
- 原 asset 改名为 world，pack 保持不变。
- 数据布局：
  - `<data>/state/vexd.db`
  - `<data>/projects/<p>/{git.git, builds, snapshots, worlds, packs, cache, rooms}`
  - 归档端：`projects/<p>/{snapshots, worlds, packs}` 和 `legacy/`

### 6.3 记录自包含

Build、Room、Snapshot、WorldRevision 保存创建或更新提交时的 `commit:{sha,subject,author,time}`；Build、Snapshot、WorldRevision 还保存当时解析的清单。已有记录的显示、启动、恢复、mrpack 导出和世界提升直接使用记录中的内容。

Git 镜像只负责取得当前远端历史和新的构建输入，正常执行 prune 与 gc。项目改写历史后，已无法从任何远端 ref 到达的提交记录标记 `commitMissingAt`，已有产物和世界继续可用；新建构建、在该提交上创建 commit/world 房间及导入其 CI 产物返回 `409 commit_unavailable`。提交重新出现时清除标记。

启动时按项目补录旧记录缺少的元数据与清单。补录期间该项目写操作及 mrpack 导出返回 `503 project_initializing`。远端刷新成功后仍不存在的提交列入补录 issues，项目继续开放；记录不一致或远端刷新失败需要明确报告并重试。

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
extra_root_entries = []

[rooms]
memory = "4G"
```

清单中的取值都要在 operator 给定的范围内校验。没有 `vex.toml` 的提交改用 `manifest_fallback`。

### 7.2 CI 产物交付（P2）

`vex-action` 读取 `vex.toml`，收集文件，下载依赖并校验哈希，最后生成一个 build bundle，内容为 `vex-build.json` 加全部文件。

bundle 有两种交付方式。

**pull 模式（已实现）：**

- CI 只执行 `actions/upload-artifact`。
- vexd 使用 GitHub App 的 installation token 做三件事：
  - 轮询 `ci.workflow` 下已完成的 run；
  - 下载名为 `ci.artifact` 的产物；
  - 从 GitHub API 读取并核验 `head_sha` 和 PR 信息。
- vexd 主动访问 GitHub，部署的 API 可以仅开放在内网。

**push 模式（P5 规划，暂停，要求 vexd 可通过公网 HTTPS 访问）：**

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
  2. 登记 `kind = "pr"`、ID 为 `pr<number>-<loader>` 的房间；`on_new_head = "when_empty"` 在无人且没有有效租约时应用新 head，`manual` 等待手动操作；
  3. 首次进入时才真正启动，无人时按 idle 规则停止。
- PR 关闭或合并时，房间标记 finished，关闭并归档。vexd 通过轮询 GitHub 发现状态变化。

**PR 评论**

vexd 通过 GitHub App 维护一条 PR 评论，房间状态变化时就地更新。评论包含：

- 服务器地址：`pr12-fabric--chorus.mc.example.com`；
- mrpack 下载链接；
- 当前提交和构建状态；
- 房间状态；
- 开启了 ruri 实验性功能时，附上 ruri 打开链接。

同时 vexd 写入一个 check run，供 PR 页面展示。

### 7.4 内置容器构建器（P5 规划，暂停）

内置构建器会在 Docker 容器或 Kubernetes Job 里构建，方式如下：

- **源码**：由 vexd 打成 `git archive` 包，通过 build token 下载。
- **构建环境**：非 root 运行，根文件系统只读。
- **缓存**：每个项目独立的 Gradle 缓存。
- **网络**：禁止访问 LAN、tailnet 和集群内部，只放行回传给 vexd 的端口。
- **产物**：打成和 CI 相同格式的 build bundle 交回 vexd。

当前 `host` 构建只允许 operator 明确信任的项目使用。

### 7.5 GitHub App 注册与 DeviceFlow

正式注册命令为 `vex github create-app --owner OWNER --name NAME --out DIRECTORY`。命令使用 GitHub App manifest 注册流程，启动本机 loopback 回调并打开浏览器；注册目录权限为 0700，生成的 `app.json`、`private-key.pem` 和 `client-secret` 文件权限为 0600。已有 App 元数据时复用登记信息；目录存在未完成注册的标记或孤立凭据文件时拒绝再次注册。用户在 GitHub 启用 DeviceFlow 并安装 App 后，将 ID、client ID 和私钥文件路径配置给 vexd，通过 `vexd github check` 验证安装和仓库权限。

GitHub 登录使用同一个平台 App。`POST /v2/auth/device` 创建流程，`poll` 领取结果，`cancel` 取消流程。等待中的授权保存在 `@platform` 的 `device_attempt` 记录中，包含 handle 摘要、GitHub device_code、interval、到期时间、首次 poll 确定的客户端名称及已取得的身份或错误。vexd 重启后恢复尚未到期的后台查询。成功领取、取消或到期会删除记录；GitHub access token 只用于读取账号身份，客户端取得的是 Vex 会话。日志和普通 API 响应不包含 device_code。

## 8. 房间运行时

- **服务端 jar**：vex 不分发服务端 jar，由 itzg 镜像在运行时下载。
  - 只有 `minecraft.accept_eula = true` 时，vexd 才会传 `EULA=TRUE`；
  - 未同意时，开房请求返回 `eula_not_accepted`。
- **镜像与类型**：服务端镜像按 Java 版本取自 `runtimes`。itzg 的 `TYPE` 由 loader 决定。
- **房间 token 的权限**：
  - 上传本房间本 generation 的快照；
  - 下载本 Build 的文件和本房间的 seed；
  - 查询和申请 hop；
  - `Vex-On-Behalf-Of` 委托操作属于暂停的 P4 规划（见 §11）。
- **Kubernetes 后端**：
  - 每个项目一个 namespace，配有 ResourceQuota、LimitRange 和 NetworkPolicy；
  - 入站只允许来自 mc-router；
  - 出站只允许 DNS、vexd 的 API 端口和公网；
  - 调度依据节点的 roles 和 priority；
  - 磁盘指标取自 kubelet stats summary。
- **Docker 后端（P5 规划，暂停）**：
  - 每个项目一个 bridge 网络；
  - 文档提供 nftables 规则，用来禁止房间访问私有地址段。
- **连接唤醒（P2）**：房间停止后，它的地址并不下线，而是改由 vexd 内置的一个最小 Minecraft 协议应答器接管。
  - 服务器列表 ping 时，返回 MOTD「预览服已休眠，连接即可唤醒」。
  - 玩家连接（login start）时，先检查该玩家是否有权进入这个房间，然后启动房间，并以「房间正在启动，请稍候重新连接」断开。没有权限的玩家只会收到说明，不会触发启动。
  - 房间就绪后，地址切回真正的服务端。
  - 唤醒按房间限速。应答器的 MOTD 不包含房间身份信息，因此不会让就绪校验误判。
  - Kubernetes 后端在房间停止时将 Service 设为无 selector，并用手动 EndpointSlice 指向 vexd 应答端口。恢复 Pod selector 前，必须通过 Pod exec 执行 `mc-monitor status -host 127.0.0.1 -port 25565 -timeout 3s -json`，检查实际服务端 SLP 中的 project、room、generation、build 身份。随后再经 mc-router 完成客户端路径的 SLP 核验，才将房间标记 ready。
  - 未来 Docker 后端必须通过 `docker exec` 在目标容器中执行相同的 `mc-monitor` 身份检查，并完成经路由的 SLP 核验。
  - 这样任何启动器只需要导入 mrpack 并填写地址，不需要事先通过 API 开房。

每次 start 受理都会发放一份加入租约；同一活跃周期保持 generation。默认租约有效期为三分钟，客户端等待期间每 120 秒重新 start 续租，完成或放弃加入时释放自己取得的租约。有效租约阻止空闲关闭和 PR 无人自动滚动；客户端必须核对 project、room、generation、build，generation 改变即结束当前加入操作。

### 8.1 分支房间重置世界

`vex room reset-world <room> -p <project> [--seed SEED]` 要求操作者输入完整房间 ID，再调用 `POST …/rooms/{id}/reset-world`。操作只允许具有项目访问权限的 admin，且房间必须是 branch、处于 stopped/closed、标记 finished、保留世界，并有已经通过归档校验的最新 generation 快照。

后端按实际卷 UID 删除世界并等待完成，清除运行记录中的世界状态，写入指定 seed（默认 empty），清空 `seedResolved`，保存并发布 `world_reset`。记录包含项目、房间、generation、卷 UID、快照 ID、seed 与时间。已有快照和世界资产保留；房间的 label、state 和 generation 保持原值，操作者设回 testing 后，下一次启动创建新世界。

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
- **`versionId`**：Build 包为 `<buildId>-a<attempt>`；房间包为 `<room>-g<generation>-<buildId>-a<attempt>`。

`clientFingerprint` 是紧凑 JSON `{"minecraft":"…","loader":"…","loaderVersion":"…","sha512":["…"]}` 的 UTF-8 字节 SHA-256；字段顺序固定，sha512 只包含 client/both 文件，统一小写、去重并按字典序排列。Ruri 使用该指纹复用已经导入的客户端实例；P4 的跨构建游戏内跳转规划也需要该指纹匹配。

导入兼容性需要同时验证标准 MRPack 依赖字段、文件 side、下载摘要以及房间 `servers.dat`。Ruri 使用宿主已有 MRPack 导入流程，并在本地准备完成后重新核对加入目标；普通准备操作不会启动玩家游戏。

## 10. 世界发布（P1 起通用化）

- **CLI 命令**：
  - `vex world publish <目录或 tar.gz> -p <project> --world <id>`
  - `vex world promote`
  - `vex pack publish <zip> -p <project> --pack <id>`
  - 对应的 API 是 `…/worlds/*` 和 `…/packs/*`。
- **d2map**：后续集成可以调用通用 `vex` CLI，当前范围不修改 d2map。
- **世界扫描器**：当前使用已有 Python 实现；Go 实现属于暂停的 P5。

## 11. 游戏内前端：大厅与 vex-agent（P4 规划，暂停）

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

### 12.1 模块与宿主边界

通用宿主提供 `RuriCore/Experimental`、`RuriExtensionKit` 和 `RuriCommandKit`。Vex 由 `RuriVex`、`RuriVexUI`、`RuriVexCLI` 三个模块组成，通过编译期功能登记取得宿主能力。模块使用宿主提供的设置、账号、文件、MRPack、实例、游戏启动和会话接口；宿主通用模块不依赖 Vex。

应用沿用上游 bundle ID、数据目录、CLI 名称、更新、语言和签名机制。通用修复与扩展机制分别提交；涉及上游接收和公开发布的决定由用户确认。

### 12.2 编译开关与运行时开关

`RURI_ENABLE_VEX=0` 在 Package.swift 中排除三个 Vex 模块、对应测试和 EventSource 依赖，也排除 Vex 资源与 Vex 的 `ruri` URL scheme 注册；`ruri://vex` 中的 host 为 `vex`。启用构建默认编译这些模块，运行时开关默认关闭。运行时关闭后，GUI、CLI 帮助、命令发现及 schema 隐藏 Vex，功能任务和订阅停止，不发生 Vex 网络请求或凭据读取。

`scripts/swift-build.sh` 统一使用 `--force-resolved-versions`，并将构建目录按 `with-vex`、`without-vex` 隔离；build、test 和 show-bin-path 共用这项选择，应用打包从对应目录收集资源。直接调用 SwiftPM build/test 同样需要该参数。原始 `swift package resolve` 会按当前依赖图重写锁定文件，更新依赖时应使用启用配置；关闭构建仍可能下载锁定文件中的未使用 pin，其依赖图和产物不包含 EventSource。Nix 提供启用的 `ruri-launcher` 与关闭的 `ruri-without-experiments` 两种产物，仅在构建副本中筛选锁定依赖。签名使用上游 `RURI_SIGN_IDENTITY`。

### 12.3 登录与安全存储

设置中的服务器列表分别保存地址与登录状态，支持邀请和 GitHub DeviceFlow；服务器页与 Git 页按服务器、项目展示。认证任务由功能生命周期管理，表单关闭、功能禁用及宿主退出时取消；异步操作在写入凭据和设置前检查取消状态及当前操作归属。

通用 `ExperimentalSecureStorage` 为每个扩展固定 `dev.ruri.launcher.ext.<id>` 命名空间，Vex 使用 `dev.ruri.launcher.ext.vex`，account 为规范化服务器 URL。创建的 Keychain 项目使用实际主程序与内置 CLI 的信任信息，并配置删除权限。通用文件 Keychain 删除 helper 按 service/account 查找唯一实际引用并调用 `SecKeychainItemDelete`，缺失视为成功，多个匹配或引用类型不符立即报错；宿主账号与扩展安全存储共用该实现。

正式首次读取会把旧 `dev.ruri.launcher.vex.api` 条目迁移至通用命名空间，完成新值写入和旧条目清理后才缓存。已有新值时继续清理残留旧条目。`--dry-run` 可以读取现有或旧条目，保持不迁移、不写入、不缓存旧值。

### 12.4 MRPack 与加入流程

Ruri 使用宿主 `MRPack.swift` 导入真实房间包，按 `clientFingerprint` 复用实例。加入期间每 120 秒续租，并用 SSE 和实际房间状态核对 project、room、generation、build；本地准备完成且目标 ready 后才启动一次游戏。准备命令只导入本地实例，launch 的 `--dry-run` 只返回计划，不开房、续租、下载或启动玩家游戏。

## 13. API v2

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
| S | 签名 URL |
| I | mc-router |

**平台级端点**

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/v2/health` | — | |
| GET | `/v2/meta` | — | 服务器名称、版本、可用的登录方式（未登录也能读）；登录后额外返回 zone、运行时目录、agent 版本 |
| POST | `/v2/auth/invites` | U/A | 普通用户只能给自己发（用于新设备），admin 可以给任何用户发；返回链接 `<public_base_url>/i/<code>`，只显示这一次（P1） |
| GET | `/i/{code}` | — | 显示一个说明页，告诉对方去哪里粘贴这个链接；不会兑换邀请，避免聊天软件预取链接时把邀请用掉（P1） |
| POST | `/v2/auth/invites/redeem` | — | 请求体 `{code, client}`，换取会话 token；有频率限制（P1） |
| POST | `/v2/auth/device`、`/v2/auth/device/poll`、`/v2/auth/device/cancel` | — | 创建、领取及取消持久化的 GitHub DeviceFlow |
| GET、DELETE | `/v2/me/sessions`、`/v2/me/sessions/{id}` | U | 查看或吊销自己已登录的设备（P1） |
| POST | `/v2/auth/logout` | U | 吊销当前会话（P1） |
| GET | `/v2/me` | U | 用户、角色、绑定的 MC 账号 |
| GET | `/v2/projects`、`/v2/projects/{p}` | U、V | |
| GET | `/v2/nodes` | A | |
| GET | `/v2/events?project=` | U | SSE，支持多项目订阅和断线后的状态同步 |
| POST | `/v2/hooks/router` | I | |

**项目级端点**

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| POST | `…/git/refresh` | D | |
| GET | `…/branches`、`…/commits`、`…/pulls` | V | `pulls` 为新增（P2） |
| GET、POST | `…/builds`（POST 仅 host 构建） | V、D | |
| GET | `…/builds/{id}`、`…/builds/{id}/log` | V、D | |
| GET | `…/builds/{id}/files/{name}` | V/R/S | |
| GET | `…/builds/{id}/pack.mrpack` | V/S | P2 |
| GET、POST | `…/rooms` | V、D | |
| GET | `…/rooms/{id}` | V/R | |
| PATCH | `…/rooms/{id}` | D | |
| DELETE | `…/rooms/{id}` | O | |
| POST | `…/rooms/{id}/start` | V | |
| POST | `…/rooms/{id}/stop`、`…/rooms/{id}/restart` | D | |
| POST | `…/rooms/{id}/reset-world` | A | 已归档、停止且 finished 的分支房间，必须确认房间 ID |
| GET | `…/rooms/{id}/pack.mrpack` | V/S | P2 |
| DELETE | `…/rooms/{id}/leases/{lease}` | U/R | |
| GET | `…/rooms/{id}/log`、`…/rooms/{id}/events` | D、V | |
| GET、POST | `…/snapshots…` | V/D/R/S | 列表、上传、下载和恢复，逐路由权限见 API 文档 |
| GET、POST、PATCH | `…/worlds…`、`…/packs…` | V/D/O/R/S | 世界版本、提升、归档、恢复及资源包，逐路由权限见 API 文档 |
| GET | `…/hop/destinations` | R | |
| POST | `…/hop` | R | |

完整方法、路径、权限和数据模型以 [API 文档](../api.md) 为准；其中的路由目录由 `TestDocumentedRoutesMatchRouter` 与实际注册表逐项比较。大厅、委托、CI push、容器构建回传及 GitHub webhook 入口尚未注册，实施范围需按暂停阶段重新规划。

## 14. 路线

| 阶段 | 状态 | 内容与验证范围 |
| --- | --- | --- |
| P0 | 已完成 | 磁盘整理、main 快照核对和仓库命名 |
| P1 | 已完成 | 配置、项目与权限、API v2、数据迁移、多项目、Kubernetes 和存储接口、邀请与会话、房间地址、EULA、通用世界发布 |
| P2 | 已完成 | vex.toml、vex-action、GitHub App 与 CI pull、PR 房间与评论/check、MRPack 与指纹、连接唤醒、GitHub DeviceFlow；真实 main CI 构建使用空 seed 和新 PVC 完成启动、SLP、停止及归档校验，已有数据保留 |
| P3 | 暂停，等待新的需求和规划 | 分支自动滚动、OIDC、GitHub 权限推导；PR 的 when_empty 策略已在 P2 实现 |
| P4 | 暂停，等待新的需求和规划 | 大厅、账号绑定、委托、按指纹跳转、launch_intent |
| P5 | 暂停，等待新的需求和规划 | Docker/Podman、S3、容器构建器、NeoForge 运行支持、Go 世界扫描器、CI push |
| P6 | 暂停，等待新的需求和规划 | 公开发布，见 §15 |
| R1、R2 | 已实现，独立完成 Ruri 发布验收 | 通用扩展机制、编译与运行时开关、登录与安全存储、MRPack 导入及指纹复用、GUI/CLI 加入流程 |
| R3 | 暂停，等待新的需求和规划 | launch_intent 与相关游戏重启流程 |

## 15. 公开发布规划（P6，暂停）

- **许可证**：待定。
  - vexd 在 AGPL-3.0 和 Apache-2.0 之间选。如果不希望别人拿去做闭源托管服务，选 AGPL-3.0。
  - vex-agent 和 vex-action 建议用 MIT 或 Apache-2.0，方便模组作者直接引入。
  - ruri 中的代码随上游，为 GPL-3.0。
- **EULA**：文档里写明 vex 不分发服务端 jar，部署者需要自行同意 EULA，并配置 `accept_eula`。
- **仓库历史**：公开前审核全部可达历史、当前文件、二进制与归档成员中的敏感内容，确认候选分类完整。
- **TLS**：当前 API 使用 DNS-01 证书和分离的 HTTPS/内部 HTTP 监听器。未来公网 CI push 同样必须启用 TLS。
- **交付物**：
  - 静态二进制；
  - systemd 单元示例；
  - Docker Compose 示例；
  - vex-tools 镜像，用 Nix dockerTools 构建，发布到 ghcr；
  - vex-action 发布到 GitHub Marketplace。
- **文档**：operator 指南（单机 Docker 起步）、vex.toml 参考、API 参考、ruri 实验性功能说明。

## 16. 测试环境独立性

Vex 的测试和运行脚本不得依赖被测试项目的当前历史、内容或分支。需要 Git 的测试创建自己的真实仓库；需要 Build 的测试使用固定文件经真实 CI bundle 导入。固定小文件保存在 `controller/internal/testfixture/testdata`，较大文件由独立目录提供，统一通过 `fixtures.json` 校验 SHA-256 和大小。

真实集成测试通过明确的环境变量选择服务、测试项目、namespace、数据目录和端口，使用真实 Kubernetes、GitHub、归档和 HTTP 行为。项目仓库改写历史或删除分支不会使固定测试文件失效。测试命令、条件及外部文件位置见 [开发说明](../development.md)，部署验证见 [部署说明](../../deploy/README.md)。
