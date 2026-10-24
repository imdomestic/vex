# Vex P2 · 2026-10-21 接续验收

本记录接续 `2026-10-20-vex-p2-validation.md` 与 `2026-10-20-pause-handoff.md`。证据路径相对 Vex 工作目录，encore 上的路径写全路径。Ruri 部分见 `docs/ruri/reports/2026-10-21-ruri-vex-continuation.md`。逐步过程记录在 `/Users/linwhite/Downloads/vex-progress-2026-10-20/PROGRESS-2026-10-21.md`。

## 现场核对

- Vex `feat/vex-p2` 从 `eeb2492685aeeeb7e88f3bbeb917e6fd37cec768` 继续，路由修复保持暂存；Ruri `feat/vex-mrpack` 从 `61dc1df78cd961714f2034b751940495bea3f4e1`、暂存树 `286a09df0c61fa309af0337e1d688d4ca59b5b9e` 继续。
- 从 Mac 全新 SSH 检查 encore：系统 `byd1yvrajcwrnq79157g8r4xfxd83jrj`，应用 profile `7j85yn0z413hva8aw6ggqxvn0rj7qk34`，受保护服务全部 active，临时表与 8792/8793 监听不存在，两个节点 Ready，三个 PVC UID 不变。seraph 系统 `s1i7jkdj2wrzpcchfvz08cjf7w05sy3d`。证据 `.work/vex-oct21/resume-{health,seraph}.log`。

## seraph 真实路由恢复：通过

- 临时 overlay `.work/vex-oct20/temporary-system.nix` 删除 8792 许可与 `vex-auth-validation` 表，只保留 8793 的端口许可、反向路径许可和限定来源表。nix-config 两份副本逐文件相同；不加 overlay 求值的两台系统与实际运行系统相同。
- 两台候选的 dry-activate 只 reload `nftables.service`，闭包差异为空，规则差异只有 8793。两台分别经过 600 秒恢复计时器、test、Mac 全新 SSH check、persist。证据 `.work/vex-oct21/route8793-*`。
- encore 上的测试二进制由当前源码以相同参数重新编译，SHA-256 相同（`57fd64e3…`）。
- `run-wake-two-node.sh seraph o20s2 8793`：PASS，59.26 秒。休眠 MOTD 不带就绪身份；非成员与直接来源被拒绝；登录身份触发启动并受 60 秒限速；Pod 实际运行在 seraph；Service 身份保持并恢复 selector；经 mc-router 的 SLP 身份匹配；正常停止完成归档并恢复休眠路由；测试 PVC UID 保持；关闭后删除自身 namespace。随后核对 namespace、PV、测试进程与 8793 监听均不存在，三个生产 PVC 不变。证据 `.work/vex-oct21/wake-seraph-o20s2{,-cleanup}.log`，encore `/home/linwhite/vex-work/.work/vex-oct20/runtime-regression/evidence/wake-3598428608`。

## 提交、推送与部署：完成

- 完整 Go 检查通过：`.work/vex-oct21/go-checks.log`。
- 提交 `27b9f7fba38a8046287b55ba02e268e5bce39016`（`fix(kubernetes): verify sleeping routes through Pod exec status`），只含 8 个代码文件。
- 扫描 `.work/vex-source-audit-20261020T152323Z/review.json`：32 个提交、702 个历史对象、354 个当前文件、68 个归档成员、六种字符串编码；未分类候选 0，实际凭据 0，提交信息候选 0。
- `feat/vex-p2` 正常推送 origin 与 encore：`c53d038..27b9f7f`。
- encore 全新 checkout `/home/linwhite/vex-oct20-27b9f7f` 构建应用 `/nix/store/y8bmd6m41rn86pisk59s4c9lkq32nzgq-vex-applications`；deploy → Mac 全新 SSH check → finish 全部通过。
- nix-config 快进到 `3025bad`（hank 的 tank/cliproxy 提交），Vex 输入更新到 `27b9f7f`。encore 最终系统 `/nix/store/mkbanhr2xc2hmqn2jnm7mn1sjipfr58i-nixos-system-encore-26.05.20260911.21a67dc` 相对原基线只有 vexd 去掉 `AF_NETLINK` 与 manifest 的源码路径变化；nftables 回到原基线。seraph 最终系统求值仍为 `s1i7jkdj…`。`.work/vex-oct20/system-operation.sh` 改为要求地址族恰好为 `AF_INET AF_INET6 AF_UNIX`。
- 两台主机按保护流程切换到最终系统（encore `mkbanhr…`，seraph `s1i7jkdj…` generation 28），8793 规则全部移除。全新 SSH 最终核对：七个服务 active、vexd 进程属于新应用、地址族 `AF_INET AF_INET6 AF_UNIX`、正式 HTTPS 与内部 HTTP 正常、未认证 `/v2/me` 返回 401、两个节点 Ready、三个 PVC UID 不变、临时表、监听与计时器均不存在。证据 `.work/vex-oct21/final-*.log`。
- 全部 20 台 NixOS 主机求值通过后提交 nix-config `c0374d12459f090c03771a04cfc33d6c9ef639c5` 并正常推送 main。应用 profile 与系统输入使用同一提交 `27b9f7f`。

## 三个客户端实际进房：通过

累计玩家游戏启动 8/8（P1 三次、SDL 退出、Hardened Runtime 主菜单，以及本轮三次）。房间 `example/pr1-fabric`，Build `fabric-901a48fd73d0`，全部在 seraph。

| 次序 | 客户端与路径 | 结果 |
| --- | --- | --- |
| 6 | Ruri GUI 打开 `ruri://vex?…room=pr1-fabric`，确认“准备并进入” | 17:09:04 UTC joined，generation 3，SLP 在线 1；`vexexample` 返回 `Vex Example CI preview 1`；AXCloseButton 退出，`Stopping!` |
| 7 | Prism 原生导入当时正式包 g3，`--server` 连接休眠地址 | 19:03:02 QuickPlay 连接使房间从 stopped/g3 进入 g4；ready 后同一游戏内返回服务器列表并重新加入，19:03:35 joined，SLP 在线 1、generation 4；`vexexample` 返回 `Vex Example CI preview 1`；正常退出 |
| 8 | HMCL 原生启动按钮（预设 QuickPlay 到 PR 地址） | 19:07:22 joined，generation 4，SLP 在线 1；`vexexample` 返回 `Vex Example CI preview 1`；正常退出 |

- 房间状态与租约只读检查脚本 `/home/linwhite/vex-work/.work/vex-oct21/pr-leases.py`，加入日志与 RCON 检查 `/home/linwhite/vex-work/.work/vex-oct21/room-player-check.sh`，SLP `.work/vex-oct21/slp.py`。
- 每次结束后正常停止房间：g3 快照 `pr1-fabric-g3`、g4 快照 `pr1-fabric-g4`（6,235,573 字节）均已经 tank 校验。旧提交房间 `c932eab71-fabric` 用于无游戏回归后正常关闭为 closed/g4，快照 `c932eab71-fabric-g4` 已经 tank 校验。
- Prism、HMCL 的证据位于 `.work/vex-p2/prism-wake-g3/`、`.work/vex-p2/hmcl-pr-g4/` 与对应日志；HMCL 实例与当时正式包 g4 的客户端文件逐字节一致（`.work/vex-p2/hmcl-pr-g4-verify.json`），实例元数据中的 versionId 仍为 g2。

## 120 秒续租排队验收与排队房间停止修复

- 用户允许后临时 `kubectl cordon seraph`。`pr1-fabric` 的保留世界在 seraph，签名 Ruri CLI 加入时房间进入真实 `queued / waiting for capacity`（generation 5）；120.622 秒后出现第二份租约（同一 generation 与 Build），取消后两份租约都被删除。随后 uncordon，两个节点 Ready。详见 Ruri 报告。
- 停止这个排队中的 generation 时，控制器在每次调和中报错 `retained world has neither generation specification nor completed archive`，房间停在 stopping（没有 Pod，PVC 保持）。原因是排队中的 generation 尚未获得规格。
- 修复提交 `5b2863127bd1f87403f5d9704185995ec06b0d07`（`fix(rooms): finish stopping a queued generation without a new archive`）：此情况直接结束停止、保留世界、不生成新快照；新增真实 SQLite 路径的 `controller/internal/rooms/stop_test.go`。完整 Go 检查通过，扫描（`.work/vex-source-audit-20261021T023303Z`）未分类候选 0、实际凭据 0，正常推送 origin 与 encore。
- encore 全新 checkout `/home/linwhite/vex-oct20-5b28631` 构建应用 `/nix/store/rkgnm86479gc1j9d0dykkqckmqmbjz99-vex-applications`，保护部署通过。部署后 `pr1-fabric` 转为 stopped/generation 5，PVC `5b46b68d…` 保持，休眠路由恢复（SLP 为休眠 MOTD）。
- nix-config 快进到 `8e05858`（另外两个提交只涉及 home 配置），Vex 输入更新到 `5b28631`。encore 系统 `/nix/store/lgimb1nmbc5fh1f1fhd6dq5f7z1jkdgc-nixos-system-encore-26.05.20260911.21a67dc` 相对上一系统只有 `vexd.toml` 中 manifest 的源码路径变化；保护切换通过。全部 20 台主机求值通过，提交 `00c8eff` 正常推送 main。

## 独立 8792 账号未关联验收

- 只含 8792 规则的临时 overlay（来源限定 Mac 尾网地址）经保护流程生效；独立服务 `/srv/vex-it-auth-unlinked-20261021` 没有用户映射。Ruri GUI 的 GitHub 登录经用户批准后显示“这个 GitHub 账号尚未加入该服务器，请联系管理员。”。
- 之后停止服务（8792 无监听），数据库会话、token、邀请与全部 records 均为 0；encore 以保护流程切回不含 8792 的系统。证据 `.work/vex-oct21/auth8792-*.log`。

## 最终状态

- encore：运行与持久系统 `lgimb1nm…`，应用 profile `rkgnm864…`（`5b28631`），根分区可用 67,602,305,024 字节。seraph：`s1i7jkdj…`。全新 SSH 最终核对 `.work/vex-oct21/final-health-2.log`。
- 三个 StatefulSet 均为 0/0；三个原 PVC Bound，UID 为 `c583ce8c-07f8-4aa1-b054-8db34357eb6d`、`c4fa6bb7-af46-4c3e-8c7d-741e35b084c4`、`5b46b68d-01a2-4428-9ae1-5ccc70eae531`。`example/pr1-fabric` 为 stopped/g5。
- 本轮创建的验收会话全部注销；`ruri on aegis` 会话 `a56f31eb…`（2026-10-17 创建）不属于本轮客户端，保持原样。临时规则、cordon、测试 namespace、临时服务与 API 令牌文件都已清理。
- Vex `feat/vex-p2` 已推送；应用 profile 与 nix-config 输入使用同一提交 `5b28631`。P2 仍有外部阻塞，功能分支不合入 `dev`。

## 剩余事项

- chorus 改为 CI 构建：`imdomestic/chorus` PR #1 仍为 OPEN（head `d1315b62…`），需要用户或 hank 合并；未合并期间保持 host 构建。
- 无写权限账号的 fork 评论负例：需要真实无写权限账号持有人。
