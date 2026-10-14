# 实现决策

## 2026-10-15：执行依据与接口

按 `directives/2026-10-15-decisions.md` 执行，P0 已通过；接口统一以 `api.md` 为准。
接口变化先修改契约并记录决定，阶段验证使用测试结果和简短日志。

## 2026-10-15：来源、构建和镜像

匿名 HTTPS 只读获取 `HCHogan/chorus` 的 `main`，登记 `main-fabric`，只构建 Fabric `:fabric:assemble`；NeoForge 关闭。
属性和依赖采用 P0 实测值，两个容器镜像使用 `p0-image.md` 的固定 digest，迁移目标仓库留待以后执行。

## 2026-10-15：Gradle 进程组限制

构建队列串行执行，使用独立 systemd 服务承载每次 Gradle 构建；服务设置 MemoryHigh=5G、MemoryMax=6G、CPUQuota=200%。
Java ActiveProcessorCount=2、Gradle workers=2、堆 1536m，限制覆盖所有子进程；控制器恢复时接管已有任务，防止并行重复构建。

## 2026-10-15：部署地址和数据

chorusd 监听 `100.64.0.43:8790`、`10.1.2.137:8790`，公开地址为 `http://encore.inner.imdomestic.com:8790`，内部地址为 `http://10.1.2.137:8790`。
数据位于 `/srv/chorus`，属主 chorusd；Nix 包与 module 通过现有 extendModules 组合，每次系统切换执行指令 §6.4 的定时恢复和新 SSH 检查。

## 2026-10-15：代码同步和凭据

encore 裸仓库 `~/git/chorus-testbed.git` 作为 `encore` remote；今晚仅同步到 encore。
用户 token 和签名密钥在 encore 生成，存入 `/var/lib/chorusd/secrets/`，root:chorusd、0640；module 接收文件路径，Mac token 存钥匙串，凭据内容不进入任何输出。

## 2026-10-15：DNS

dnsmasq 仅为 `chorus.internal` 应答，监听 tailnet 与 LAN 地址，启用 no-resolv、bind-dynamic；仅在 tailscale0、wlo1 开放要求的 DNS、HTTP 和 Minecraft 端口。
Mac 可免密码 sudo 时配置 resolver，否则验证并使用临时 zone `100-64-0-43.sslip.io`，晨间切回；reconciler 随配置同步已有 Service 注解。

## 2026-10-15：白名单、容量和空闲时间

通过已安装 Ruri 的同版本 CLI 只读取得用户名；hank 白名单保留待填写的空数组并加注释。
容量 encore=2、seraph=4；两个房间与一次构建并行时 MemAvailable 低于 2 GiB，则将 encore 调为 1；所有房间空闲 10 分钟、租约 3 分钟。

## 2026-10-15：独立 Ruri Chorus

本地仓库 `/Users/linwhite/ruri-chorus` 使用 upstream 和 dev；应用名 `Ruri Chorus`，bundle id `dev.ruri.launcher.chorus`，支持目录为 `~/Library/Application Support/Ruri Chorus`，禁用 Sparkle。
构建统一使用 swiftbuild；已安装 Ruri 只允许指令 §5.3 的一个测试实例，独立应用的真实进入等待用户登录，自动化验证启动计划。

## 2026-10-15：资产发布

d2-map-dev 的 `feat/chorus-publish` 新增 `d2map chorus publish`，保留现有 `worlds.publish(cfg)`；该分支留给用户审阅。
首个资产 plaza 使用指定 sample/world；扫描非 minecraft 命名空间，必要运行时 jar 由依赖接口向所有 Fabric 构建下发；资源包按敏感资料处理。

## 2026-10-15：归档身份

生产归档根目录使用 `tank:/srv/archive/chorus/` 下的 snapshots、assets、packs；等待用户提供生产 SSH 身份期间，生产同步关闭、热缓存文件全部保留。
P6 使用现有身份向独立 `it-<时间戳>` 路径进行真实同步验证，保留全部远端测试文件并记录路径。

## 2026-10-15：Build、generation 和状态冲突

采纳构建列表、Build 阶段字段与重建、generation、新周期和固定种子的契约；Build 增加从 jar 元数据读取的 modIds。
HTTP 409 的状态冲突统一使用 conflict，invalid_request 仅用于 HTTP 400，避免客户端混淆输入错误与运行状态。

## 2026-10-15：就绪与 SSE

ready 以 Pod Ready 和经 mc-router 的 SLP 三项身份匹配为依据；重复 start 同步进行三秒 SLP 核验，失败转 starting/re-verify。
每次 SSE 连接首先 sync，随后发送实时事件和十五秒心跳；内存事件序号仅用于调试。ruri 匹配 id/generation/buildId 后按 ready 电平启动一次，等待时每 120 秒续租。

## 2026-10-15：种子选择与恢复

branch 种子依次取最新已发布快照、源房间 seedResolved、empty；源房间必须存在，受理 start 时固定来源并检查 Minecraft 版本。
快照仅在 tank 时进入 queued/restoring seed，自动恢复后继续启动；恢复失败记 archive_failed，snapshotId 仅用于展示。

## 2026-10-15：发布、hop 和恢复契约

资产 ID 使用裁决规定的正则，revision 为每个资产递增的整数字符串，相同最新世界 SHA-256 返回已有 revision；资源包 revision 使用 ZIP 的 SHA-256。
资产 namespaces 必须包含于 Build.modIds；hop 返回 room 和 lease。采纳下载、Snapshot、异步恢复及资产恢复契约，并与种子自动恢复共用实现。

## 2026-10-15：真实组件测试

允许纯函数、临时 SQLite、临时 git、真实 handler 的 httptest 和实际 k3s/Minecraft；禁止替换依赖的 mock、stub、fake clientset。
Go 集成测试使用 integration 标签；ruri 离线测试使用纯函数和真实录制数据，联网测试通过 CHORUS_IT_URL 连接实际 chorusd。

## 2026-10-15：前台验收与服务化

P1 在 `/home/linwhite/chorus-testbed/.work/p1-data` 以前台方式运行，构建使用相同限制的 systemd user 单元；P2 正常停止进程后完整复制数据到 `/srv/chorus`，设置 chorusd 属主。
dnsmasq 与正式 chorusd 服务在 P2 一并按系统切换协议激活，使每次激活都能检查要求的全部服务；P1 使用已经验证的临时 zone。

构建全局锁使用 `/srv/chorus-build.lock`（root:root、0444），进程通过只读文件描述符取得排他 flock；P1 与 P2 共用同一文件，避免不同数据目录同时构建。

P1 的 Mac HTTP 验收通过本地 SSH 转发 `127.0.0.1:18790` 访问真实 chorusd；Minecraft 使用已开放的 mc-router 25565。P2 按系统切换协议开放正式 HTTP 端口后，客户端恢复公共 URL。

encore 的 chorusd UID/GID 固定为已确认未使用的 980，迁移时可在用户由 Nix 激活创建前准备正确属主，首次服务启动直接读取完整数据。

## 2026-10-15：客户端依赖与身份

Ruri Chorus 使用 EventSource 1.4.0 解析 SSE，保留现有安装器和下载缓存；API token 存入 `dev.ruri.launcher.chorus.api`，账户键为去除尾斜线的后端 URL。
无账号的 dry-run 验证真实安装内容、Java 和启动参数，登录凭据在实际启动时获取；不生成替代账号。

## 2026-10-15：接口响应形状

Health 使用 `{status:"ok"}`；Meta.limits 返回构建并发、缓存数量和各节点容量；Node 返回就绪、可调度状态与房间列表。
Git refresh 成功返回 204；房间登记首次 201、复用 200；具体响应对象统一列在 api.md，便于各客户端使用同一模型。

## 2026-10-15：镜像初始化与构建缓存

init container 通过 GET 下载服务端 manifest 文件，并校验每个文件的 SHA-256 和大小，再交给镜像的 MODS 本地目录导入功能。
停止时若归档 Job 确认尚未生成 level.dat，以独立终止状态结束该周期，保留 PVC；完整世界仍须成功归档才能删除临时房间 PVC。

Gradle 使用 P0 已经真实生成的 Gradle/NeoForm 缓存；Fabric 任务继续实际执行，单进程组资源限制保持原值。冷缓存反编译触发 MemoryHigh 压力，缓存准备属于部署依赖。

## 2026-10-15：Minecraft 26.3 客户端自动验收

唯一测试实例关闭 macOS 原生宿主、垂直同步及首次辅助功能引导，使 SDL 客户端完成初始化。配置范围限定该测试实例。
已安装 Ruri 0.10.5 的 CLI 未识别 SDL 正常退出能力；通过同一 macOS NSRunningApplication 正常退出请求验证游戏输出 Stopping! 并以 0 退出，自动验收采用该系统请求。独立 Ruri Chorus 增加 SDL 能力识别。

## 2026-10-15：tank 集成验收

测试沿用 encore 的现有 SSH 身份，并使用 Mac 已信任的 tank 主机公钥进行严格校验；生产同步继续关闭。
断点续传验收只中断测试使用的 SSH 传输连接，随后重试并核对远端摘要与大小；测试文件永久保留在独立 it 时间戳目录。

## 2026-10-14：Chorus 来源

用户确定目标仓库为 `imdomestic/chorus`，迁移完成前使用 `HCHogan/chorus` 的 `main` 分支作为占位来源。后续更换要求见根目录 `REPOSITORY-MIGRATION.md`。

## 2026-10-14：Fabric 产物构建任务

房间所需的 Fabric jar 使用 `:fabric:assemble` 生成。该任务执行 Minecraft 编译依赖准备、common 与 Fabric 编译、资源处理及 jar 打包。

当前上游 `:fabric:build` 同时启动 GameTest，其中包含 `makeMockServerPlayerInLevel()`。用户禁止使用 mock，因此项目不采用这些测试作为验收证据。实际服务端启动、模组加载与客户端进入仍按规划在真实运行环境中验证。

任务依赖已通过 `:fabric:assemble --dry-run` 检查，包含完整产物构建步骤。生产配置的 `gradle_task` 使用这一明确任务。

## 2026-10-14：encore 构建资源

构建使用 Nix OpenJDK 25，并设置 `JAVA_TOOL_OPTIONS` 中的 `-XX:ActiveProcessorCount=2`、Gradle `--max-workers=2` 和 Gradle 最大堆内存 `1536m`。临时验收服务对整个构建进程组设置 `MemoryHigh=5G`、`MemoryMax=6G`、`CPUQuota=200%`。

实际 NeoFormRuntime `2.0.31` 依据可见处理器数量计算 Vineflower 最大堆内存，至少为 4096 MiB；Vineflower `1.12.0` 同时依据该数量选择默认并行线程。此设置控制构建资源，所有编译步骤保持启用。资源峰值以 systemd cgroup 统计为准。

## 2026-10-14：环境搭建范围

用户已要求在 encore 搭建 k3s，并允许在构建阶段归档现有 Minecraft 服务的数据。六个旧服务在归档恢复校验通过后停用，原始文件保持保留。归档包含世界、完整配置、数据库和恢复所需的 Nix 运行依赖。

`imdomestic/nix-config` 保持只读。k3s 配置在本项目中单独提供，系统切换前核验原有配置与服务影响。

## 2026-10-14：ruri 的本机构建方式

Xcode 26.6 下，ruri 的 debug 构建、测试和产物目录查询统一显式传入 `--build-system swiftbuild`，构建目录使用 `.work/ruri-swiftbuild`。该方式负责将 String Catalog 编译为运行时需要的 `.lproj`、`.strings` 和 `.stringsdict`；本机 SwiftPM 默认 native 模式的规则仅复制 `.xcstrings`。

本机帮助标注 swiftbuild 为 preview。当前上游 SHA 已通过真实 debug 构建和 28 项测试，包括全部英文消息和复数规则检查。源码、测试断言和依赖锁定文件保持原样。测试范围遵循用户禁止 mock 的要求，详细证据见 `p0-ruri.md`。
