# P0 基础设施实测记录

检查时间：2026-10-14，Asia/Tokyo。通过 Mac 上的 `linwhite` 账号使用 SSH 检查 encore、seraph、tank；连接使用 `BatchMode=yes`、`ConnectTimeout=12`。encore 已运行 k3s，真实 Fabric Pod 已完成启动和清理验证。现有 Minecraft 服务的归档与停用进度见 `legacy-minecraft-archive.md`。

| 主机 | Tailnet IPv4 | LAN IPv4 / 接口 | Tailnet DNS | SSH |
| --- | --- | --- | --- | --- |
| encore | `100.64.0.43` | `10.1.2.137/24` / `wlo1` | `encore.inner.imdomestic.com` | Mac 可登录 |
| seraph | `100.64.0.44` | `10.1.2.194/24` / `enp11s0` | `seraph.inner.imdomestic.com` | 当前离线 |
| tank | `100.64.0.4` | `192.168.20.115/24` / `br-lan` | `tank.inner.imdomestic.com` | Mac、encore 均已验证登录 |

encore 和 seraph 已核验的 `nixos-version` 均为 `26.05.20260911.21a67dc (Yarara)`。当前 tailnet 使用 `inner.imdomestic.com` 域名，API 的实际主机名采用 `encore.inner.imdomestic.com`。seraph 的 agent 尚未部署，待恢复连接后核对并准备。

| P0 检查项目 | 权威证据 | 当前结果 |
| --- | --- | --- |
| k3s server | `sudo k3s kubectl get nodes -o wide` | encore 为 `Ready`，角色 `control-plane`，版本 `v1.35.7+k3s1` |
| 节点 IP | 同上 | `10.1.2.137`，内核 `6.18.50` |
| containerd | 节点 `.status.nodeInfo.containerRuntimeVersion` | `containerd://2.2.5-k3s2` |
| StorageClass | `sudo k3s kubectl get storageclasses` | 默认 `local-path`；`rancher.io/local-path`；`WaitForFirstConsumer`；`Delete` |
| ServiceLB | 已部署 k3s 配置与节点标签 | 已启用；encore 的 `svccontroller.k3s.cattle.io/enablelb=true` |
| 真实 Fabric Pod | P0 镜像验证记录 | `08:17:51Z` 启动，`08:18:08Z` Ready，重启次数 0 |
| `chorus-p0` 生命周期 | `kubectl get namespace chorus-p0 --ignore-not-found` 与 `kubectl get pv` | 验证结束后命名空间和 PV 均已清理 |

当前集群包含 encore 一个已就绪节点。seraph 的运行验收尚待主机恢复连接。

| 存储位置 | 文件系统 | 容量 / 可用空间 | 权限与存在状态 |
| --- | --- | --- | --- |
| encore `/srv` | `/dev/nvme0n1p5`，挂载于 `/` | 总量 `367460651008` 字节；完成本地归档与恢复后可用 `42464751616` 字节，约 39.5 GiB | `/srv` 属于 root，0755；`/srv/chorus` 尚不存在 |
| seraph `/` | `/dev/nvme2n1p4` | 总量 `789404262400` 字节；可用 `565116252160` 字节，约 526 GiB | 根磁盘查询成功 |
| tank `/` | `/dev/sdd:/dev/sdb` | 总量 `13099978665984` 字节；归档前可用 `12130904498176` 字节，约 11.0 TiB | `/srv/archive/chorus/legacy/20261014T084821Z` 已创建，root 所有、0700 |
| tank `/home/linwhite` | 同上 | 同上 | 属于 linwhite，0700；`test -w` 成功 |
| tank `/data/srv` | 同上 | 同上 | 属于 root，0777；`test -w` 成功 |
| tank `/data/builds` | 同上 | 同上 | 属于 root，0777；`test -w` 成功 |

空间数值来自 `df -B1` 的检查时刻。tank 的 root SSH 身份已经实际验证，`/srv` 可写，归档目标为 `/srv/archive/chorus/legacy/<UTC时间>`，目录权限为 0700。tank 提供 `rsync 3.5.0` 和 `sha256sum`。

encore 到 tank 已使用严格主机密钥校验完成 `ssh ... tank hostname`，输出 `tank`。验证使用 Mac 已信任的 SSH 会话读取 tank ED25519 公钥，并通过本次连接的 `KnownHostsCommand` 提供给 encore，设置 `StrictHostKeyChecking=yes`、`UpdateHostKeys=no`，没有写入两机的 SSH 配置或 known_hosts。该公钥指纹为 `SHA256:2DbKvgpyR8+RZifi3sQ5XKoU1octRo1ekWQ5tEsW8JA`。无人值守的 chorusd 服务仍需要自己的可读 SSH 身份文件与受信任主机密钥配置。

encore 的旧 TCP 25565 服务定义为 `minecraft-server-proxy.service`：

| 属性 | 实测值 |
| --- | --- |
| 服务用户 | `minecraft` |
| 服务目录 | `/var/lib/minecraft/proxy` |
| 配置绑定 | `velocity.toml` 中 `bind = "0.0.0.0:25565"` |
| 停止前监听 | `ss -ltnp` 显示 `*:25565` |

该绑定覆盖 encore 的本地 IPv4 地址。用户已选择归档校验后停用旧服务并保留原文件；完整归档、独立恢复与 tank 全量 SHA256 校验均已通过。持久归档位于 `/srv/archive/chorus/legacy/20261014T084821Z`。encore 当前 generation 为 `/nix/store/n4r586zyshxnl4jhpsbbf9gisn4zwf9h-nixos-system-encore-26.05.20260911.21a67dc`，六个旧 service 与六个 socket 均为 `masked`、`inactive`；切换前后 8,937 项 Minecraft 文件及秘密目录 4 项一致，k3s、MySQL、PostgreSQL 保持运行。检查过的四个后端 `whitelist.json` 均为空，尚无可据以确认 Linwhite / hank Minecraft 用户名的白名单记录。

encore 的 TCP / UDP 53 与 TCP 8790 在检查时没有监听。分离 DNS 规则尚未配置。两机资源实测：

| 主机 | 物理内存 | 检查时可用内存 | Swap |
| --- | --- | --- | --- |
| encore | `24873283584` 字节，约 23.2 GiB | `7654322176` 字节，约 7.1 GiB；运行负载期间会变化 | 0 |
| seraph | `32645382144` 字节，约 30.4 GiB | `31348928512` 字节，约 29.2 GiB | 64 GiB |

表中内存数据来自旧 Minecraft 服务与构建工作并行时的测量。单个 5 GiB 上限的 Fabric Pod 已通过运行验收；`max_running.encore = 2` 的并发容量仍需依据完整部署后的常驻负载验收。归档恢复检查使用独立 4 GiB 内存上限。

encore 的现有 secret 管理配置位于 `/home/linwhite/.config/nix-config`，检查时 HEAD 为 `cc004e5814bb2dbef08186901b71f65c83bae064`。`flake.nix` 引入 `sops-nix`；`nixos/profiles/base.nix` 导入其 NixOS module，`defaultSopsFile` 优先选择 `secrets/hosts/<host>.yaml`，没有该文件时使用 `secrets/secrets.yaml`。encore、seraph 的专用 YAML 和 encore 的 `/run/secrets` 当前均不存在。Chorus module 可以沿用文件路径接口，但其 secret 文件尚未配置。该仓库保持原状。

encore 的地图工程位于 `/home/linwhite/d2-map-dev`，HEAD 为 `b4a839756388c00d9e9fb790643d1e16d398b225`，检查前后 `git status --short` 均为空。包名为 `d2map`，`pyproject.toml` 注册 `d2map = "d2map.cli:main"`，实现使用 Python `argparse`。既有命令包括：

```sh
d2map run v3-plaza --config configs/v3-plaza.toml
d2map run v3-full --config configs/v3-full.toml
d2map status v3-plaza legacy-3e259153
d2map audit v3-plaza legacy-3e259153
d2map templates workbench --line v3-plaza --run-id legacy-3e259153
d2map runtime build v3-plaza --run-id legacy-3e259153 --arguments :fabric:jar
```

CLI 尚未注册独立的资产发布命令。`src/d2map/lines/v3_plaza/worlds.py` 已有内部 `publish(cfg)` 函数，职责为导出比较世界并运行服务端验证。新增 Chorus 发布命令应沿用 `d2map <命令> [选项]` 的结构和 `paths.toml` / `D2MAP_PATHS` 路径机制。

| 已有转换产物 | 绝对路径 | 检查时大小 |
| --- | --- | --- |
| 广场样板世界 | `/home/linwhite/d2-assets/data/handmade/v3-plaza/sample/world` | 4.9 MiB |
| 广场比较世界 | `/home/linwhite/d2-assets/data/runs/v3-plaza/legacy-3e259153/compare/world` | 8.8 MiB |
| 庭院世界 | `/home/linwhite/d2-assets/data/runs/courtyard/legacy-2026-10-08/world` | 5.5 MiB |
| 全图比较世界 | `/home/linwhite/d2-assets/data/runs/v3-full/eedbd9ff/compare/world` | 1.5 GiB |

广场配置 `configs/v3-plaza.toml` 为 `scale = 2.0`。样板的 `selection.json` 记录 variant `D`，对应 manifest SHA256 为 `871548a0fe9d89f90b163aef2521733c9936e80bc3a3498aa02e616691f17b23`。既有 `validation.json` 的 `.value` 为 `status = "passed"`、`failures = 0`、`samples = 1350`，记录 37 个世界文件。

本次使用工程已安装的 `nbtlib` 读取样板 `world/level.dat`，确认 `Data.Version.Name = "26.3"`、`DataVersion = 5023`、`ServerBrands = ["fabric"]`。实际世界包含 `level.dat`、`data/`、`datapacks/`、`dimensions/`、`players/`；主世界区块位于 `dimensions/minecraft/overworld/region/*.mca`。源码 `src/d2map/core/mc/world.py` 使用同一路径。发布归档应以选定世界目录为唯一输入，生成顶层 `world/`，并校验 MC 26.3 的实际目录结构。原始提取资源位于独立的 `d2-assets/sensitive`，本次未读取或复制其内容。

工程通过 `flake.nix` / `flake.lock` 固定 Python 与运行工具。当前 `paths.toml` 中的 Java 25 安装目录为 `/nix/store/jzgzdbryqzji2a6wqcxmbw46hqm7bga5-openjdk-25.0.4.1+1/lib/openjdk`；Java 21 安装目录为 `/nix/store/r5yyzs1n6hrs3n6kjmxlf8dwynbif5mq-openjdk-21.0.12.1+1/lib/openjdk`。实际读取 NBT 使用 `/nix/store/x8qgr4h05psniynzi7ifkbsahbqm4ir1-python3-3.13.15-env/bin/python -B`，没有生成 Python 字节码。
