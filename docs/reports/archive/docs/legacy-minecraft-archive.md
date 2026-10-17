# 现有 Minecraft 服务归档与恢复

用户已选择归档校验后停用六个旧服务并保留原文件。完整本地归档、独立恢复检查和 tank 全量 SHA256 校验均已通过；encore 已激活持久停用配置，六个 service 与六个 socket 均为 `masked`、`inactive`。数据位于 `/var/lib/minecraft`，约 3.22 GiB，根文件系统为 ext4。切换前后的原目录清单全部一致，原始文件继续保留。

| 服务 | 入口配置 | 归档版本 | 目录大小 | 保存接口 |
| --- | --- | --- | --- | --- |
| proxy | `0.0.0.0:25565` | Velocity | 31 MiB | `/run/minecraft/proxy.stdin` |
| lobby | `127.0.0.1:25568` | Paper 1.21.1 | 593 MiB | RCON 25578；控制台 FIFO |
| GTL | `127.0.0.1:25560` | Minecraft 1.20.1 | 548 MiB | 控制台 FIFO；RCON 关闭 |
| bingo | `127.0.0.1:25573` | Minecraft 1.21.11 | 482 MiB | RCON 25577；控制台 FIFO |
| speedrun | `127.0.0.1:25567` | Paper 1.21.11 | 1.7 GiB | RCON 25579；控制台 FIFO |
| bedrock-proxy | Java `127.0.0.1:25572`；Bedrock UDP 19132 | Velocity / Geyser | 32 MiB | `/run/minecraft/bedrock-proxy.stdin` |

17:18 JST 使用 `mcstatus 12.0.6` 查询四个世界服务和 bedrock-proxy 的 Java / Bedrock 入口，在线人数均为 0；两个代理的 `glist all` 控制台查询也确认在线人数为 0。`legacy_status.py` 支持逐项实时复查。

归档包含六个完整目录、当前世界和历史世界。speedrun 的 `world.bak`、`world_finished_20251231`、`world_finished_20260102` 分别约 386、502、359 MiB，全部保留。配置、mods、plugins、libraries、服务启动定义、原始权限、文件属主和符号链接一并保留；`/var/lib/minecraft-secrets` 单独包含在私有归档中。

| 数据组件 | 当前存储 | 归档与恢复检查 |
| --- | --- | --- |
| 世界 | `level.dat` 与 Anvil 区域文件 | 恢复到独立目录；逐项比对 SHA256、权限、属主、链接、纳秒修改时间及 ACL/xattrs；通过 nbtlib / anvil-parser2 读取全部世界与已有区块 |
| CMI SQLite | `lobby/plugins/CMI/cmi.sqlite.db` | 停止服务后复制；恢复副本执行 `PRAGMA integrity_check` |
| bingo SQLite | `bingo/config/yet-another-minecraft-bingo/bingo.db` | 同上 |
| 两个 Velocity 的 LuckPerms | `plugins/luckperms/luckperms-h2-v2.mv.db` | 停止服务后复制；同版 H2 2.1.214 驱动执行 Script，导入全新数据库，再次导出并逐字节比较 |
| MariaDB | 数据库 `minecraft`；四张 CMI 表；账号 `mc_user@localhost` | 原生逻辑导出数据库、账号和授权；在关闭 TCP 的独立实例中恢复，检查数据表并比对行数 |
| PostgreSQL | 数据库 `minecraft`、`luckperms`；owner `minecraft` | 分别生成 custom 格式归档；恢复到独立 PostgreSQL 16 实例，比对各表行数 |

PostgreSQL 的 `minecraft` 数据库当前没有应用数据表，`luckperms` 包含六张表。MariaDB 的相关表为 `CMI_inventories`、`CMI_playtime`、`CMI_playtimereward`、`CMI_users`。归档范围仅包含这些 Minecraft 数据库和相关账号。

GTL 装有 FTB Backups 2，当前配置最多保留五份备份。完整归档通过独立目标目录执行，保留其现有 `backups/` 内容。当前 ext4 文件系统没有现成的原子文件系统快照接口；运行中的世界、H2、SQLite 和外部数据库需要各自的保存机制。H2 的官方文档要求在线备份通过其 SQL 命令执行；LuckPerms 提供应用级导出命令。此处的完整原状态归档采用服务完成保存并关闭文件之后读取数据的流程。[H2 备份说明](https://h2database.com/html/features.html#database_file_layout)，[LuckPerms 导出说明](https://luckperms.net/wiki/Switching-storage-types)。

可审阅脚本位于 `docs/support/`：

1. `legacy_archive.sh` 检查六个服务已经停止，建立独立 GC root，保存文件清单、服务定义、相关数据库和 Nix 运行依赖，生成 SHA256 清单。原始世界和配置保持原状。
2. `legacy_verify.sh` 把归档解压到全新目录，比对完整清单，读取世界和 SQLite 数据，向独立 Nix store 导入运行依赖，并启动专用 PostgreSQL / MariaDB 实例完成真实恢复。测试数据库仅监听此次工作目录中的 Unix socket。`legacy_verify_h2.sh` 完成 H2 导出、导入和内容比较。只有全部检查完成才生成 `VERIFIED` 文件。
3. `legacy_files.py` 负责文件清单、内容校验与 Nix 依赖根目录收集；`legacy_validate_worlds.py` 使用格式库读取世界；SQL 文件提供真实表行数对照；`legacy_mariadb_account.py` 通过 Python CSV 库读取 MariaDB 原生 TSV 输出。
4. `legacy_transfer.sh` 使用经过核验的 SSH 身份与固定 known_hosts，将通过本地恢复检查的归档传输到 tank。`legacy_transfer_prepare.sh` 用 `SHA256SUMS` 文件的 SHA256 标识目标归属；已有目录仅在标记、属主与权限完全符合要求时允许继续传输。

归档固定的 NixOS 系统 closure 为 `12350439824` 字节。归档脚本用独立 GC root 保留该版本，外发 NAR 包只导出六个服务 unit、socket、凭据生成 unit、数据目录内 Nix 符号链接及数据库工具的依赖集合；该集合实测为 325 个 Nix 路径、`3834720392` 字节。各运行依赖根目录也分别建立 GC root。归档同时保留原始二进制依赖，可在原机器不可用时导入另一台 NixOS 主机。[Nix 本地独立 store](https://nix.dev/manual/nix/2.30/store/types/local-store.html)，[Nix 导入归档](https://releases.nixos.org/nix/nix-2.18.6/manual/command-ref/nix-store/import.html)。

encore 具备 PostgreSQL 16.15 和 MariaDB 11.4.12 的初始化、导出、启动及管理命令。工程现有 Python 环境 `/nix/store/x8qgr4h05psniynzi7ifkbsahbqm4ir1-python3-3.13.15-env/bin/python` 已安装 nbtlib 和 anvil-parser2；Java 25 路径见 P0 基础设施报告。执行时，归档目录与恢复目录均使用项目 `.work/` 下的全新目录，权限为 0700；归档和恢复的完整内容不进入 Git。

本次归档标识为 `20261014T084821Z`。本地归档位于 encore 的 `/home/linwhite/chorus-testbed-p0/.work/legacy-20261014T084821Z`，独立恢复位于同级 `lr-20261014T084821Z`。`minecraft-data.tar.zst` 为 `2575165527` 字节，`minecraft-runtime.nar.zst` 为 `2289148264` 字节。完整归档已传输至 tank 的 `/srv/archive/chorus/legacy/20261014T084821Z` 并通过远端校验。

tank 已确认可用空间约 11.0 TiB。传输采用 SSH、rsync `--partial`、每秒 5 MiB 限速，目标目录为 root 所有、权限 0700。tank 内同一份 `SHA256SUMS` 的所有项目均通过校验，`REMOTE_VERIFIED` 和本地 `TANK_VERIFIED` 均记录 `2026-10-14T09:24:39+00:00`。归档标记 `ARCHIVE_ID` 与远端 `SHA256SUMS` 文件的 SHA256 一致；既有目录的归属与权限复用检查已实际通过。原始文件、本地归档和 GC root 保留。

恢复服务时，从通过验证的归档恢复到独立目录，导入对应 Nix 运行依赖，恢复 Minecraft 属主与权限、私有凭据和数据库账号，并分别恢复相关数据库。服务定义和网络入口在独立验证完成后按用户决定启用。归档中的代理定义使用 25565 和 19132，重新启用前需检查端口和访问方式。

脚本通过 Bash 语法检查和 Python 编译检查，编译缓存写入被忽略的 `.work/`。完整恢复中的 8,937 项 Minecraft 文件及秘密目录 4 项与捕获清单逐项一致，覆盖文件内容、权限、属主、链接、纳秒修改时间和 ACL/xattrs。元数据独立测试还验证了 67 项真实文件，其中 34 项具有扩展属性，包含根目录和子目录的 access/default ACL。

15 个世界的 `level.dat` 和 1,197 个区域文件已检查，256,804 个已有区块解码成功；359 个原有零字节区域文件列入 `metadata/world-validation.json` 并原样保留。两个 SQLite 数据库通过 `integrity_check`。325 个 Nix 依赖已导入独立 store 并通过内容检查。

Minecraft 实际使用的 PostgreSQL 两个数据库和 MariaDB `minecraft` 数据库已通过原生逻辑导出，在独立 Unix socket 实例中恢复成功。PostgreSQL 各表行数与源数据库一致，MariaDB 四张表检查均为 `OK`，表清单及行数一致。两个 H2 数据库独立恢复后的导出内容与恢复前逐字节一致，源副本 SHA256 保持一致。H2 身份依据实际 LuckPerms 5.5.71 二进制中的 `H2ConnectionFactory.createConnection` 调用确认，采用空用户名与空密码；对应实现见 [LuckPerms H2 连接代码](https://raw.githubusercontent.com/LuckPerms/LuckPerms/master/common/src/main/java/me/lucko/luckperms/common/storage/implementation/sql/connection/file/H2ConnectionFactory.java)。

本地完整恢复证明为归档目录中的 `LOCAL_RESTORE_VERIFIED` 和恢复目录中的 `VERIFIED`、`H2_VERIFIED`。测试数据库实例已正常退出，原有 PostgreSQL / MariaDB 保持运行。恢复检查使用独立 systemd 单元，内存上限 4 GiB；脚本检查 Unix socket 长度，为 MariaDB 和 Java 显式指定项目工作目录作为临时目录。归档脚本对运行中服务的保护已实测通过，发现运行状态立即终止且不创建归档目录。

当前系统 generation 为 `/nix/store/n4r586zyshxnl4jhpsbbf9gisn4zwf9h-nixos-system-encore-26.05.20260911.21a67dc`。`chorus-retire-legacy` 返回 `Result=success`、`ExecMainStatus=0`；切换前后 8,937 项 Minecraft 文件和秘密目录 4 项均通过清单比对。k3s、MySQL 和 PostgreSQL 保持 `active`。
