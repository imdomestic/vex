# Ruri Vex P1 真实客户端验收

验收使用 `/Users/linwhite/ruri-vex-experimental/.work/review/gui-final/build/Ruri.app` 内的 P1 CLI，数据目录为同仓库 `.work/vex-real-it`。正式服务器为 `http://encore.inner.imdomestic.com:8790`。2026-10-18 18:07 JST 检查到 Mac 已解锁，隔离 Microsoft 账号 `linWhite` 的实际认证成功。原 Ruri 的数据和账号保持原状。

## 游戏记录

| 验收 | 结果与时间（UTC） | 证据 |
|---|---|---|
| chorus main | 09:10:31 实际进入；generation12、build `fabric-779645c48650`；SLP 在线人数1 | `.work/vex-p1-main-launch.json`、`.work/vex-p1-main-joined.log`、`.work/vex-p1-main-slp.json` |
| main → plaza | RCON 以实际玩家执行 `/server world:plaza`，客户端收到 transfer 并进入 seraph 的 `w-plaza-779645c4-fabric`；09:11:05 joined、generation1、SLP 在线人数1 | `.work/vex-p1-plaza-joined.log`、`.work/vex-p1-plaza-slp.json` |
| plaza → main | `/server room:main-fabric` 返回，09:11:18 joined、SLP 在线人数1 | `.work/vex-p1-main-return.log`、`.work/vex-p1-main-return-slp.json` |
| example main | 09:12:47 实际进入；generation3、build `fabric-3cf4f9d4009a`；SLP 在线人数1；玩家执行 `/vexexample` 返回 `Vex Example v2` | `.work/vex-p1-example-main-launch.json`、`.work/vex-p1-example-main-joined.log`、`.work/vex-p1-example-main-slp.json`、`.work/vex-p1-example-main-command.log` |
| example 旧提交 | 09:13:31 实际进入 seraph 的 `c932eab71-fabric`；generation3、build `fabric-932eab713246`；SLP 在线人数1；玩家执行 `/vexexample` 返回 `Vex Example v1` | `.work/vex-p1-example-old-launch.json`、`.work/vex-p1-example-old-joined.log`、`.work/vex-p1-example-old-slp.json`、`.work/vex-p1-example-old-command.log` |

chorus main 启动前自动读取的新 head 是 `779645c48650e575c12a596aad412dab8a090236`。客户端实例复用了既有隔离实例，按新 Build 更新模组。example head 为 `3cf4f9d4009a8a9e46810096e9e9366c8b2e8095`，旧提交为 `932eab713246083bfb944bca2058126cc9214e70`；两个版本复用同一隔离实例并实际进入对应服务器。当前游戏启动次数为 **3/8**。

## 操作与退出

首次进入时 Minecraft 显示辅助功能引导，AX 仅提供标准窗口控件。通过 `javap` 检查实际 26.3 客户端的 `Gui.buildInitialScreens`、`AccessibilityOnboardingScreen.onClose` 与 `Screen.shouldCloseOnEsc` 后，发送一次 Escape 完成正常引导；随后 QuickPlay 自动连接 main。没有使用截图或视觉功能。

P1 备份的 `session quit` 返回 `appExitRequestDisabled`，该包未包含独立 SDL 通用修复。三次游戏均通过 AX 标准窗口关闭按钮退出；客户端日志记录 `Stopping!`，`ruri-game` 与 `ruri-monitor` 进程均结束。服务端房间由主任务执行正常停止和归档。

## GUI 验收

已使用 `RURI_DATA_DIR` 打开 P1 备份 GUI，PID20260；`lsof` 确认记录数据库与实例位于 `.work/vex-real-it`。用户完成该 GUI 的 Keychain 授权后，应用正常显示主窗口。生产 CLI 会话与 Microsoft 认证已完成实际验证。

| GUI 项目 | 实测结果 |
|---|---|
| 隔离目录 | 设置页显示 `.work/vex-real-it`，`lsof` 结果一致 |
| 重命名 | 使用设置菜单改为 `encore-gui-check`，签名 CLI 读取确认，随后通过同一菜单恢复 `encore-production` |
| 单框邀请登录 | GUI 对独立 `http://127.0.0.1:8792` 的邀请链接添加并登录成功，保存名称 `friend-validation` |
| 服务器及项目分组 | 服务器页实际显示 encore-production、friend-validation 分组，后者包含 chorus、example 项目 |
| 会话失效 | 管理员通过临时实例的 Unix socket 撤销此次 GUI 会话后，GUI 自动显示“登录已失效”；正式 CLI whoami 继续成功 |
| 退出与删除 | GUI 的退出菜单清除临时本机会话并显示“未登录”，随后删除 friend-validation；正式连接保留 |
| 正式 FQDN 的 GUI 请求 | CFNetwork 返回 ATS `-1022`，设置页及服务器页显示无法连接；尚未通过 |

证据为 `.work/vex-p1-gui-home.txt`、`vex-p1-gui-settings.txt`、`vex-p1-gui-renamed.json`、`vex-p1-gui-servers.json`、`vex-p1-gui-servers-tree.txt`、`vex-p1-gui-expired.txt`、`vex-p1-gui-revoke-primary.json` 和 `vex-p1-gui-ats.log`。当前 GUI 使用的 P1 备份未包含随后新增的 SSE 状态显示；正式域名与最新 GUI 的验收将使用 HTTPS 和方案 C 的模块构建。

8792 的临时会话列表最终为空，`vex-login-it.service` 已停止，本次 SSH 转发已结束。隔离目录中只保留 encore-production，最终 CLI whoami 成功。证据为 `.work/vex-p1-gui-logout-tree.txt`、`vex-p1-gui-final-servers.json` 和 `vex-p1-gui-final-whoami.json`。正式 HTTPS 域名 GUI 请求和最新 GUI 的断开状态将在模块改造后验证；服务端 P1 合并与 P2 已获得独立推进授权。

证据路径均相对于 `/Users/linwhite/ruri-vex-experimental`。反编译检查输出仅保存在 ignored `.work/vex-p1-*-javap.txt`。报告不包含凭据。
