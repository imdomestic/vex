# vex 设计

状态：草案 v0.1，2026-10-17。
现状依据：chorus-testbed `f035614` 的盘点报告 `STATE-REPORT.md`。
本文描述目标架构。每一轮的执行范围以 `docs/directives/` 中的指令为准。

## 1. 定位

vex 是 Minecraft 模组开发者自托管的持续部署测试平台，主要能力如下：

- 接入任意 Git 仓库，按提交构建。
- 在 Kubernetes（k3s）上为分支、提交和世界开房，经 mc-router 路由。
- 启动器（ruri）和游戏内的 vex-agent 走同一套 API，操作方式相同。

部署者（operator）是可信方。项目仓库、仓库内清单、构建产物和上传的世界都不可信。

chorus 是 vex 的第一个项目，vex 中不再保留任何 chorus 专属代码或语义。

## 2. 命名

| 对象 | 名称 |
| --- | --- |
| 守护进程 | `vexd` |
| 命令行 | `vex`（取代 chorusctl） |
| 房间内服务端模组 | vex-agent。mod ID 为 `vex_agent`，因为 NeoForge 的 mod ID 不允许连字符。Java 包为 `com.imdomestic.vex.agent` |
| 运维配置 | `/etc/vex/vexd.toml` |
| 仓库清单 | 模组仓库根目录下的 `vex.toml` |
| CLI 用户配置 | `~/.config/vex/config.toml` |
| 环境变量前缀 | `VEX_` |
| 数据目录 | 默认 `/var/lib/vex`；我们的部署用 `/srv/vex` |
| systemd 单元 | `vexd.service`、`vex-build@.service`、`vex-dns.service` |
| Kubernetes | 系统 namespace 为 `vex-system`；项目 namespace 为 `vex-p-<project>`；标签前缀为 `vex.imdomestic.com/` |
| 房间地址 | `<room>.<project>.<zone>:25565`；我们的 zone 是 `vex.internal` |
| GitHub | `imdomestic/vex`，由 chorus-testbed 改名而来 |

## 3. 信任模型

可信范围：operator 本人及其配置、vexd 所在主机、Kubernetes 控制面、归档存储。

不可信范围：

- 项目仓库的全部内容，包括构建脚本、Gradle 插件和 `vex.toml`；
- 构建产物；
- 在房间 JVM 中运行的项目模组；
- 上传的世界和资源包。

由此得出以下约束：

1. **构建只能在沙箱中执行。** 沙箱在 P2 实现。在此之前，只有 `trust = "trusted"` 的项目允许构建，其他项目的构建请求返回 `sandbox_unavailable`。
2. **清单取值都要按 operator 设定的范围校验。** `vex.toml` 只能从运行时目录里挑选镜像，资源申请不能超过项目配额。
3. **房间 token 只给最小权限。** 房间 token 应被视为"项目模组代码手里的凭据"，只能操作本项目、本房间、本 generation。
4. **游戏内委托操作要收窄范围。** vex-agent 和项目模组跑在同一个 JVM 里，所以游戏内委托操作的可信度不会高于项目代码本身。委托的范围要严格限定，并且可以按项目关闭（见 §11）。
5. **vexd 不代不可信输入访问任意 URL。** vexd 位于 LAN 和 tailnet 内，这样做可以避免 SSRF。依赖下载放在沙箱里完成。
6. **不泄露项目是否存在。** 请求无权访问的项目时一律返回 404，不返回 403。

## 4. 配置系统

### 4.1 来源与优先级

优先级从高到低为：**命令行 > 环境变量 > 配置文件 > 内置默认值**。vexd 和 vex CLI 共用同一套实现。

- **配置文件路径**：依次取 `--config`、`VEX_CONFIG`，都没有时用默认路径。
- **每个配置项都能用三种方式设置**，映射规则如下：
  - 配置文件：`[rooms.idle] commit = "10m"`
  - 环境变量：`VEX_ROOMS__IDLE__COMMIT=10m`。`.` 换成 `__`，全部大写。
  - 命令行：`--rooms.idle.commit=10m`。字段名中的 `_` 和 `-` 视为相同。
- **集合一律写成"以 ID 为键的表"**，即 `[projects.chorus]`，不使用 `[[projects]]`。这样环境变量和命令行也能定位到单个条目，例如 `VEX_PROJECTS__CHORUS__DEFAULT_BRANCH=dev`、`--projects.chorus.default_branch=dev`。
- **ID 格式统一。** 项目、节点、用户、世界、资源包的 ID 都满足 `[a-z][a-z0-9-]{0,22}`，且不能以 `-` 结尾，因此可以直接用作 DNS label。环境变量里，ID 段中的 `_` 解码为 `-`。字段名只用 snake_case，所以解码不会有歧义。
- **列表**：配置文件中写成 TOML 数组。环境变量和命令行中用逗号分隔；如果值以 `[` 开头，就按 TOML 字面量解析。
- **秘密**：只能通过 `*_file` 字段引用文件。环境变量可以给出 `*_file` 路径。命令行直接传秘密值一律拒绝，因为会出现在进程列表里。
- **未知键**：任何一层出现未知键（环境变量只检查 `VEX_` 前缀下的键）都直接报错，避免拼写错误被静默忽略。
- **诊断命令**：
  - `vexd config show --sources` 打印最终配置，并标出每个值来自哪一层。秘密只显示路径，不读取内容。
  - `vexd config check` 只做校验。
- **生效方式**：改配置后需要重启，暂不做热重载。

### 4.2 Nix

- vex 仓库的 flake 输出 `nixosModules.vexd` 和 `packages.vex`。
- `services.vexd.settings` 是自由格式的 attrset，经 `pkgs.formats.toml` 生成 `vexd.toml`。
- 模块级选项只保留少量几个：`enable`、`package`、`openFirewall`、`dataDir`。
- nix-config 中只 import 这个模块，再写 settings。
- 用户 token 在配置里只存 sha256，所以 settings 可以直接放进公开的 nix-config。

### 4.3 运维配置示例

```toml
[server]
listen = ["100.64.0.43:8790", "10.1.2.137:8790"]
public_base_url = "http://encore.inner.imdomestic.com:8790"
internal_base_url = "http://10.1.2.137:8790"
data_dir = "/srv/vex"
zone = "vex.internal"
router_address = "10.1.2.137:25565"

[auth]
signing_key_file = "/var/lib/vex/secrets/signing.key"

[users.linwhite]
admin = true
token_sha256 = "…"          # token 是 32 字节随机数，存 sha256 即可
minecraft = ["<uuid>"]      # 绑定的 MC 账号，用于白名单和游戏内委托

[users.hank]
token_sha256 = "…"

[nodes.encore]
k8s_name = "encore"
roles = ["persistent", "ephemeral", "build"]
max_rooms = 2
priority = 10

[nodes.seraph]
k8s_name = "seraph"
roles = ["ephemeral", "build"]
max_rooms = 4
priority = 20               # 数值大的优先

[runtimes.java25]
server_image = "itzg/minecraft-server:java25@sha256:7462fe34…"
build_image = "eclipse-temurin:25-jdk@sha256:…"   # P2 起使用

[rooms]
heap = "4G"
idle = { branch = "10m", commit = "10m", world = "10m" }
join_lease = "3m"
start_timeout = "5m"

[archive]
backend = "rsync-ssh"       # 也可以是 "local" 或 "none"
target = "vex-archive@tank.inner.imdomestic.com:/srv/archive/vex"
identity_file = "/var/lib/vex/ssh/id_ed25519"
known_hosts_file = "/var/lib/vex/ssh/known_hosts"
bwlimit = "5M"

[projects.chorus]
remote = "https://github.com/imdomestic/chorus.git"
default_branch = "main"
trust = "trusted"           # P2 之后默认为 "untrusted"
# credentials_file = "…"   # 私有仓库用：SSH 私钥或 HTTPS token
manifest_fallback = "/etc/vex/manifests/chorus.toml"   # 给没有 vex.toml 的旧提交用；P2 起生效
whitelist = []              # 成员以外额外放行的 MC 账号

[projects.chorus.members]
linwhite = "owner"
hank = "developer"

[projects.chorus.policy]   # P3 起生效
auto_build = ["main", "dev"]
on_new_head = "when_empty"
agent_delegation = true     # P4 起生效

[projects.chorus.quota]
max_rooms = 4
room_memory_max = "6Gi"
build_cpu = "2"
build_memory = "6Gi"
build_timeout = "20m"
build_cache = 20
snapshot_keep = 5
snapshot_keep_days = 14
```

## 5. 对象模型与身份

### 5.1 角色

- 全局角色只有 `admin`。
- 项目内角色从高到低为 `owner`、`developer`、`viewer`：
  - `viewer`：可以查看；可以启动、进入已经存在的房间。
  - `developer`：额外可以构建、新建房间、停止或重启房间、发布世界和资源包。
  - `owner`：额外可以做破坏性操作，包括删除房间记录、归档世界。
- 房间白名单由两部分组成：有 MC 账号的项目成员，加上 `projects.<p>.whitelist`。这取代原来的 `hank_whitelist`。

### 5.2 身份与 ID

- 所有记录都归属到项目。DB 主键为 `(kind, "<project>/<id>")`，counter 名为 `<project>:<name>`。
- Build ID 在项目内唯一，格式为 `<loader>-<sha12>-<recipe8>`。
  - recipe 哈希覆盖三部分：规范化后的清单 loader 段、运行时目录条目、注入的 vex-agent 版本。
  - P1 先沿用 `<loader>-<sha12>`，P2 再加上 recipe 段。
- Room ID 格式：
  - 分支房间：`<slug>-<loader>`
  - 提交房间：`c<sha8>-<loader>`
  - 世界房间：`w-<world>-<sha8>-<loader>`
- 原来的 asset 改名为 world（世界资产），pack 名称不变。
- k8s 对象名和文件相对路径都记在对象里，运行时不再从 ID 推导。

### 5.3 数据布局

- 本地：
  - `<data>/state/vexd.db`
  - `<data>/projects/<p>/{git.git, builds, snapshots, worlds, packs, cache}`
- 归档端：`<target>/projects/<p>/{snapshots, worlds, packs}`
- `<target>/legacy/` 保存平台以外的历史备份。

## 6. 仓库清单 vex.toml（P2）

```toml
schema = 1

[minecraft]
version = "26.3"
java = 25                    # 必须存在于 operator 的 runtimes 中

[loaders.fabric]
loader_version = "0.19.5"
build = ["./gradlew", ":fabric:build", "--no-daemon"]   # argv 数组，在沙箱内执行
agent = true                 # 是否注入 vex-agent；平台没有对应版本时报错

[[loaders.fabric.files]]
glob = "fabric/build/libs/*.jar"
exclude = ["*-sources.jar", "*-dev.jar", "*-javadoc.jar"]
side = "both"                # both | client | server

[[loaders.fabric.deps]]
url = "https://cdn.modrinth.com/…/fabric-api-0.161.0+26.3.jar"
sha256 = "…"                 # 必填
side = "both"

[world]
extra_root_entries = ["chorus-checkpoint.json"]   # 取代原先写死的允许表条目

[rooms]
memory = "4G"                # 不得超过 quota.room_memory_max
```

规则：

- **清单来源**：从固定的提交里读取清单。没有时用 `manifest_fallback`；两者都没有，该提交不可构建。
- **依赖下载**：所有依赖都必须带 sha256，由沙箱内的上传器下载、校验、上传。vexd 自己不去访问这些 URL。
- **世界元数据**：`requires.mods`（modId 列表）取代 `requires.chorusMod`。
- **mod ID 解析按 loader 分开**：Fabric 读 `fabric.mod.json`，NeoForge 读 `META-INF/neoforge.mods.toml`。原有的 zip 限额保留：256MiB，嵌套 8 层。
- **已知限制**：用 `git archive` 打包源码，不包含 submodule，也不展开 LFS。

## 7. 构建沙箱（P2）

构建以 Kubernetes Job 的形式运行在项目 namespace 里，节点从带 `build` 角色的节点中选。Job 分三步：

1. **init 1 取源码**：用 build token 下载 vexd 生成的 `git archive` 源码包，并校验 sha256。构建过程中不接触任何 git 凭据。
2. **init 2 构建**：镜像取 `runtimes.<java>.build_image`，执行清单里的 `build`。运行环境限制如下：
   - 非 root 运行，根文件系统只读；
   - 只有 `/work`（emptyDir）和 `/cache`（项目自己的 Gradle 缓存 PVC）可写；
   - CPU、内存、期限取自项目 quota。
3. **main 上传**：由 vex-tools 镜像执行，依次做三件事：
   - 按清单收集文件；
   - 下载 deps 并校验 sha256；
   - 用 build token 把文件和元数据上传给 vexd。

vexd 收到后再次校验大小和 sha256，解析 mod 元数据，然后发布 Build。

其他约束：

- **网络**：用 NetworkPolicy 限制，k3s 自带 network policy 控制器。
  - 出站：允许 DNS、vexd 的 API 端口、公网。
  - 拒绝：RFC1918、100.64.0.0/10、link-local 和集群网段。
- **缓存**：Gradle 缓存按项目隔离，不能跨项目共享，否则存在缓存投毒的风险。同一个项目同一时间只跑一个构建。
- **镜像打包**：vex-tools 镜像用 Nix `dockerTools` 构建，本机部署时用 `k3s ctr images import` 导入。
- **build token** 的权限只有三项：下载本 Build 的源码包、上传本 attempt 的结果、追加本 attempt 的日志。

## 8. 房间运行时与隔离

- **项目 namespace**：每个项目一个，由 vexd 创建，并附带三样东西：
  - ResourceQuota，取值来自项目 quota；
  - LimitRange；
  - NetworkPolicy，规则见下一条。
- **房间 Pod 网络**：
  - 入站只允许 mc-router。
  - 出站允许 DNS、vexd 的 API 端口和公网；拒绝 LAN、tailnet 和其他 namespace。
- **服务端镜像**：按 Java 版本从 `runtimes` 中取。itzg 的 `TYPE` 由 loader 决定，例如 fabric 对应 `FABRIC`，neoforge 对应 `NEOFORGE`。
- **房间 token 权限**：
  - 上传本房间本 generation 的快照；
  - 下载本 Build 的文件和本房间的 seed；
  - 查询和申请 hop；
  - P4 起还包括委托操作。
- **调度**：按 `nodes.*.roles` 和 `priority` 选择节点。分支房间只调度到 `persistent` 节点。节点磁盘指标改从 kubelet stats summary 获取，不再只采样 vexd 所在的主机。
- **mc-router**：需要确认它能否跨 namespace 发现 Service。如果不能，就由 vexd 通过 mc-router 的路由 API 下发映射。这一点待验证。
- **RBAC**：vexd 需要 ClusterRole 来管理 `vex-p-*` 下的资源，但 RBAC 无法按名字前缀限制，所以前缀检查只能写在 vexd 代码里。

## 9. 客户端指纹与跳转（P4）

- **指纹定义**：每个 Build 计算一个 `clientFingerprint`，取值为下列内容的 sha256：
  - 按 sha256 排序的 `side ∈ {both, client}` 文件 sha256 列表；
  - MC 版本、loader、loader 版本。
- **hop 条件**：目标 Build 的指纹等于来源 Build 的指纹，并且玩家对应的用户有权进入目标项目。满足这两点就能直接 transfer，原来"必须同一个 BuildID"的规则废除。
- **指纹不同时**：转为 `launch_intent`（见 §11）。
- **版本下限**：transfer 依赖原版 1.20.5 起提供的 transfer 包，目标服务器需要开启 `accepts-transfers`。低于 1.20.5 的项目不支持游戏内跳转。

## 10. 持续部署（P3）

- **`policy.auto_build`**：匹配的分支在镜像刷新后，如果出现新 head，自动发起构建。
- **`policy.on_new_head`** 决定分支房间如何处理新版本：
  - `notify`：只发 `update_available` 事件。
  - `when_empty`：新 Build 就绪后，房间一旦无人且没有租约，就滚动到新 generation，世界沿用原 PVC。
  - `immediate`：新 Build 就绪后立即滚动。在线玩家会收到提示，并被保存后踢出。
- 构建失败时，房间保持当前 generation，并发出 `failed` 事件。
- 继续用轮询拉取，不依赖 GitHub webhook，因为 vexd 可以只在内网可达。

## 11. 游戏内前端（P4）

vex-agent 是 API 的委托客户端。

- **界面**：命令 `/vex` 打开界面，优先用原版 1.21.6 起由服务端下发的 Dialog，这样客户端不需要额外装模组。Dialog 在 26.3 上是否可用待实测；不可用时退回可点击的聊天组件。
- **可做的事**：
  - 查看分支、提交和构建状态；
  - 查看兼容标记（直接进入或需要重启）；
  - 发起构建、开房、跳转。
- **委托接口**：`POST /v2/projects/{p}/rooms/{id}/delegate`，用房间 token 加玩家 UUID 调用。vexd 需要逐项校验：
  1. 该 UUID 绑定到某个用户；
  2. 该用户在项目内的角色足够；
  3. 该玩家当前在来源房间里，依据是 mc-router 的 connect 事件加 SLP sample；
  4. 项目的 `agent_delegation` 已开启。
  委托只开放构建、新建或启动提交房间、hop 这几项操作。
- **launch_intent**：当目标指纹不同时，vexd 在该用户的 SSE 上发出 `launch_intent {project, room, generation, buildId}`。用户在 ruri 中开启"允许游戏内切换"后，ruri 收到事件会依次：关闭游戏 → 同步模组 → 用 QuickPlay 重新启动。前提是 ruri 正在运行。

## 12. API v2 草案

**通用约定**

- 全部路径以 `/v2` 为前缀。下文中 `…` 代表 `/v2/projects/{p}`。
- 错误结构沿用 v1：`{error:{code,message,details?}}`。
- 请求无权访问的项目一律返回 404。

**权限缩写**

| 缩写 | 含义 |
| --- | --- |
| — | 无需认证 |
| U | 任意登录用户 |
| V / D / O | 项目内 viewer / developer / owner 及以上 |
| A | 全局 admin |
| R | 房间 token（本项目、本房间、当前 generation） |
| B | build token |
| S | 签名 URL |
| I | 可信的 mc-router 来源 |

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/v2/health` | — | 仅返回存活状态 |
| GET | `/v2/meta` | U | 版本、zone、运行时目录、可用的 agent 版本 |
| GET | `/v2/me` | U | 用户名、admin、MC 账号、各项目角色（新增） |
| GET | `/v2/projects` | U | 可见项目摘要（新增） |
| GET | `/v2/projects/{p}` | V | 项目详情：默认分支、loaders、policy、quota（新增） |
| GET | `/v2/nodes` | A | 节点、容量、磁盘 |
| GET | `/v2/events?project=a,b` | U | 全局 SSE，只推送可见项目，每个事件带 `project` |
| POST | `/v2/hooks/router` | I | mc-router 的 connect/disconnect 通知 |
| POST | `…/git/refresh` | D | |
| GET | `…/branches` | V | |
| GET | `…/commits?branch&limit&before` | V | |
| GET | `…/builds?sha&loader&status` | V | |
| POST | `…/builds` | D | `{sha, loader, force?}` |
| GET | `…/builds/{id}` | V | 带 `clientFingerprint`（P4） |
| GET | `…/builds/{id}/log` | D | |
| GET | `…/builds/{id}/files/{name}` | V/R/S | R 只能取本 Build 的文件 |
| GET | `…/builds/{id}/source` | B | P2 |
| POST | `…/builds/{id}/result` | B | P2 |
| GET | `…/rooms?kind&state` | V | |
| POST | `…/rooms` | D | `{kind: commit\|world, sha, loader, seed?, world?}` |
| GET | `…/rooms/{id}` | V/R | R 只能查自身和 hop 目标 |
| PATCH | `…/rooms/{id}` | D | `{label}` |
| DELETE | `…/rooms/{id}` | O | 只能删除 closed 状态的房间（新增） |
| POST | `…/rooms/{id}/start` | V | 返回 `{room, lease}` |
| POST | `…/rooms/{id}/stop`、`…/restart` | D | |
| DELETE | `…/rooms/{id}/leases/{lease}` | U/R | 只能删自己持有的租约 |
| GET | `…/rooms/{id}/log` | D | |
| GET | `…/rooms/{id}/events` | V | 单房间 SSE |
| POST | `…/rooms/{id}/delegate` | R | P4 |
| GET | `…/snapshots?room` | V | |
| GET | `…/snapshots/{id}` | V | |
| POST | `…/snapshots/{id}/restore` | D | |
| GET | `…/snapshots/{id}/download` | D/S | |
| POST | `…/snapshots` | R | 只能上传本房间本 generation 的快照 |
| GET | `…/worlds` | V | |
| GET | `…/worlds/{id}` | V | |
| GET | `…/worlds/{id}/revisions/{rev}` | V | 带 `tankVerifiedAt`（新增） |
| POST | `…/worlds/{id}/revisions` | D | multipart：元数据 + 世界 gzip |
| POST | `…/worlds/{id}/promote` | D | `{snapshot}` |
| PATCH | `…/worlds/{id}` | O | `{state}` |
| POST | `…/worlds/{id}/restore` | D | |
| GET | `…/worlds/{id}/revisions/{rev}/download` | D/R/S | R 只能取本房间的 seed |
| GET | `…/packs` | V | 新增 |
| POST | `…/packs/{id}/revisions` | D | |
| GET | `…/packs/{id}/{file}` | V/S | 客户端通过 server.properties 中的签名 URL 下载 |
| GET | `…/hop/destinations` | R | |
| POST | `…/hop` | R | |

**相对 v1 删除的接口**

- `/v1/deps/{sha}`：改为通过 Build 文件路由下载。
- `/v1/mods/chorus-hop/…`：vex-agent 改为普通的平台注入文件，同样走 Build 文件路由。

**SSE 事件**

保留 v1 的 `sync`、`room`、`build`、`ready`、`failed`、`branch_*`，并新增：

- 删除事件：`room_deleted`、`build_deleted`；
- 对象事件：`snapshot`、`world`、`pack`；
- `update_available`（P3）、`launch_intent`（P4）。

## 13. 路线

| 阶段 | 内容 | 验收 |
| --- | --- | --- |
| P0 | 清理 encore 磁盘；核对 main 快照；仓库改名 | 根盘余量明显回升；main 有可恢复的快照 |
| P1 | 重命名；配置系统；项目维度；权限；API v2；数据迁移；第二个项目；ruri 适配 v2 | 两个项目并行运行，chorus 数据完整 |
| P2 | vex.toml；构建沙箱；recipe 哈希；房间网络隔离 | 接入一个不受信任的第三方仓库，构建与运行均隔离 |
| P3 | `auto_build`、`on_new_head` | push 后无人操作，房间自动滚动到新版本 |
| P4 | vex-agent；客户端指纹跳转；Dialog；委托；`launch_intent` | 在游戏内切到另一个提交，ruri 自动重启并进入 |
| P5 | NeoForge；节点磁盘指标完善 | NeoForge 示例项目跑通 |
| P6 | 公开发布 | 见 §14 |

## 14. 公开发布前

- **清理历史**：仓库历史中有内网 IP、主机名、用户名和 chorus 资产路径。公开前要审查历史，或者从一个干净的提交重新开始。
- **TLS**：API 支持 TLS（`server.tls_cert_file` / `tls_key_file`），或者在文档里要求使用反向代理。
- **非 NixOS 部署**：提供静态二进制和 systemd 单元示例；vex-tools 镜像发布到 ghcr。
- **文档**：operator 指南、vex.toml 参考、API 参考、许可证。
