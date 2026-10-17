# 执行指令补充：vex P1 范围调整

- 日期：2026-10-17
- 补充对象：`docs/directives/2026-10-17-vex-p0-p1.md`
- 设计依据：已更新到 v0.2 的 `docs/design/vex.md`

本补充与原指令冲突的地方，以本补充为准。P0 不受影响。

如果 P1 已经开工：已完成的部分按本补充重构，然后再做 P1 验收；尚未开始的部分直接按本补充执行。

## 背景

vex 将作为开源产品发布。开源化评审得出的结论是：要去掉 vex 对我们自己基础设施的依赖。

- 大多数模组作者只有一台机器，所以单机 Docker 是首选的运行后端，k8s 排第二。
- 构建方式以"CI 推送产物"为主。

这两项都安排在 P2 之后。但接口边界要在 P1 重构时一并划好，否则以后返工的代价更高。

## 新增或修改的 P1 项

### A1 运行后端接口

- 新建 `internal/runtime` 包，定义 `Backend` 接口，包含以下操作：
  - EnsureProject、EnsureRoom、ObserveRoom；
  - StopRoom（先保存再停止，带超时）；
  - ArchiveWorld（输出 tar.gz 流或文件）；
  - DeleteRoom（可选择保留世界）；
  - Logs、RCON、Capacity。
- 现有 kube 代码整体迁到 `internal/runtime/kubernetes`。
- 原指令 1C 中的 namespace、ResourceQuota、NetworkPolicy、StatefulSet、PVC、initContainer、归档 Job、节点池，都属于这个后端的内部实现。
- 调和逻辑只依赖接口。RoomSpec 中不再出现 k8s 类型。世界卷用一个不透明的 ID 表示，记录在 runtime 中。
- 设计接口时以 Docker 后端（P5）为参照，确认它能这样实现：
  - 世界和模组目录放在 vexd 数据目录下，以 bind mount 方式挂载；
  - 模组由 vexd 直接写入；
  - 归档由 vexd 直接打包目录。
  
  如果某个接口在 Docker 下没法自然实现，在报告中说明。
- 配置项：
  - `runtime.backend`，P1 只接受 `kubernetes`；
  - `[runtime.kubernetes]` 下放 kubeconfig、storage_class 和 `nodes.*`，原指令里顶层的 `nodes.*` 移到这里。
- 路由抽象为 `router.Registrar`，P1 只实现 k8s 注解方式。原指令中关于 mc-router 跨 namespace 发现的实测照常进行。

### A2 存储后端接口

- 新建 `archive.Store` 接口，提供 Put、Verify、Get、Stat 四个操作。key 用相对路径，例如 `projects/<p>/snapshots/…`。
- P1 实现两种后端：
  - `local`：一个本地目录；
  - `rsync-ssh`：即现有的 tank 实现。
- 配置分为 `[archive] backend` 和 `[archive.rsync_ssh]`、`[archive.local]` 三节。
- 传输队列、退避、校验逻辑都与具体后端无关。

### A3 认证接口

- 新建 `auth.Provider` 接口，输出身份 `provider:subject`。P1 只实现 `static`，即 token_sha256。
- 用户记录增加 `identities` 列表。DB 和模型中统一用用户 ID，不要用 token 或用户名充当身份。
- OAuth/OIDC 和会话 token 放到 P3。

### A4 单级房间地址（覆盖原指令中的 `<room>.<project>.vex.internal`）

- 地址改为 `<room>--<project>.<zone>`。部署者只需要配一条 `*.<zone>` 泛解析。
- ID 规则增加两条：不允许包含 `--`，不允许以 `-` 结尾。
- 我们的部署：dnsmasq 和 Headscale 的 split DNS 都配置 `vex.internal`。
- 原 P1 验收第 6 项改为：`main-fabric--chorus.vex.internal` 能解析并能连上。

### A5 EULA

- 新增 `[minecraft] accept_eula`，布尔值，默认 false。
- 值为 false 时，开房请求返回 409 `eula_not_accepted`，`vexd config check` 给出提示。
- 只有值为 true 时，才向服务端镜像传 `EULA=TRUE`。我们的 settings 设为 true。

### A6 通用的世界和资源包发布

`vex` CLI 提供以下命令，它们只调用 v2 API：

- `vex world publish <目录|tar.gz> -p <project> --world <id> [--title …] [--pack <id>@<rev>]`
- `vex world list`
- `vex world show`
- `vex world promote <snapshot>`
- `vex world archive`
- `vex world restore`
- `vex pack publish <zip> -p <project> --pack <id>`

d2map 改为调用 vex CLI。这一项属于 d2-map-dev，不在本指令范围内，在报告中列为后续工作即可。

### A7 文件哈希

Build 文件、世界文件和资源包入库时，除了 sha256，还要计算 sha1 和 sha512，写入记录和 API 响应。P2 生成 mrpack 时会用到。

### A8 构建来源字段

- Build 记录增加 `source`，P1 的值固定为 `{kind: "host"}`。
- 构建配置改为 `projects.<p>.build.source`，P1 只接受 `host`，并且仅限 `trust = "trusted"` 的项目。

### A9 ruri

原指令的 1F 作废，改为执行 ruri 仓库中的 `2026-10-17-ruri-vex-experimental.md`（R1）。

## 新增的 P1 验收项

10. **后端边界检查**：以下两条命令都没有输出。
    - `rg -l 'k8s.io/' controller --glob '!**/internal/runtime/kubernetes/**' --glob '!**/cmd/**'`
    - 调和逻辑（`internal/rooms`）中不出现 StatefulSet、PVC、Namespace 等 k8s 词汇。
11. **local 存储后端**：用真实的临时目录做一次完整流程：快照归档、校验、本地淘汰、恢复。
12. **EULA**：先把 `accept_eula` 设为 false，开房返回 `eula_not_accepted`；改回 true 后能正常开房。
13. **世界发布**：用 `vex world publish` 重新发布 plaza 的一个新 revision，世界房间能用这个 revision 启动。
14. **文件哈希**：Build 文件的 sha1、sha512 与本地对 jar 重新计算的结果一致。
