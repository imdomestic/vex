# 进度

挂机开始：2026-10-15 02:51 JST。SSH/encore sudo 通过；Mac sudo 需要密码；encore 磁盘可用 40 GiB、MemAvailable 约 18 GiB、k3s Ready；Ruri CLI 0.10.5 可用；临时 zone 解析通过；seraph 可访问但尚未加入集群。

## 当前阶段

第二轮 B 修复已部署，真实 main-fabric 进入、十分钟空闲停止和方块保存检查通过。C 已受保护切换至 nix-config；D/E 正在执行。A 指定房间 GUI 验收待用户，H 首次推送随之等待；F 配置与构建通过，tank sudo 授权待用户。G 合并与完整 pytest 通过。

18:58 JST：encore 运行系统和持久系统均为 `gaya9a3803rbyywjlkskks9lcjbanz96`，关键服务与十二个旧 Minecraft 单元检查通过。证据 `.work/round2-c-activation.log`。

18:56 JST：main-fabric generation 6 读取到停止前放置的绿宝石方块；自动停止副本数为零，PVC UID 保持一致。证据 `.work/round2-b-idle.log`、`.work/round2-b-persistence.log`。

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

04:00 JST：两个 ready 房间与一次 Gradle 构建重叠采样 14 次，MemAvailable 最低 10.80 GiB，最大并发构建为 1；encore 容量保持 2。日志 `.work/logs/p2-resources.jsonl`。

04:00 JST：玩家正常退出，游戏 exit=0；commit 房间空闲起点为 18:59:33 UTC。main-fabric 已通过 RCON 放置测试方块，等待停止与恢复验证。

04:13 JST：生产 chorusd、dnsmasq、k3s、mysql、postgresql 均运行；旧服务保持 masked。真实 SSE 断线后重新收到 sync，142 项客户端测试通过。

04:13 JST：真实停止世界包含 chorus-checkpoint.json，归档校验与恢复保留该文件；实际归档集成检查通过，生产更新正在执行保护协议。

04:19 JST：main-fabric generation 2 启动后，原钻石方块通过 RCON 验证；commit generation 4 使用 main-fabric-g1 快照启动，新世界同样通过 seed-ok 检查。

04:19 JST：旧提交房间与 Ruri Chorus prepare/dry-run 通过，实际模组已替换；main-fabric 空闲起点为 19:17:09 UTC，等待十分钟自动缩容。

04:28 JST：main-fabric 空闲十分钟后自动缩容为零并发布 generation 2 快照；stopped→finished→closed、恢复 testing 与 restart 接受 generation 3 均通过。

04:38 JST：生产 systemd 模板完成真实 Fabric 构建，耗时 30.60 秒、峰值内存 1.77 GiB；cgroup、全局构建锁、三个 jar 的 canonical/alias 字节及三次缓存复用均通过。P2 encore 验收完成。

04:43 JST：plaza 发布 revision 1，重复上传保持 revision、摘要和创建时间；源世界 62 项文件及目录均保持原样。main 构建 attempt 3 包含服务端 chorus-hop，generation 4 已通过就绪核验。

04:49 JST：生产更新通过全部保护检查，完整 Gradle 构建和两项 JUnit 通过。已启动游戏会话 `93485875-F577-4867-B560-FB732CF626C2`，累计启动次数 4/8。

04:54 JST：游戏内目的地列表、main→plaza→main 往返、跨构建拒绝提示通过，两端 SLP 均记录 online=1。正常退出后资产房间已 closed，main 已 stopped 并保留 PVC；日志 `.work/logs/p5-client.log`。

04:54 JST：plaza 编辑候选 promote 为 revision 2，重复 promote、下载摘要和源世界保留检查通过；Go build/vet/test 全部通过，日志 `.work/logs/p5-go-validation.log`。

05:02 JST：tank-only 快照自动恢复后启动真实 Minecraft，通过 Pod、路由 SLP 与区块/方块日志验证；真实资源包 CLI 上传、幂等和下载校验通过，测试命名空间已删除。日志 `.work/p6-seed.log`。

05:11 JST：真实 Tailscale 中断 65 秒后恢复，SQLite 重开后的第二次同步完成续传、远端校验、淘汰与恢复；Go build/vet/test 全部通过。日志 `.work/p6-tailscale-test.log`、`.work/p6-final-checks.log`。

05:12 JST：收尾检查通过，临时房间 closed、main-fabric stopped 且 PVC 保留；测试命名空间与游戏进程均已清理，生产服务正常，根盘可用 32.22 GiB。日志 `.work/final-health.log`；审阅文件交付到本机 `Downloads/Chorus-Testbed-审阅-2026-10-15/`。

| 阶段 | 必需证据 | 当前状态 |
| --- | --- | --- |
| P0 | 集群、存储、构建、镜像实际启动、ruri 构建和测试、环境报告 | 通过 |
| P1 | controller 实际创建房间与 SLP 核验、Mac 进入、缓存命中、Go 验证、DNS | 通过 |
| P2 | 10 分钟回收、世界保留、节点调度及失联、controller 重启接管 | encore 全部通过；待 seraph 的调度与失联验收 |
| P3 | 服务器页面自动进入、模组替换、明确错误、测试验证 | 自动化通过；新应用登录与真实进入待用户 |
| P4 | 合并提交图、旧提交房间进入、失败日志 | 自动化通过；真实进入待用户 |
| P5 | plaza 资产发布、游戏内往返 transfer、构建约束、资源包 | 通过；资源包 N/A |
| P6 | 断线后同步、远端校验后淘汰、快照恢复并启动 | 通过；生产同步待用户提供 SSH 身份 |

## 使用的默认值

- 项目目录使用默认名称 `chorus-testbed`。
- 生产数据目录为 `/srv/chorus`，属主 chorusd；用户凭据位于 `/var/lib/chorusd/secrets/`。
- 生产 tank 同步关闭，热缓存保留全部文件；当前 Minecraft zone 为 `100-64-0-43.sslip.io`。

## 需要用户确认

- 待用户：独立 Ruri Chorus 登录与 P3/P4 真实进入、split DNS、tank 生产 SSH 身份、两个 GitHub 远端、d2 发布分支审阅。
- 待 hank：Minecraft 用户名与 token 安全交付；待 seraph：加入 k3s 后的调度及失联验收。
- 已实施的自主选择供复核：固定 systemd 构建模板、EventSource 1.4.0 与专属钥匙串、SDL 客户端启动和正常退出方式、LAN 控制下的 Tailscale 故障测试。详情见 `decisions.md`，具体操作见晨报。

## 用户已确定的配置

2026-10-14：当前使用 `HCHogan/chorus` 的 `main` 分支作为占位来源；目标仓库为 `imdomestic/chorus`，迁移完成后需要更换。项目根目录的 `REPOSITORY-MIGRATION.md` 记录此要求。

2026-10-14：用户要求在 encore 搭建 k3s；六个现有 Minecraft 服务归档校验通过后停用，保留原始文件。encore k3s 已运行；归档于 18:24:39 JST 完成 tank 校验，随后应用持久停用配置。归档路径为 `tank:/srv/archive/chorus/legacy/20261014T084821Z`。

## 工作目录与数据

临时文件、克隆、下载与日志存放在被 `.gitignore` 排除的 `.work/`。encore 的独立核对目录为 `/home/linwhite/chorus-testbed-p0/.work/`。Java、Swift、Python 与归档命令使用相应工作目录；Nix 构建使用 `/nix/var/nix/builds`，构建隔离环境内使用 `/build`；k3s 使用 `/var/cache/chorus-k3s`；metrics-server 使用 `/var/cache/metrics-server`；Minecraft 容器使用 `/data/.work/`。
