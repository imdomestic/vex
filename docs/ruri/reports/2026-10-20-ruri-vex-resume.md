# Ruri Vex · 10 月 20 日暂停接续记录

用户于 2026-10-20 16:47 JST 要求保存进度并暂停。GUI 已正常退出；没有玩家游戏、编译任务或 Device Flow 轮询运行。包含独立 SDL 验收在内，已核实累计游戏次数为 5/8。

## 源码与应用

- 仓库 `/Users/linwhite/ruri-vex-experimental`，分支 `feat/vex-mrpack`，HEAD `61dc1df78cd961714f2034b751940495bea3f4e1`。
- 本轮源码已暂存，index tree 为 `286a09df0c61fa309af0337e1d688d4ca59b5b9e`，没有提交或推送。接续时先读取当前 diff，保留用户期间修改。
- 当天修改包括通用 Info.plist URL handler、Git/服务器共享加入进度与取消控件、Device Flow 实际截止时间处理、两个真实联网测试、Keychain 精确引用删除。
- 唯一应用 `build/Ruri.app`，隔离数据 `.work/vex-real-it`。最新稳定签名包装完成，八个组件验证通过：`~/worklog/ruri-local-signing/.work/20261020T164615+0900-12764/result.json`。
- 包装命令只能使用 `~/.local/bin/ruri-build-local debug --build-system swiftbuild`。Team `59ZC6MZ73T`，主程序和 CLI 的 designated requirement 一致。仓库签名源码未修改。
- 正式服务器 `encore`：`https://vex.inner.imdomestic.com:8790`，默认项目 example，目前未登录。
- 临时条目 `github-unlinked`：`https://vex.inner.imdomestic.com:8792`，没有登录身份。
- 隔离 Microsoft 登录保留。用户仅允许验收账号 `8D8D27B5-9A5A-4779-B36D-EB65EAE35A0B` 使用默认 Ruri 支持目录内对应 `.account-locks` 锁文件。

## 已通过的实际验证

| 项目 | 结果与证据 |
| --- | --- |
| 完整测试 | 启用 638 项、关闭 593 项通过；联网项目在离线套件中按环境变量跳过。`.work/vex-oct20-enabled-device-tests.log`、`.work/vex-oct20-disabled-tests.log` |
| 本地化与工具 | 4048 条资源，中文和英文完整；Python 工具 38 项通过 |
| Nix | 两种构建完成。启用 `/nix/store/zxqwzzpm4bff9agkd1iy8fycw4fpihj3-ruri-launcher-0.11.0`；关闭 `/nix/store/75sgzgyq78mrvm0izk6mykjzxa3cqbsv-ruri-launcher-0.11.0`；日志 `.work/vex-oct20-nix-{enabled,disabled}-final.log` |
| 关闭产物 | 四份 golden、依赖图、资源、Info.plist、链接库、符号和可读字符串检查通过，没有实验组件或 EventSource，没有创建数据目录 |
| 运行时关闭 | 实际签名 CLI 四份 golden 一致，真实 Keychain、网络和 SSE 断点命中为零 |
| 安全扫描 | `.work/vex-source-audit-20261020T071920Z/review.json`：327 提交、4217 历史对象、913 工作区文件、6 归档成员及六种字符串编码，零凭据 |
| 正式 GUI GitHub 登录 | 用户实际授权后成功；CLI whoami 返回 linwhite/admin。`.work/vex-oct20-gui-github-whoami.json` |
| 未关联身份 | 独立 8792 实际 GitHub 授权后返回“GitHub 账号尚未关联 Vex 用户，请联系管理员”，CLI exit 1，没有保存客户端身份 |
| 真实到期与取消 | 8792 未授权请求实际等待 888.008 秒，达到 expiresAt 后超时；真实取消测试与 GUI 取消通过。`.work/vex-oct20-secondary-device-expiry.log` |
| Git 页面与 PR 链接 | 实际 PR #1 展开、固定目标 MRPack 准备、进度与取消通过；已知服务器和未知服务器链接都显示确认并可取消 |
| AX 范围 | 原生有界 AX 读取与一次 System Events 遍历完成；随后主线程采样 275/275 等待正常事件。尚未运行 VoiceOver |
| 玩家与其他启动器 | 当天没有启动玩家；Prism/HMCL 均已登录并导入 PR generation 2 包，等待统一游戏窗口 |

上述完整测试、Nix 与扫描覆盖 Keychain 精确引用删除之前的源码。最后这一处修改只有定向测试和稳定签名包装通过，尚未获得完整验证结果。

## Keychain 当前状态

GUI 创建的正式会话条目可以由 CLI 读取；CLI logout 已撤销远端会话，随后 `SecItemDelete` 返回 -25244。GUI 通过设置中的退出登录成功删除该条目，配置已经清除用户身份，没有保留该条目的空值或隐藏状态。

`Sources/RuriVex/VexSettings.swift` 当前通过 `SecItemCopyMatching` 获取匹配引用，要求结果唯一，再以 `kSecMatchItemList` 执行删除。该修改的定向测试与稳定签名包装通过。尚未执行“GUI 创建、CLI 删除”与“CLI 创建、GUI 删除”的双向实际验证。没有修改 ACL、签名策略或原版 Ruri 条目。

官方依据为 Apple `SecItemDelete` 文档和 TN3137：file-based keychain 查询使用搜索列表，按属性删除默认覆盖全部匹配项；精确引用可以指定同一个条目。当前没有实际 ACL 元数据足以确认 -25244 的最终原因，因此不能将修复标为通过。

## 接续顺序

1. 读取当前 diff 和本记录，确认正式服务状态；独立 8792 验收服务已停止，临时放行规则已移除。
2. 使用最新签名 CLI 发起正式 GitHub 登录，完成用户授权；这一项尚未开始。随后验证 GUI 删除 CLI 创建的实际条目。
3. 通过正式邀请在 GUI 登录，再用签名 CLI 验证实际删除。若 -25244 仍然存在，继续限定于 Vex 条目的真实 ACL/owner 调查；不修改稳定签名或原版条目。
4. 服务端路由验收通过后，用 GUI 进入 example PR #1，记录加入日志和 SLP，正常退出。这是第 6 次玩家启动，随后依次完成 Prism、HMCL 的第 7、8 次验收。
5. 完成当前源码完整测试、必要 Nix 构建、关闭检查、本地化和安全扫描，再决定提交。当前不得提交或推送。

下一条命令先核对源码：

```sh
cd /Users/linwhite/ruri-vex-experimental
git status --short
git diff --stat
git diff --cached --stat
```

正式 CLI 授权命令需要把输出保存到权限 0600 的本地文件；用户码由主任务交给用户，禁止输出 token：

```sh
umask 077
env TMPDIR="$PWD/.work/temp" RURI_DATA_DIR="$PWD/.work/vex-real-it" \
  build/Ruri.app/Contents/Helpers/ruri-cli vex login --github --server encore --json \
  > .work/vex-next-cli-device-private.log 2> .work/vex-next-cli-device-private.error
```

正常启动 GUI：

```sh
env TMPDIR="$PWD/.work/temp" RURI_DATA_DIR="$PWD/.work/vex-real-it" \
  build/Ruri.app/Contents/MacOS/Ruri > .work/vex-next-gui.log 2>&1 &
```

独立 API 验收会话已经通过原有 logout 命令正常撤销，`.work/vex-c-api-session.json` 与 `.work/vex-c-api-token.txt` 已删除。临时授权日志只保存在忽略目录，报告不包含用户码、邀请码或会话令牌。

本机最后只读空间检查约剩余 19 GiB。保留用户 worktree；必要时只清理本任务生成且不再使用的构建缓存和扫描展开文件。
