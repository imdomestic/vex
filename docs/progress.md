# 进度

## 当前阶段

P0 环境核对已通过验收。P1 等待跨组件接口提议确认；P1–P6 尚未开始，P7 保持可选。

需求来源：`/Users/linwhite/Downloads/chorus-testbed-plan.md`。

## P0 已核实事实

- 本地工作目录为 `/Users/linwhite/chorus-testbed`。P0 交付分支为 `feat/p0-environment`，验收通过后合入 `dev`。
- Mac 已通过 SSH 验证 encore、seraph、tank；当前 encore 与 tank 可访问，seraph 已离线。
- encore 已通过独立 Nix 配置启用 k3s `v1.35.7+k3s1`，节点 Ready；默认 local-path 为 WaitForFirstConsumer，ServiceLB 已启用。seraph 当前离线，agent 尚未部署。
- 六个旧 Minecraft 服务及其 socket 已持久禁用。完整归档经过本地独立恢复和 tank SHA-256 校验，切换前后 8,937 项 Minecraft 文件与 4 项凭据目录记录均一致；原文件保留。
- GitHub 的 `HCHogan/chorus` 当前仅有 `main` 分支，HEAD 为 `fc16eaac252bc8404e99a2368fecd768d31c8067`。
- chorus 配置为 MC `26.3`、Java `25`、Fabric Loader `0.19.5`、Fabric API `0.161.0+26.3`。Fabric API 版本属性名为 `fabric_version`。
- 已使用 Nix JDK 25 完成 `:fabric:assemble --rerun-tasks`，16 个任务执行通过；正式 jar 与 Fabric API 的大小、SHA-256、元数据均已核对，源码保持原样。
- 固定 itzg 镜像已经在 encore 实际启动 Fabric 26.3 与上述模组，健康检查、RCON 保存、transfer 配置、压缩恢复检查通过。
- Mac 当前开发者目录为 `/Applications/Xcode-26.6.0.app/Contents/Developer`，Xcode `26.6`、Swift `6.3.3`、macOS SDK `26.5`，首次启动准备检查通过。
- ruri 上游源码使用 `--build-system swiftbuild` 完成 debug 构建，中英文资源编译完整；28 项实际函数、文件、持久化及本地化测试全部通过。源码与依赖锁定文件保持原样，测试范围及命令见 `p0-ruri.md`。

## 当前验收状态

| 阶段 | 必需证据 | 当前状态 |
| --- | --- | --- |
| P0 | 集群、存储、构建、镜像实际启动、ruri 构建和测试、环境报告 | 通过；seraph 按“若在线”条件记录为离线；ruri 执行经源码检查的 28 项测试 |
| P1 | controller 实际创建房间与 ACK、Mac 进入、缓存命中、Go 验证、DNS | 等待跨组件接口提议第 1–5 项确认 |
| P2 | 10 分钟回收、世界保留、节点调度及失联、controller 重启接管 | 等待 P1 验收 |
| P3 | 服务器页面自动进入、模组替换、明确错误、测试验证 | 等待 P2 验收 |
| P4 | 合并提交图、旧提交房间进入、失败日志 | 等待 P3 验收 |
| P5 | tower 资产发布、游戏内往返 transfer、构建约束、资源包 | 等待 P4 验收 |
| P6 | 断线后同步、远端校验后淘汰、快照恢复并启动 | 等待 P5 验收 |

## 使用的默认值

- 项目目录使用默认名称 `chorus-testbed`。
- 数据目录计划使用 `/srv/chorus`，当前尚未创建。
- 其余待定默认值尚未写入运行配置。

## 需要用户决定

- 规划 §0.6 涉及的十项跨组件接口细节及对应方案记录于 [接口提议](interface-proposals.md)。其中第 1–5 项需要在 P1 实现前确认；全部提议尚未实现。

## 用户已确定的配置

2026-10-14：当前使用 `HCHogan/chorus` 的 `main` 分支作为占位来源；目标仓库为 `imdomestic/chorus`，迁移完成后需要更换。项目根目录的 `REPOSITORY-MIGRATION.md` 记录此要求。

2026-10-14：用户要求在 encore 搭建 k3s；六个现有 Minecraft 服务归档校验通过后停用，保留原始文件。encore k3s 已运行；归档于 18:24:39 JST 完成 tank 校验，随后应用持久停用配置。归档路径为 `tank:/srv/archive/chorus/legacy/20261014T084821Z`。

## 工作目录与数据

临时文件、克隆、下载与日志存放在被 `.gitignore` 排除的 `.work/`。encore 的独立核对目录为 `/home/linwhite/chorus-testbed-p0/.work/`。Java、Swift、Python 与归档命令使用相应工作目录；Nix 构建使用 `/nix/var/nix/builds`，构建隔离环境内使用 `/build`；k3s 使用 `/var/cache/chorus-k3s`；metrics-server 使用 `/var/cache/metrics-server`；Minecraft 容器使用 `/data/.work/`。
