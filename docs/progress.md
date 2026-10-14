# 进度

挂机开始：2026-10-15 02:51 JST。SSH/encore sudo 通过；Mac sudo 需要密码；encore 磁盘可用 40 GiB、MemAvailable 约 18 GiB、k3s Ready；Ruri CLI 0.10.5 可用；临时 zone 解析通过；seraph 可访问但尚未加入集群。

## 当前阶段

P0、P1 通过。P3/P4 已完成构建、139 项测试及真实 prepare/dry-run。P2 开始世界保留、空闲回收与资源验收；部署候选已构建。P7 今晚不执行。

需求来源：`/Users/linwhite/Downloads/chorus-testbed-待确认文件-2026-10-14/chorus-testbed-plan.md`；本晚执行以 `directives/2026-10-15-decisions.md` 为准。

## P0 已核实事实

- 本地工作目录为 `/Users/linwhite/chorus-testbed`。P0 交付分支为 `feat/p0-environment`，验收通过后合入 `dev`。
- Mac 已通过 SSH 验证 encore、seraph、tank；今晚 seraph 可以访问，但尚未安装 k3s，相关集群验收为待 seraph。
- encore 已通过独立 Nix 配置启用 k3s `v1.35.7+k3s1`，节点 Ready；默认 local-path 为 WaitForFirstConsumer，ServiceLB 已启用。
- 六个旧 Minecraft 服务及其 socket 已持久禁用。完整归档经过本地独立恢复和 tank SHA-256 校验，切换前后 8,937 项 Minecraft 文件与 4 项凭据目录记录均一致；原文件保留。
- GitHub 的 `HCHogan/chorus` 当前仅有 `main` 分支，HEAD 为 `fc16eaac252bc8404e99a2368fecd768d31c8067`。
- chorus 配置为 MC `26.3`、Java `25`、Fabric Loader `0.19.5`、Fabric API `0.161.0+26.3`。Fabric API 版本属性名为 `fabric_version`。
- 已使用 Nix JDK 25 完成 `:fabric:assemble --rerun-tasks`，16 个任务执行通过；正式 jar 与 Fabric API 的大小、SHA-256、元数据均已核对，源码保持原样。
- 固定 itzg 镜像已经在 encore 实际启动 Fabric 26.3 与上述模组，健康检查、RCON 保存、transfer 配置、压缩恢复检查通过。
- Mac 当前开发者目录为 `/Applications/Xcode-26.6.0.app/Contents/Developer`，Xcode `26.6`、Swift `6.3.3`、macOS SDK `26.5`，首次启动准备检查通过。
- ruri 上游源码使用 `--build-system swiftbuild` 完成 debug 构建，中英文资源编译完整；28 项实际函数、文件、持久化及本地化测试全部通过。源码与依赖锁定文件保持原样，测试范围及命令见 `p0-ruri.md`。

## 当前验收状态

03:21 JST：控制器全部 Go 包编译及现有真实 Git、SQLite、签名、ID、SSE、SLP 测试通过；mc-router 已运行，kubeconfig 越权访问检查通过。encore 正在编译同一源码，Ruri Chorus debug 构建通过。

03:21 JST：已安装 Ruri 创建唯一测试实例 `E3EFAC4E-FF95-4BB6-AEDD-AD2D7CFC7DDA`，名称 `Chorus Test · fabric 26.3`，正在下载实际游戏资源。游戏启动计数 0/8。

03:46 JST：Fabric 构建 attempt 2 完成，16 项实际任务执行，耗时 29.7 秒；服务端与客户端模组摘要一致，房间 generation 3 通过 Pod+SLP 身份核验。日志 `.work/p1-build-attempt2.log`。

03:54 JST：Ruri Chorus 真实 API、SSE、模组安装及 dry-run 通过；plaza 世界发布、revision 幂等与持久任务集成通过。已安装 Ruri 的 SDL 初始化配置调整完成，游戏启动计数 3/8。

03:55 JST：P1 真实进房通过，服务端记录 linWhite joined the game，mc-router SLP online=1、三项身份全部匹配；客户端 session `A88EA06D-CF36-418D-8DB7-4B6F7802812C`。

| 阶段 | 必需证据 | 当前状态 |
| --- | --- | --- |
| P0 | 集群、存储、构建、镜像实际启动、ruri 构建和测试、环境报告 | 通过 |
| P1 | controller 实际创建房间与 SLP 核验、Mac 进入、缓存命中、Go 验证、DNS | 通过 |
| P2 | 10 分钟回收、世界保留、节点调度及失联、controller 重启接管 | 进行中；节点集群验收待 seraph |
| P3 | 服务器页面自动进入、模组替换、明确错误、测试验证 | 自动化通过；新应用登录与真实进入待用户 |
| P4 | 合并提交图、旧提交房间进入、失败日志 | 自动化进行中；真实进入待用户 |
| P5 | plaza 资产发布、游戏内往返 transfer、构建约束、资源包 | 等待 P2 与 P3 验收 |
| P6 | 断线后同步、远端校验后淘汰、快照恢复并启动 | 等待 P5 验收 |

## 使用的默认值

- 项目目录使用默认名称 `chorus-testbed`。
- 数据目录计划使用 `/srv/chorus`，当前尚未创建。
- 其余待定默认值尚未写入运行配置。

## 需要用户确认

- P1 使用独立验收数据目录与 systemd user 构建单元，P2 完整迁移到 `/srv/chorus` 并执行系统服务化；运行数据和世界保留。
- 客户端采用 EventSource 1.4.0 解析 SSE；独立钥匙串服务为 `dev.ruri.launcher.chorus.api`。
- 临时 zone 为 `100-64-0-43.sslip.io`，晨间配置 split DNS 后切回 `chorus.internal`。
- Minecraft 26.3 的测试实例使用直接 Java 启动，并通过 macOS 正常退出请求结束游戏；详情见 decisions。

## 用户已确定的配置

2026-10-14：当前使用 `HCHogan/chorus` 的 `main` 分支作为占位来源；目标仓库为 `imdomestic/chorus`，迁移完成后需要更换。项目根目录的 `REPOSITORY-MIGRATION.md` 记录此要求。

2026-10-14：用户要求在 encore 搭建 k3s；六个现有 Minecraft 服务归档校验通过后停用，保留原始文件。encore k3s 已运行；归档于 18:24:39 JST 完成 tank 校验，随后应用持久停用配置。归档路径为 `tank:/srv/archive/chorus/legacy/20261014T084821Z`。

## 工作目录与数据

临时文件、克隆、下载与日志存放在被 `.gitignore` 排除的 `.work/`。encore 的独立核对目录为 `/home/linwhite/chorus-testbed-p0/.work/`。Java、Swift、Python 与归档命令使用相应工作目录；Nix 构建使用 `/nix/var/nix/builds`，构建隔离环境内使用 `/build`；k3s 使用 `/var/cache/chorus-k3s`；metrics-server 使用 `/var/cache/metrics-server`；Minecraft 容器使用 `/data/.work/`。
