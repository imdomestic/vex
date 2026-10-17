# P0 镜像核对

核对时间：2026-10-14。Registry 响应、镜像配置、被检查的镜像层保存在 `.work/p0-images/`；实际运行日志保存在 `.work/p0-container.log` 和 `.work/p0-container-verification.log`。这些文件由 `.gitignore` 排除。

## 镜像版本

| 项目 | Minecraft 服务端 | mc-router |
| --- | --- | --- |
| 标签 | `itzg/minecraft-server:java25` | `itzg/mc-router:latest` |
| 多架构 index digest | `sha256:7462fe34a56686719463ff898a674c1a1ce802988138d2bfdeff2507fb1644e8` | `sha256:64ae69eaa7a6800a79bbf962f027f4020878efa25183359115bc7f6ee569c36e` |
| Linux amd64 manifest digest | `sha256:5afb59532dcc9d3ad3a5dba8393b20ee37df4ba4e4fc8484f9e0a9175aa51696` | `sha256:f6b3ce3aa2016a36d4cd6848151d34780684d7cbcb84eac19dea4a6368e866e7` |
| 源码 revision | `7934d32399147acb8fe0b5e34efea1cd20d5e83a` | `3b9fb8f6c4783fb3ac685fcb6f183d0a8f6ac9a2` |
| 配置记录的版本 | `JAVA_VERSION=jdk-25.0.4.1+1` | `1.47.2` |
| 镜像创建时间 | `2026-10-13T16:06:22.405543941Z` | `2026-10-04T13:53:14.191328174Z` |

对应固定引用：

```text
itzg/minecraft-server:java25@sha256:7462fe34a56686719463ff898a674c1a1ce802988138d2bfdeff2507fb1644e8
itzg/mc-router:1.47.2@sha256:64ae69eaa7a6800a79bbf962f027f4020878efa25183359115bc7f6ee569c36e
```

通过 Registry V2 API 获取真实响应，逐一计算 index、amd64 manifest、config 的 SHA-256，与 Registry 返回值及引用关系核对一致。`mc-router:1.47.2` 与 `latest` 的 index digest 已分别查询，结果一致。镜像源码按 config 的 `org.opencontainers.image.revision` 获取。

## 服务端配置

`ACCEPTS_TRANSFERS="true"` 对应 `server.properties` 中的 `accepts-transfers=true`。已同时检查固定 revision 的定义文件和上述 amd64 镜像层内的 `image/property-definitions.json`，两处映射一致。[属性映射源码](https://github.com/itzg/docker-minecraft-server/blob/7934d32399147acb8fe0b5e34efea1cd20d5e83a/files/property-definitions.json)

Fabric 使用 `TYPE=FABRIC`，Minecraft 版本通过 `VERSION` 传入，loader 版本通过 `FABRIC_LOADER_VERSION` 传入。[Fabric 官方镜像说明](https://docker-minecraft-server.readthedocs.io/en/latest/types-and-platforms/server-types/fabric/)

## 镜像文件

从固定 amd64 manifest 指定的 Registry blob 下载相关镜像层，逐层核对 SHA-256 和字节数，再使用 Python `tarfile` 检查归档成员。结果如下；所有命令均在 Mac 上检查下载的归档内容。

| 要求 | 镜像层中的文件 | 权限及内容 |
| --- | --- | --- |
| `mc-health` | `/usr/local/bin/mc-health` | 指向 `/image/scripts/shims/mc-health`；目标脚本权限 `0755` |
| `rcon-cli` | `/usr/local/bin/rcon-cli` | 权限 `0755`，6,279,328 字节；构建配置指定 `1.7.8` |
| `tar` | `/usr/bin/tar` | 权限 `0755`，440,264 字节 |
| `gzip` | `/usr/bin/gzip` | 权限 `0755`，93,424 字节 |
| `curl` | `/usr/bin/curl` | 权限 `0755`，297,288 字节 |

构建文件安装 `rcon-cli`，复制 health 脚本并建立命令链接；Ubuntu 包安装脚本安装 `curl`。[固定 revision 的镜像构建文件](https://github.com/itzg/docker-minecraft-server/blob/7934d32399147acb8fe0b5e34efea1cd20d5e83a/Dockerfile)、[Ubuntu 包安装文件](https://github.com/itzg/docker-minecraft-server/blob/7934d32399147acb8fe0b5e34efea1cd20d5e83a/build/ubuntu/install-packages.sh)

静态文件核对与容器内实际执行均已通过。GNU tar 版本为 `1.35`，gzip 为 `1.12`；curl 成功获取 Fabric 官方服务端元数据；真实 `world/level.dat` 经 tar/gzip 压缩、解压后逐字节一致。

## mc-router 配置

固定 revision 支持 `--in-kube-cluster` 与 `KUBE_NAMESPACE=chorus-rooms`；Service 使用 `mc-router.itzg.me/externalServerName` 注册主机名，端口名支持 `minecraft`。`mc-router.itzg.me/autoScaleUp="false"` 与 `mc-router.itzg.me/autoScaleDown="false"` 可关闭对应 Service 的自动扩缩。Webhook 配置保留 `WEBHOOK_EVENTS=connect,disconnect`、`WEBHOOK_REQUIRE_USER=false`。[mc-router 1.47.2 对应说明](https://github.com/itzg/mc-router/blob/3b9fb8f6c4783fb3ac685fcb6f183d0a8f6ac9a2/README.md)

已检查固定 revision 的 `server/connector.go`：玩家信息读取条件为 `handshake.NextState == mcproto.StateLogin`。transfer 连接依赖 SLP 轮询确认玩家数量。

## 运行验收状态

用户授权搭建的 encore k3s 节点运行 `v1.35.7+k3s1`。实际在 `chorus-p0` 创建临时 PVC 和 Pod，固定到 encore；PVC 使用 `local-path`，事件确认等待消费者后完成绑定。

Pod 于 `2026-10-14T08:17:51Z` 启动容器，`08:18:08Z` 进入 Ready，重启次数为 `0`。服务端实际加载 Minecraft `26.3`、Fabric Loader `0.19.5`、Chorus `26.3.0.0` 和 Fabric API `0.161.0+26.3`。容器内两个模组的 SHA-256 与构建及依赖下载记录完全一致。

`mc-health` 返回版本 `26.3`、在线玩家 `0`、预期 MOTD；`rcon-cli list` 和 `rcon-cli 'save-all flush'` 执行成功。生成的配置为 `accepts-transfers=true`、`online-mode=true`、`white-list=true`、`enforce-whitelist=true`。JVM 为 Temurin `25.0.4.1+1-LTS`。

运行时临时文件位于 `/data/.work/`，`TMPDIR`、`JAVA_TOOL_OPTIONS`、`CONSOLE_IN_NAMED_PIPE` 均显式指定该目录。临时 Pod 使用集群内部地址，没有外部 Service 或 hostPort。验收结束后删除 `chorus-p0`，等待 namespace 删除完成；API 确认对应 PV 不存在，宿主上的该卷目录也已删除。

## 本地证据

- `.work/inspect_registry.py`：获取并校验两种镜像的 Registry index、amd64 manifest 与 config。
- `.work/inspect_image_files.py`：下载并校验所需镜像层，检查工具文件和属性映射。
- `.work/p0-images/registry-result.json`：固定镜像引用、revision、创建时间与层描述。
- `.work/p0-images/image-file-result.json`：工具路径、权限、长度及来源镜像层 digest。
- `.work/p0-kubernetes-resources.json`：实际 Pod 状态、镜像 ID、PVC 绑定信息。
- `.work/p0-persistent-volume.json`：实际 PV 和节点约束。
- `.work/p0-minecraft.yaml`、`.work/p0-in-container.sh`：本次资源与容器验收命令。
