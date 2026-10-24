# Ruri Vex · 2026-10-21 接续验收

工作目录 `/Users/linwhite/ruri-vex-experimental`，分支 `feat/vex-mrpack`。证据路径相对该目录。服务端部分见 `docs/reports/2026-10-21-vex-p2-continuation.md`。

## 提交与推送

| 提交 | 内容 | 验证 |
| --- | --- | --- |
| `156333f` | feat(vex): move Vex into optional static modules behind host extension points | 该状态单独完整测试：启用 638 项（9 项联网跳过）、关闭 593 项通过 |
| `89bd8de` | fix(vex): let the app and its CLI delete each other's sessions | 与已测试并已构建的源码逐字节相同 |
| `3544814` | test(vex): require VEX_IT_URL for device and lease integration tests | 同上 |
| `3b43004` | build(nix): package Ruri with and without the Vex component | 两种 Nix 构建通过 |

- 最终状态完整测试：启用 638 项（执行 629、跳过 9）、关闭 593 项通过；Python 工具测试 38 项通过；本地化检查通过。`.work/vex-oct21-{enabled,disabled,c1-enabled,c1-disabled}-tests.log`、`vex-oct21-tool-tests.log`。
- Nix（nixpkgs `f20cd90e…`）：启用 `/nix/store/w8gc0zhdw5wd9ippwiw8gilnyzb0am4h-ruri-launcher-0.11.0`，关闭 `/nix/store/rqy0889gjd53h5ikybh969xjpkg4b1pm-ruri-launcher-0.11.0`。Nix 源码集合的 895 个文件与提交内容逐字节相同。关闭产物经 `.work/verify-disabled.py` 检查：四份 CLI golden、依赖、包内路径、Info.plist、链接库、符号与可读字符串都不含实验组件，没有创建数据目录。
- 关闭组件的 SwiftPM 测试会在工作目录删除 `Package.resolved` 中的 EventSource pin；每次运行后都恢复为提交内容，Nix 构建与提交使用的都是含 EventSource 的版本。
- 秘密扫描 `.work/vex-source-audit-20261020T192051Z/review.json`：331 个提交、4314 个历史对象、913 个当前文件、6 个归档成员；候选只属于已审阅的本地化摘要前缀与上游账号测试数据两类，实际凭据 0。
- `imdomestic/ruri-chorus` 新分支 `feat/vex-mrpack` 正常推送到 `3b43004`；`feat/vex-experimental` 保持 `61dc1df`。没有联系上游，独立 SDL 修复分支保持原样。

## 联网测试入口：通过

- 两个 Device Flow 测试与 lease 测试同时要求 `VEX_IT_URL` 与各自参数。只设置各自参数时三项都 skipped：`.work/vex-oct21-t03-root-absent.log`。
- 完整变量下真实取消测试 0.417 秒通过；真实到期测试 899.266 秒通过：`.work/vex-oct21-t03-{cancel-real,expiry-real-2}.log`。第一次到期测试与服务端部署重叠（vexd 重启后等待中的 Device Flow 只在进程内存中），提前结束，记录保留在 `.work/vex-oct21-t03-expiry-real.log`。

## Keychain 跨进程删除：修复并通过

- 原因：主程序与 CLI 的 designated requirement 相同，读取可以跨进程；默认访问对象的 Decrypt 只信任创建者路径，ChangeACL 应用列表为空，没有 Delete 授权项，删除只允许创建条目的可执行文件，因此另一方删除返回 `-25244`。只读 ACL 证据 `.work/vex-oct21-t04-acl-{gui,cli}-created.json`。
- 修复：新建条目时传入 `kSecAttrAccess`，受信任应用为应用包与 `Contents/Helpers/ruri-cli`，并为该项增加 `ACLAuthorizationDelete`（`Sources/RuriVex/VexSettings.swift`）。
- 真实验收：GUI 邀请登录 → CLI 退出；CLI 邀请登录 → GUI 退出；正式签名 CLI 文本模式 GitHub 登录（自动打开 Arc 设备页，用户批准）→ GUI 退出。三种情况都吊销服务端会话、删除条目、清除配置，没有系统窗口；Microsoft 条目与其他服务器配置不变。数据库与 vexd 日志中没有 GitHub 凭据。

## GUI 提示

- 拒绝 HTTP：“添加服务器”输入 `http://…` 显示“服务器地址必须使用 HTTPS。”，列表不变。
- 真实到期：GitHub 15 分钟期限到达时，登录表单中的授权码与“等待授权…”消失，显示“授权已超时，请重新登录。”。`.work/vex-oct21-t05-gui-timeout-3.jsonl`。
- 功能关闭时的 URL 入口：lldb 启动签名 GUI（全新数据目录），`open -a` 送达 `ruri://vex?…`，界面显示“Vex 尚未启用。…”；Vex 客户端、事件流、Token 存储、传输、认证的 372 个断点位置命中为 0，`SecItem*` 与 socket 调用中没有 Vex 调用栈；正常退出。`.work/vex-oct21-disabled-url/`。
- PR 链接确认后进入房间：见服务端报告第 6 次启动。
- 未解决：四次 GitHub 登录观察中，两次主窗口在发起后约 3.5 分钟从 AX 中消失（第一次 204 秒，第四次 225 秒），一次表单在到期后约 3 秒关闭；两次窗口消失后正常退出卡在 `prepareToQuit`，以 SIGTERM 结束。源码中没有关闭窗口的路径，原因未确定，也可能是人工关闭窗口。证据 `.work/vex-oct21-t05-gui-timeout{,-2,-4}.jsonl`、`vex-oct21-gui-sample*.txt`。
- 账号未关联：对没有用户映射的独立 8792 服务发起 GUI GitHub 登录，用户批准后表单显示“这个 GitHub 账号尚未加入该服务器，请联系管理员。”并持续显示；本地没有保存会话或 Keychain 条目。`.work/vex-oct21-t05-unlinked.jsonl`。

## MRPack 实际复用与文件安全：通过

- 同一 PR 目标两次 prepare：第二次 4.35 秒，模组三种摘要、大小与修改时间完全不变，没有重新下载。
- 模组集合恰好等于 Build 的 both 端文件，三种摘要一致；server 端 vex-agent 不在客户端。
- 非托管 JAR 保留并在计划中报告；修改托管文件时 prepare 失败（`CoreModpackUpdater.filesChangedAfterPreview`），文件保持用户修改，恢复后 prepare 成功。
- 包内 `servers.dat` 不导入；QuickPlay 参数指向房间地址。游戏运行后 Minecraft 自行写入隐藏 QuickPlay 项，由下次启动前的 `cleanBeforeLaunch` 清理（本轮没有剩余启动次数，未实际执行）。
- 旧提交回归：`c932eab71-fabric` prepare 为 generation 4、Build `fabric-932eab713246`，`launch --dry-run` 一致，随后正常关闭。
- 证据 `.work/vex-oct21-t07/`、`.work/vex-oct21-t07-old/`。

## 120 秒自动续租：通过

- 续租只在加入仍等待时触发；现有房间重新启动远少于 120 秒，没有自然的长时间等待。真实排队条件为构建、种子恢复与容量；`pr1-fabric` 的世界保留在 seraph。
- 用户允许后临时 `kubectl cordon seraph`（seraph 上没有 Pod）。签名 CLI `vex prepare pr1-fabric` 使房间进入 `queued / waiting for capacity`（generation 5）。第一份租约 `b0467e10…`，120.622 秒后第二份租约 `980b8d1b…`，同一 generation 与 Build，服务端记录了两份租约。随后 SIGINT 取消，CLI 在 125.991 秒退出，本次加入的租约全部删除。之后 uncordon，CLI 会话注销。没有缩短时间、手动调用 `issue()` 或修改状态。
- 证据 `.work/vex-oct21-t06/`（`observations.jsonl`、`cli.ndjson`、`summary.json`）与 `.work/vex-oct21-t06-run.jsonl`。
- 停止这个排队中的房间时发现服务端缺陷（停在 stopping），已在 Vex `5b28631` 修复并部署，见服务端报告。
