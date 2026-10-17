# P0 环境报告

核对日期：2026-10-14，Asia/Tokyo。需求来源为 `/Users/linwhite/Downloads/chorus-testbed-plan.md`。

## 验收记录

| 检查项目 | 当前证据 | 验收状态 |
| --- | --- | --- |
| encore / seraph 节点、k3s 版本 | encore Ready，v1.35.7+k3s1；seraph 当前离线、agent 尚未部署 | encore 已验证；seraph 待上线后接入 |
| local-path、WaitForFirstConsumer、ServiceLB | local-path 为默认；实际 PVC 等待消费者后绑定；ServiceLB 已启用 | 已验证 |
| 系统切换后运行状态 | CoreDNS、local-path、metrics-server 全部可用；metrics API 返回真实节点数据，证书和临时目录为 `/var/cache/metrics-server` | 通过 |
| encore 数据磁盘 | 归档和系统切换完成后，`/srv` 所在根磁盘可用 `42026319872` 字节，约 39.1 GiB | 已核对；数据目录尚未创建 |
| tank 可达与权限 | Mac 和 encore 均能通过严格校验的 SSH 登录；旧服务专用归档目录为 root 所有、0700 | 旧服务归档远端 SHA-256 全部通过；chorusd 服务身份尚未配置 |
| 旧 Minecraft 服务归档与停用 | 本地独立恢复、tank 校验、原文件前后比对通过；六个服务及六个 socket 均 masked、inactive | 通过，原文件保留 |
| chorus 远端配置与依赖 | `main` 的 `fc16eaac252bc8404e99a2368fecd768d31c8067` | 已核对 |
| Fabric 实际构建 | `:fabric:assemble --rerun-tasks` 退出 0；正式 jar 与 Fabric API 已核对 | 构建通过，实际加载通过 |
| Minecraft 镜像 digest 与工具 | Registry 内容校验、固定镜像层与实际容器命令检查 | 通过 |
| Fabric 26.3 容器实际启动 | Ready、0 次重启；mc-health、RCON 保存、accepts-transfers、tar/gzip/curl 实际验证 | 通过 |
| d2-map-dev 世界与命令结构 | 真实世界 NBT、当前源码与目录检查 | 已核对 |
| ruri debug 构建 | Xcode 26.6、Swift 6.3.3、SDK 26.5；上游源码使用 swiftbuild 构建退出 0，构建耗时 56.01 秒 | 通过 |
| ruri 测试 | 测试宿主资源编译和签名通过；28 项实际函数、文件、持久化及本地化测试全部通过 | 通过；全量测试因包含 mock 未执行 |
| 临时命名空间清理 | chorus-p0、临时 PV 和对应宿主存储目录均已删除 | 通过 |

P0 已通过验收。seraph 按规划的“若在线”条件记录为离线，agent 接入仍待该节点上线。P1 需要先确认跨组件接口提议，后续阶段保持待执行状态。

## Chorus 配置实际值

当前占位来源为 `HCHogan/chorus`，仅使用 `main`。目标来源为 `imdomestic/chorus`，更换要求记录在根目录 [REPOSITORY-MIGRATION.md](../REPOSITORY-MIGRATION.md)。

| 配置项 | 当前源码与环境实际值 |
| --- | --- |
| `git.remote` | `https://github.com/HCHogan/chorus.git` 可匿名读取；生产只读凭据尚未配置 |
| `git.default_branch` | `main`，用户已确认 |
| `minecraft.version` | `26.3`，属性 `minecraft_version` |
| `minecraft.java` | `25`，属性 `java_version` |
| `loaders.fabric.gradle_task` | `:fabric:assemble`，依据真实任务依赖确定，见 `decisions.md` |
| `loaders.fabric.artifact_glob` | `fabric/build/libs/*.jar` |
| `loaders.fabric.artifact_exclude` | `*-sources.jar`、`*-dev*.jar`、`*-javadoc.jar`；实测排除后恰好一个正式 jar |
| `loaders.fabric.loader_version_property` | `fabric_loader_version`，当前值 `0.19.5` |
| Fabric API 属性 | `fabric_version`，当前值 `0.161.0+26.3` |
| Fabric API URL 模板 | `https://maven.fabricmc.net/net/fabricmc/fabric-api/fabric-api/{fabric_version}/fabric-api-{fabric_version}.jar` |
| Fabric API 安装范围 | `both` |
| chorus Fabric 运行时模组依赖 | Fabric API；Fabric Loader、Minecraft、Java 由运行环境提供 |
| Gradle | wrapper 固定 `9.7.1` |
| Fabric Loom | `1.18.1` |
| ModDevGradle | `2.0.147` |
| NeoFormRuntime | 实际依赖解析为 `2.0.31` |
| Vineflower | 实际依赖解析为 `1.12.0` |
| 构建 JDK | `/nix/store/dasnfgbvb4nff68czbr0461hl924nbh7-openjdk-25.0.4.1+1` |
| Java 发行版本 | Nix OpenJDK `25.0.4.1+1` |

multi-loader 目录为 `common/`、`fabric/`、`neoforge/`。`fabric/src/main/resources/fabric.mod.json` 声明的模组运行时依赖为 `fabric-api`；`another-mod` 位于可选的 `suggests`，没有列为必需依赖。`common/` 通过 NeoForm 生成编译用 Minecraft 产物，因此 Fabric 构建也会执行该步骤。

## 服务地址与容量条件

| 配置项目 | 当前值或条件 |
| --- | --- |
| `server.listen` | 规划地址可使用 `100.64.0.43:8790`、`10.1.2.137:8790` |
| `server.public_base_url` | 当前实际 DNS 对应 `http://encore.inner.imdomestic.com:8790` |
| `server.internal_base_url` | `http://10.1.2.137:8790` |
| `server.data_dir` | 默认 `/srv/chorus`；目录当前不存在 |
| `minecraft.zone` | 默认 `chorus.internal`；分离 DNS 尚未配置 |
| Minecraft 入口 | 六个旧服务及其 socket 已持久禁用，原文件校验一致；`25565` 当前没有监听 |
| `minecraft.whitelist` | 既有白名单为空，Linwhite 和 hank 的玩家名尚未确认 |
| `rooms.namespace` | 默认 `chorus-rooms`；尚未创建 |
| `rooms.max_running` | 默认 encore=2、seraph=4 尚未通过当前常驻负载下的容量验证 |
| secret 管理 | 当前 NixOS 配置使用 sops-nix；Chorus secret 文件尚未配置 |
| `archive.tank_target` | 默认路径 `/srv/archive/chorus` 已创建，旧服务归档位于其 `legacy/` 子目录；chorusd 服务账号待配置 |

encore 在旧服务停止且恢复检查完成后，可用内存约 18.0 GiB。临时构建在独立 systemd 用户服务中限制整个进程组内存为 6 GiB、CPU 为 200%，并记录 cgroup 峰值。房间并发容量仍需按规划进行实际验证。

## 详细证据

- [基础设施与地图工程](p0-infrastructure.md)：节点访问、存储、端口、secret 管理、真实 MC 26.3 世界目录和 NBT。
- [旧服务归档与恢复](legacy-minecraft-archive.md)：世界、数据库、Nix 运行依赖的真实恢复和 tank 校验，停用后的原文件核验。
- [k3s 系统配置](../../bootstrap/README.md)：固定 Nix 输入、当前运行配置与系统切换后的检查。
- [Chorus Fabric 构建](p0-build.md)：真实命令、产物 SHA-256、耗时、进程组内存峰值与运行依赖。
- [镜像](p0-image.md)：固定 digest、实际启动、健康与 RCON 检查、`ACCEPTS_TRANSFERS`、mc-router 配置及临时资源清理。
- [Mac 与 ruri](p0-ruri.md)：Xcode 与 SDK 版本、上游 SHA、String Catalog 构建方式、28 项测试结果与完整命令。
- [进度](progress.md)：阶段状态、待定默认值与需要用户决定的环境条件。

## 数据范围

本次没有读取或复制原始 Bungie 白模和贴图。chorus、ruri 仅在独立临时克隆中读取及构建。encore 的既有 chorus、d2-map-dev、nix-config 保持原状。用户授权的 k3s 系统配置位于 `nix/bootstrap/`；旧服务归档的保存与恢复范围见 `legacy-minecraft-archive.md`。
