生产部署源码在 encore 的 `/home/linwhite/chorus-testbed`，裸仓库在 `/home/linwhite/git/chorus-testbed.git`。本目录脚本使用项目 `.work/temp`；Nix builder 使用 `/nix/var/nix/builds`。全部系统切换遵循 [2026-10-15 执行指令 §6.4](../docs/directives/2026-10-15-decisions.md)。

每次部署执行以下顺序：

1. 同步已经验证的源码，运行 `bash deploy/build-system.sh`。脚本构建 `encore-chorus`，保存 `.work/activation/candidate-system`、GC root 和 `dry-activate.log`。检查构建结果及服务差异，确认 k3s、数据库、SSH 与十二个旧 Minecraft 单元保持规定状态。
2. 在 Mac 执行 `bash deploy/activate-from-mac.sh update /nix/store/<已审阅的系统路径>`。脚本通过内容校验确认本地与 encore 的 controller 源码一致，再重新构建并验证候选路径。源码或候选变化时立即退出，系统继续运行当前配置。
3. 脚本保存 `/run/current-system`，通过 `systemd-run --on-active=600 --unit=chorus-revert` 安排旧系统的 `switch-to-configuration test`，随后对候选执行 `test`。
4. 使用新的 SSH 连接验证 `k3s`、`mysql`、`postgresql`、`chorusd`、`dnsmasq` 均为 active，encore 为 Ready，十二个旧 Minecraft 单元均为 masked/inactive。继续检查真实 API 鉴权健康请求、Mac 到 8790 的连通性、DNS UDP/TCP 查询。
5. 全部检查通过后停止恢复 timer，设置持久系统 profile，再执行候选的 `switch`。脚本核对运行路径与 profile 相同，并写入 `.work/activation/persisted-system`。任何检查失败时保留自动恢复 timer，不执行持久切换。

首次迁移使用 `deploy` 参数。`prepare-production.sh` 在确认构建空闲、磁盘余量和 SQLite 状态后，停止前台 P1 控制器，将数据与相同秘密复制到正式目录，并核对内容与 SQLite 记录数量。原 P1 数据继续保留；后续更新固定使用 `update` 参数。

脚本的职责如下：

| 脚本 | 操作 |
| --- | --- |
| `bootstrap.sh` | 部署两个 namespace、RBAC、mc-router，并生成受限 kubeconfig |
| `build-system.sh` | 构建完整系统、建立 GC root、输出 dry-activate 差异 |
| `test-system.sh` | 核对基础服务与旧单元，设置 600 秒恢复 timer，执行候选 test |
| `check-system.sh` | 在新的 SSH 连接中检查实际服务、节点、API 与 DNS |
| `persist-system.sh` | 验证最近的 SSH 检查凭证，停止 timer，持久化并执行 switch |
| `install-hop.sh` | 持有共享构建锁，安装固定 SHA-256 的 `chorus-hop` jar 并再次校验 |
| `test-kube.sh`、`test-pod-network.sh` | 在隔离 namespace 中执行真实 Kubernetes 与 Pod 网络验证 |
| `verify-room-mods.py` | 对实际房间逐个核对 jar 名称、大小与 SHA-256 |

正式目录包括 `/srv/chorus`、`/var/lib/chorusd/config.toml`、`/var/lib/chorusd/kubeconfig` 和 `/var/lib/chorusd/secrets/`。用户 token 使用 `[users]` TOML，签名密钥为原始 32 字节。目录与凭据只在本地受限路径保存，部署日志不输出凭据内容。

每次真实构建通过固定的 `chorus-build@<buildId>.service` 执行同一个 Go 二进制的 `build-worker` 子命令。控制器管理排队与事务，systemd 限制整个构建进程组，`/srv/chorus-build.lock` 串行化 Gradle。构建日志位于 `/srv/chorus/builds/<buildId>/`，服务工作目录为 `/srv/chorus/work/<buildId>`。
