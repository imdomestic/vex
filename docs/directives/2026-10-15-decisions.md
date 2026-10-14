# Chorus Testbed · 裁决与挂机执行指令（2026-10-15）

> 给实现 agent。这是一份**执行指令**，不是讨论稿。读完后立即开始，**不要等待任何确认**——Linwhite 已经睡了，醒来前不会回复你。
>
> 效力顺序：**本文件 > `docs/interface-proposals.md`（按本文件修订后）> `chorus-testbed-plan.md`**。本文件没提到的地方，照原计划执行。

---

## 0. 开工前的第一组动作（按顺序）

1. Mac 防休眠：`nohup caffeinate -ims -t 43200 >/dev/null 2>&1 &`（12 小时）。确认 `pmset -g assertions` 里有它。
2. 把本文件原样存进仓库：`docs/directives/2026-10-15-decisions.md`。
3. 把 §2–§4 的裁决写进 `docs/decisions.md`（每条 2–4 行：结论 + 理由）。
4. 新建 `docs/api.md`：以原计划附录 A 为底，并入 §3 中被采纳的提议和修订。**它是 chorusd 与 ruri 之间唯一的接口依据**，之后两边都按它实现；接口要变，先改它，再写进 decisions。
5. 在 `docs/progress.md` 顶部写一行：挂机开始时间，以及 §5.1 的环境自检结果。
6. 新建 `docs/morning-report.md`，按 §6.7 的格式先写个骨架。之后每完成一个阶段更新一次。

---

## 1. 对 P0 的结论

- P0 **通过**，证据充分。旧服务的归档和停用处理得很稳妥。
- 从现在起**速度优先**：证据就是验收测试本身，加上一段简短日志，不再写 P0 那种逐字节取证级别的文档。`progress.md` 每条一两行；长日志放 `.work/`，文档里只写路径。
- 接口提议整体偏重。§3 里已经砍掉了不必要的复杂度（尤其是第 3 项），**照裁决实现，不要再把复杂度加回去**。

---

## 2. 环境与配置定案

| 项目 | 定案 |
|---|---|
| chorus 来源 | `https://github.com/HCHogan/chorus.git`，匿名 HTTPS 只读，**不需要部署密钥**。`git.default_branch = "main"`。迁移到 `imdomestic/chorus` 的要求照 `REPOSITORY-MIGRATION.md`，今晚不做。 |
| 分支房间 | 当前远端只有 `main`，所以只有 `main-fabric`。原计划示例里的 `dev-fabric` 一律换成 `main-fabric`。 |
| Fabric 构建 | `:fabric:assemble`；产物、依赖、属性名照 `env-report.md`。NeoForge 关闭。 |
| 构建资源 | 照 `decisions.md` 的参数：ActiveProcessorCount=2、`--max-workers=2`、Gradle 堆 1536m；**构建进程组** MemoryHigh=5G、MemoryMax=6G、CPUQuota=200%；**同一时刻只允许一个 Gradle 构建**。用 systemd 的哪种方式实现由你决定，写进 decisions。 |
| 镜像 | 用 `p0-image.md` 中钉住的两个 digest。 |
| chorusd 地址 | listen `100.64.0.43:8790`、`10.1.2.137:8790`；`public_base_url = http://encore.inner.imdomestic.com:8790`；`internal_base_url = http://10.1.2.137:8790`；`data_dir = /srv/chorus`（由你创建，属主为系统用户 `chorusd`）。 |
| chorusd 部署 | 代码在 chorus-testbed 仓库的 `nix/` 下（包 + NixOS module），沿用 `nix/bootstrap` 的 extendModules 方式组合到 encore 的系统里。**不碰 `imdomestic/nix-config` 仓库**。切换系统时必须遵守 §6.4。 |
| 代码同步到 encore | 在 encore 上建裸仓库 `~/git/chorus-testbed.git`，作为 Mac 仓库的 `encore` remote。**今晚不建 GitHub 仓库，不 push 到 GitHub。** |
| secrets | 用户 token（linwhite、hank 各一个）和签名密钥由你在 encore 上生成，放在 `/var/lib/chorusd/secrets/`（root:chorusd，0640）。module 只接收文件路径；以后迁到 sops-nix 由用户来做。**token 不得出现在日志、文档、git 或聊天里。** Mac 端 token 写进钥匙串。 |
| 白名单 | Linwhite 的 MC 用户名用已安装 Ruri 的账号列表读取（**只读用户名，不读 token**）。hank 的用户名待用户提供，配置里留空数组项并加注释。 |
| 容量 | `max_running`：encore=2、seraph=4，保持不变。P2 要实测"两个房间 + 一次构建"同时运行时 encore 的 `MemAvailable`。余量低于 2 GiB 就把 encore 改为 1，并记入 decisions。 |
| 空闲时间 | commit、asset、branch 房间都是 10 分钟；加入租约 3 分钟。 |
| 资源包敏感性 | 按敏感处理（原计划默认）。 |
| ruri fork | 在本地 `/Users/linwhite/ruri-chorus` 克隆，remote `upstream` 指向 guh0613/ruri，主线 `dev`。**改成独立应用**：bundle id `dev.ruri.launcher.chorus`，应用名 `Ruri Chorus`，支持目录 `~/Library/Application Support/Ruri Chorus`。**绝不读写已安装 Ruri 的数据目录**（§5.3 的测试实例除外）。禁用 Sparkle。今晚不建 GitHub fork。构建一律带 `--build-system swiftbuild`（见 `p0-ruri.md`）。 |
| d2-map-dev | 在分支 `feat/chorus-publish` 上新增命令 `d2map chorus publish`。**不要改名、也不要复用**现有的内部函数 `worlds.publish(cfg)`。**不合并**到它的主线，留给用户审。 |
| 第一个资产 | id `plaza`，标题"高塔 · 中庭广场样板"，来源 `/home/linwhite/d2-assets/data/handmade/v3-plaza/sample/world`。全图 `v3-full`（1.5 GiB）作为可选的第二个资产 `tower-full`，P5 验收通过后再做。 |
| tank | 归档根目录 `tank:/srv/archive/chorus/`，子目录 `snapshots/`、`assets/`、`packs/`。chorusd 的**生产 SSH 身份需要用户提供**（§7）。 |

### 2.1 DNS 定案

- 目标 zone 是 `chorus.internal`。在 encore 上用 dnsmasq（放进同一个 NixOS module）只应答这一个 zone：`*.chorus.internal → 100.64.0.43`。配置要点：`no-resolv`、`bind-dynamic`，只监听 `100.64.0.43` 和 `10.1.2.137`。防火墙在 `tailscale0` 和 `wlo1` 上放行 53/udp+tcp、8790/tcp、25565/tcp。
- **客户端怎么解析这个 zone**：最终方案是用户在 Headscale 上配置 split DNS（这是用户的事）。今晚：
  1. Mac 上 `sudo -n true` 能成功 → 写 `/etc/resolver/chorus.internal`（内容 `nameserver 100.64.0.43`），再用 `dscacheutil -q host -a name main-fabric.chorus.internal` 验证。
  2. 做不到 → **临时**把 `minecraft.zone` 设为 `100-64-0-43.sslip.io`（公共通配 DNS，任意 `<x>.100-64-0-43.sslip.io` 都解析到 100.64.0.43），同样先验证能解析。在 decisions 里注明"临时 zone，晨间切回"。切回只需改配置；reconciler 必须能把已有 Service 的注解同步到新 zone。
  3. 两条都不通 → 所有真实进房验收都标为 `待用户`。

---

## 3. 接口提议裁决（`interface-proposals.md` 第 1–10 项）

| # | 裁决 | 修订 |
|---|---|---|
| 1 构建列表 | **采纳** | — |
| 2 Build 字段与重建 | **采纳** | 409 的错误码改用新增的 `conflict`（见 §3.4） |
| 3 SSE 重放 / sync / 首次 ACK | **否决原方案，改用 §3.1** | 不做 `eventCursor`、不做持久事件日志、不加 `confirmation` 字段 |
| 4 generation 与新启动周期 | **采纳** | 原方案里所有"409 invalid_request"改为 `409 conflict` |
| 5 `branch:` 种子 | **采纳，按 §3.2 修订** | 没有快照时回退，不再返回 404 |
| 6 资产 revision 下载 | **采纳** | — |
| 7 发布对象与 revision | **采纳，按 §3.3 修订** | 资产 revision 改为递增整数，并新增命名空间约束 |
| 8 hop 目的地与租约 | **采纳** | 返回体为 `{room, lease}`，去掉 `eventCursor` |
| 9 Snapshot 与恢复 | **采纳** | 另加：start 遇到"只在 tank 上"的种子快照时自动恢复（§3.2） |
| 10 资产从 tank 恢复 | **采纳** | — |

P1 需要实现第 1、2、3（按修订）、4、5 项。第 6–10 项的契约现在就写进 `docs/api.md`，到对应阶段再实现。

### 3.1 就绪判定：改为电平触发（替代第 3 项）

- **Room 的 `state=ready` 只能在验证通过后写入。** 验证条件：Pod Ready，**并且**经 mc-router 做 SLP 读到的 MOTD 含有 `room=<id> build=<buildId> generation=<n>`，三项全部匹配。
- 对已经是 ready 的房间调用 `POST /v1/rooms/{id}/start` 时，chorusd 在响应前**同步重新做一次 SLP 验证**（超时 3 秒）：
  - 通过 → 返回 `200` 和 ready 的 Room；
  - 失败 → 房间回到 `starting`（phase 为 `re-verify`），返回 `202`。
  - 其他情况照第 8 项：已 ready 返回 200，其余返回 202。
- SSE（全局和单房间两个路径不变）：
  - **每次连接的第一条事件都是 `sync`**，内容为 `{rooms: Room[], builds: Build[]}`，范围就是这条订阅的范围。之后推送实时事件：`room`（完整 Room）、`build`（完整 Build）、`ready`（`{id, generation, host, port, buildId}`）、`failed`（`{id, generation, error}`），以及 git 变动事件。
  - 每 15 秒发一次 `: ping`。
  - 事件 `id` 用内存里的递增序号，只用于调试。服务端**忽略** `Last-Event-ID`，因为每次重连都会重新 sync。
- **ruri 的进入规则（电平触发）**：
  - 从 start 响应中取目标 `(id, G=generation, B=buildId)`。
  - 只要从任何来源（start 响应、`sync`、`room` 事件、`ready` 事件）观察到某个 Room 同时满足 `id` 匹配、`generation==G`、`buildId==B`、`state==ready`，**并且**本地模组已经准备好，就启动游戏。每次加入操作**最多启动一次**。
  - 一旦观察到 `generation≠G`，或 `state` 变为 `failed`、`stopping`、`archiving`、`closed`、`lost` 中的任何一个，就放弃本次加入，并显示原因。
  - 等待期间每 120 秒重新调用一次 start 来续租约（同一 generation 只是发放新租约）。
- **chorus-hop**：照第 8 项，每 2 秒查询一次 Room，`generation` 和 `buildId` 都匹配且状态为 ready 时执行 transfer。

### 3.2 种子修订（第 5、9 项）

- `branch:<name>` 在 start 被受理时解析一次，按以下顺序取：
  1. 房间 `<slug(name)>-<loader>` 最新一个已发布的快照；
  2. 若没有快照，取该房间的 `seedResolved`（也就是它的初始世界来源）；
  3. 若仍为 null，取 `empty`。

  解析结果写进 `seedResolved`。源房间不存在时返回 `404 not_found`。
- 选中的快照只在 tank 上（P6 之后才可能出现）时，start **不报错**：房间进入 `queued`，phase 为 `restoring seed`，chorusd 自动恢复后继续启动；恢复失败则房间进入 `failed`，错误码 `archive_failed`。`error.details.snapshotId` 保留，只用于展示。
- 其余内容照第 5、9 项，包括 Minecraft 版本一致性检查和 start 的可选 `seed` 字段。

### 3.3 发布修订（第 7 项）

- 资产 revision 改为**每个资产各自递增的整数**，用字符串表示（`"1"`、`"2"`…）。服务端为每个 revision 记录世界文件的 sha256。上传的世界 sha256 与 `latestRev` 相同时，返回 `200` 和已有的 revision（幂等重试）；否则返回 `201` 和新 revision。`promote` 走同样的规则。
- 资产 id 必须匹配 `^[a-z0-9][a-z0-9-]{0,15}$`。
- `asset.json.requires` 新增 `namespaces: string[]`：由发布工具扫描世界的区块调色板，列出所有非 `minecraft` 的方块、物品命名空间。
- Build 新增 `modIds: string[]`：构建时从各个 jar 的 `fabric.mod.json` 读出。
- start 资产房间和 hop 时要求 `namespaces ⊆ modIds`，不满足就返回 `409 build_mismatch`，并在 message 里说明缺少哪些命名空间。
- 资源包 revision 等于 ZIP 的 sha256，照原提议。

### 3.4 错误码

新增 `conflict`，用于状态冲突（HTTP 409）。原提议中所有"409 invalid_request"都改用它。`invalid_request` 只用于 400。

---

## 4. 测试规则（"禁止 mock"的精确定义）

- **禁止**：用假实现替换依赖。包括 mock 对象、stub server、client-go fake clientset、`unittest.mock`，以及手写的假 chorusd。
- **允许**：
  - 纯函数测试，例如 slug、ID、LRU、状态机、lane 布局、SSE 解析。
  - 真实组件：临时 SQLite 文件、临时 git 仓库、承载**真实** chorusd handler 的 `httptest` 服务器、真实 k3s 命名空间 `chorus-it-<时间戳>`（用完删除）、真实 Minecraft 镜像。
  - 从真实系统**录制**下来的数据作为测试输入，比如从真实 chorusd 录下的 SSE 字节流。
- Go：普通单元测试用 `go test ./...`；集成测试用 build tag `integration`，在 encore 上运行。
- ruri：离线测试只覆盖纯函数和录制数据；联网测试读取环境变量 `CHORUS_IT_URL`，对 encore 上真实的 chorusd 运行。

---

## 5. 今晚的执行路线

### 5.1 环境自检（先做，结果写进 progress）

- 从 Mac SSH 到 encore（BatchMode）；encore 上 `sudo -n true`；Mac 上 `sudo -n true`。
- encore：`df -h /`、`free -g`、k3s 节点状态。
- 找到已安装 Ruri（0.10.5）自带的 CLI。**只用和已安装应用同版本的 CLI**；找不到，就把所有"真实进房"验收标为 `待用户`。
- seraph 是否在线：预期离线。所有依赖 seraph 的验收项都标为 `待 seraph`。

### 5.2 阶段顺序与依赖

`P1 → P2`，`P1 → P3 → P4`，`P2 + P3 → P5 → P6`。

**P2 卡住时，可以先做 P3、P4**——它们只依赖 P1 的 API。P7 今晚不做。

### 5.3 真实进房测试（不需要人在场）

1. 用已安装 Ruri 的同版本 CLI 新建**一个**实例：名称 `Chorus Test · fabric 26.3`，Fabric Loader 0.19.5。这是唯一允许在已安装 Ruri 里做的写操作；它的其他实例、账号和设置一概不动。
2. 把 manifest 中 `side` 为 `both`/`client` 的 jar 放进这个实例的 `mods/`。
3. 启动：`launch start <实例> --server <房间主机>:25565 --account <账号>`。
4. 确认进入成功：房间日志里出现 `<名字> joined the game`，并且 SLP 读到的在线人数为 1。
5. 用 `session quit` 正常退出游戏。
6. 全晚最多启动游戏 8 次。连续 2 次因为认证或网络失败，就停止这类测试，相关项标为 `待用户`。

### 5.4 各阶段在原计划基础上的调整

- **P1**：
  - 按 §2.1 落地 DNS。
  - 原计划写的是"手动建实例进房"，改为 §5.3 的自动化流程。
  - 先以前台方式运行 chorusd 完成验收；NixOS 服务化放到 P2。
- **P2**：
  - "放一个方块还在"用 RCON 自动验证：`setblock` → 停止房间 → 再次启动 → 执行 `execute if block … run say ok`，检查日志里出现 `ok`。
  - "进入后退出，10 分钟后 closed"用 §5.3 的流程完成。
  - 只有 encore 时，"seraph 在线时落在 seraph"和"节点失联"两项标为 `待 seraph`，但要确认 seraph 离线时房间落在 encore。
  - 不配置 seraph 加入集群。
- **P3**：
  - 实现照原计划。原计划的"mock 后端单元测试"改为 §4 的规则。
  - 新应用里没有登录账号，所以自动化只做到 `launch --dry-run`（验证启动计划正确）。真实进入标为 `待用户`，需要用户先在 Ruri Chorus 里登录一次。
- **P4**：照原计划；自动化范围同 P3。
- **P5**：
  1. 先扫描 plaza 世界的命名空间。只有 `minecraft` 和 chorus 自己的命名空间 → 直接继续。存在别的命名空间（例如 d2map 自带的运行时方块模组）→ 把那个 jar 作为 Fabric 依赖（`side: both`）由 chorusd 托管在 `/v1/deps` 下发，这样所有构建的客户端模组集合仍然一致；该 jar 不进 git，并写进 decisions。
  2. hop 测试不需要人打字：测试客户端在 `main-fabric` 里时，用 RCON 执行 `execute as <名字> run server plaza`；确认玩家进入资产房间后，再执行 `execute as <名字> run server room:main-fabric` 回来。
  3. plaza 没有资源包的话，资源包那一项记为 N/A。
- **P6**：
  - 代码照原计划实现。
  - 真实同步测试用你现有的 SSH 身份，写到 `tank:/srv/archive/chorus/it-<时间戳>/`。**只写不删**，留下的测试文件写进晨报。
  - chorusd 的生产 tank 身份等用户处理；在此之前生产配置里的 tank 同步保持关闭，热缓存池不淘汰任何文件。

---

## 6. 挂机（AFK）协议

### 6.1 不提问、不等待

- 遇到本文件和原计划都没覆盖的决定：选**最保守、可逆**的方案，写进 decisions，并在 progress 的"需要用户确认"一节里记一条，然后继续工作。
- 永远不要停下来等回复。

### 6.2 被阻塞时

- 把阻塞项标为以下之一：`待用户`（需要账号、登录、凭据或用户的选择）、`待 seraph`、`待 hank`。然后转去做其他不依赖它的工作。
- 同一个技术问题，尝试 **3 种不同方法或累计 90 分钟**仍未解决：把现象、已尝试的方法和下一步建议写进 progress，标为 `卡住`，转做其他工作。
- 所有剩余工作都被阻塞 → 进入 §6.6 收尾。

### 6.3 提交与合并

- **commit 前提**：该仓库的构建和测试全部通过。没通过只能 `git add`。
- **阶段合入 `dev` 的前提**：所有可以自动验证的验收项都通过，**并且**涉及房间行为的阶段（P1、P2、P5）的真实进房测试通过。
  - 真实进房因为 `待用户` 无法执行时：**不合入 dev**，下一个阶段的分支从当前阶段分支上切出（堆叠）。早上由用户验证后按顺序合入。
- 分支命名：`feat/p1-core`、`feat/p2-lifecycle`、`feat/p3-ruri-servers`、`feat/p4-ruri-git`、`feat/p5-assets-hop`、`feat/p6-tank`。

### 6.4 修改 encore 系统时（每次都要照做）

1. 先 `nix build`，再 `dry-activate`，检查改动只涉及 chorusd、dnsmasq、防火墙和目录规则。
2. **设置死人开关**：先记录当前系统路径 `OLD=$(readlink -f /run/current-system)`，然后执行 `sudo systemd-run --on-active=600 --unit=chorus-revert "$OLD/bin/switch-to-configuration" test`。
3. 用 `switch-to-configuration test`（也就是 `nixos-rebuild test`）激活新系统。
4. **从 Mac 新开一个 SSH 连接**，检查：k3s、mysql、postgresql、chorusd、dnsmasq 都是 active，旧 Minecraft 服务仍然是 masked。
5. 全部通过 → `sudo systemctl stop chorus-revert.timer`，再执行 `switch` 使其持久生效。
6. 任何一项失败 → 不要取消计时器，等它在 10 分钟内自动回滚，然后排查。

### 6.5 红线（任何情况下都不做）

- 不修改 `imdomestic/nix-config`、`HCHogan/chorus`；不修改 `d2-map-dev` 的主线。
- 不读取、不复制 `d2-assets/sensitive` 下的任何文件。发布工具要拒绝该目录下的任何路径。
- 不删除 tank 上的任何文件；不动 `/var/lib/minecraft`、旧服务的归档和它们的 masked 状态。
- 不 push 到 GitHub，不创建远端仓库；不 force push 任何 remote。
- 只删除 `chorus-it-*` 测试命名空间，以及已成功归档的 commit/asset 房间的 PVC。不删除其他命名空间、PV 或宿主目录。
- 不在任何输出里打印 token、密钥、Microsoft 凭据。
- encore 根分区可用空间低于 15 GiB 时，停止创建新快照和新构建，先写进晨报。

### 6.6 结束条件

满足以下任一条件就收尾：

- P1–P6 全部完成，或者剩下的只有 `待用户`/`待 seraph`/`待 hank`/`卡住` 项；
- encore 不可达超过 30 分钟；
- 本机时间到了 **09:30 JST**。

收尾步骤：

1. 停掉所有临时房间，删除所有 `chorus-it-*` 命名空间。
2. 保留 `main-fabric` 的世界（PVC）。
3. 确认 chorusd 处于运行状态，或者明确写明它为什么没有运行。
4. 最后一次更新晨报。

### 6.7 晨报格式（`docs/morning-report.md`，控制在一屏半以内）

1. **一句话结论**：做到了哪个阶段，现在能不能用。
2. **阶段表**：P1–P6，每项为 通过 / 部分（列出待办项）/ 未开始 / 卡住。
3. **需要你做的事**：按耗时从短到长排序，每条附可以直接执行的命令或操作步骤。§7 中已经解决的项不要列。
4. **我替你做的决定**：只列本文件没有规定、由你自己决定的事项。
5. **异常与风险**：失败、回滚、资源吃紧、留在 tank 上的测试文件路径。

最后一条回复（如果有人看）就是晨报第 1–3 节的内容。

---

## 7. 预计早上要交给用户的事（供你写晨报时参考，不要试图自己完成）

- DNS：在 Headscale 配置 split DNS（`chorus.internal → 100.64.0.43`），或者在每台 Mac 上加 `/etc/resolver/chorus.internal`；之后把 zone 切回 `chorus.internal`。
- 在 Ruri Chorus 里登录一次 Microsoft 账号，然后跑 P3/P4 的真实进入测试。
- 提供 hank 的 MC 用户名；把 hank 的 token 交给他（token 文件路径写在晨报里）。
- chorusd 访问 tank 的生产身份：用哪个账号、权限怎么限定。
- 让 seraph 上线，加入集群并做调度验收。
- 审阅 d2-map-dev 的 `feat/chorus-publish` 分支。
- 决定 chorus-testbed 和 ruri fork 的 GitHub 远端。
- 合并堆叠的阶段分支（如果有）。
