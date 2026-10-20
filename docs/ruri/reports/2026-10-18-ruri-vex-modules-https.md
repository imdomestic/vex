# Ruri 独立模块与 HTTPS 验收

工作目录：`/Users/linwhite/ruri-vex-experimental`；分支：`feat/vex-mrpack`。用户于 10 月 20 日 16:47 JST 要求保存进度并暂停；源码已经暂存，没有提交或推送。应用、编译和授权轮询均已结束。上游已读取到 `8bd487882de5faf4e09c1a2320d78b3700f2e3a3`。

## 实施与验证

| 项目 | 当前结果 | 证据 |
| --- | --- | --- |
| 三个业务 target | `RuriVex`、`RuriVexUI`、`RuriVexCLI`；业务资源和测试数据均在各自目录中 | `Package.swift`、`Sources/RuriVex*`、`Tests/RuriVex*` |
| 宿主接口 | 通用配置、页面/设置/服务器分区、URL、CLI、启动前回调、实例导入/更新、启动取消与会话查询；业务模块不访问 AppModel、StateStore、InstanceService、InstanceTransfer 或 LaunchService | `RuriCore/Experimental/`、`ExperimentalUI.swift`、`ExperimentalCommands.swift` |
| 一处注册 | `CompiledExperimentalFeatures.register()` 中一行注册三个提供者 | `Sources/RuriFeatureRegistration/CompiledExperimentalFeatures.swift` |
| 宿主具体类型检查 | 宿主 Swift 文件没有 Vex 业务类型引用；上游 Minecraft 模组数据库含有游戏实体名称 Vex | `rg` 检查宿主目录；数据库属于上游现有内容 |
| 启用构建 | 10 月 20 日完整套件 638 项通过，联网测试按环境变量执行；最后 Keychain 精确引用删除修改已完成定向测试与稳定签名包装，尚待完整检查 | `.work/vex-oct20-enabled-device-tests.log`、`.work/vex-oct20-keychain-reference-tests.log`、`.work/vex-oct20-keychain-reference-package.log` |
| 关闭构建 | 10 月 20 日 593 项完整测试通过；debug 打包、资源搬移和 CLI 验证已通过 | `.work/vex-oct20-disabled-tests.log`、`.work/vex-c-disabled-package-capabilities.log` |
| 关闭产物检查 | 四份 CLI golden 逐字节一致；依赖图、包内路径、Info.plist、Mach-O 符号/可读字符串/链接库均无 Vex 业务组件与 EventSource；未创建数据目录。通用 URL handler 为 dev.ruri.launcher.urls | `.work/vex-oct20-disabled-artifact.log`、`.work/verify-disabled.py` |
| 运行时关闭检查 | 启用组件的实际 CLI 在 LLDB 下执行四份 golden；输出逐字节一致，未创建数据目录；真实网络、钥匙串和 SSE 入口的调用次数均为零 | `.work/vex-c-runtime-disabled-capabilities.log`、`.work/runtime-disabled-observation/result.json` |
| 本地化 | 4048 条资源；简体中文与英文完整；模块独立资源继续执行上游校验 | `python3 scripts/localization.py --check` |
| Python 工具测试 | 38 项通过 | `.work/vex-c-python-tests.log` |
| Nix | 使用上游固定 nixpkgs 和 Swift 6.2.4；10 月 20 日两种构建均通过，包含 UI 生命周期、通用 URL handler 与实际截止时间判断。最后 Keychain 修改尚待 Nix 构建 | `.work/vex-oct20-nix-{enabled,disabled}-final.log` |
| HTTPS | 正式地址与独立第二实例均已由 GUI 使用邀请登录；第二实例 SSE 断开、自动恢复和会话撤销提示通过 | 真实 HTTPS、AX 语义查询、第二实例管理 socket |
| 安全扫描 | 两个 feature 的完整祖先和工作区扫描通过：327 个提交、4217 个历史对象、913 个文件、6 个归档成员及六种编码字符串，零凭据。最后 Keychain 修改尚待增量扫描 | `.work/vex-source-audit-20261020T071920Z/review.json` |

完整启用测试包含托管文件三种摘要校验、相同内容不同文件名、用户文件保留、用户编辑冲突、实际 MRPack 解包与 `servers.dat` 排除、加入状态机、SSE 录制数据、CLI golden。后端联网项目需要明确环境变量才能运行，离线测试不会创建假后端。

运行时关闭的观察使用真实已打包 CLI，断点覆盖 `SecItemCopyMatching`、`SecItemAdd`、`SecItemUpdate`、`SecItemDelete`、`connect`、`connectx` 和 `VexEventStream.events`。每份 golden 的全部断点均解析成功且命中次数为零；调试器没有替换任何依赖。构建关闭检查与运行时关闭检查使用不同产物，证据分别保存。

## 构建开关与工具链

当前使用 manifest 条件 `RURI_ENABLE_VEX=0` 关闭业务组件，默认启用。关闭时 Package.swift 不声明业务 target、业务测试或 EventSource 依赖；注册模块使用 `RURI_VEX_ENABLED` 编译条件。

SwiftPM traits 已在 Xcode Swift 6.3.3 和 Nix Swift 6.2.4 上实际检查。指定宿主 product 的关闭构建可以成功，依赖解析结果会移除 EventSource；完整 `swift test --disable-default-traits` 仍然编译独立 OptionalFeature target，并报出 `no such module 'EventSource'`。因此按 C3 授权采用 manifest 编译条件，使完整构建和完整测试使用相同组件集合。原始日志位于 `.work/traits-probe-disabled-tests.log`、`.work/vex-c-nix-traits-test.log`；探针只使用真实 SwiftPM target 和 EventSource 依赖。

```sh
env RURI_ENABLE_VEX=0 RURI_BUILD_DIR=.work/build-disabled \
  TMPDIR="$PWD/.work/temp" scripts/swift-build.sh test \
  --build-system swiftbuild --no-parallel --enable-swift-testing
env TMPDIR="$PWD/.work/temp" \
  ~/.local/bin/ruri-build-local debug --build-system swiftbuild
```

Nix 导出 `default`、`ruri-launcher`、`ruri-without-experiments`，后者使用 `enableVex = false`。Nix 的构建目录明确设置为守护进程管理的 `/nix/var/nix/builds`。主目录权限保持不变。

## 宿主目录中的改动

| 文件或目录 | 通用职责 |
| --- | --- |
| `RuriCore/Experimental/ExperimentalFeatures.swift` | 任意实验功能的可编码配置与启用后回调注册 |
| `RuriCore/Experimental/ExperimentalConfigurationStore.swift` | 读取和修改指定实验功能的配置，不向组件返回完整应用状态 |
| `RuriCore/Experimental/ExperimentalInstanceCapabilities.swift` | 实例文件校验、MRPack 导入/更新、QuickPlay 参数和隐藏服务器条目清理 |
| `RuriCore/Experimental/ExperimentalLaunchCapabilities.swift` | 通过宿主启动游戏、取消启动、按实例查询实际会话；账号状态仅提供布尔值 |
| `RuriCore/Operations/LaunchService.swift` | 发起启动前调用已启用扩展的回调 |
| `RuriCore/Infrastructure/Persistence/AppSettings.swift` | 持久化通用实验配置 |
| `RuriCore/Instances/Transfers/InstanceTransfer.swift` | MRPack 准备时支持调用方排除文件 |
| `RuriExtensionKit/ExperimentalUI.swift` | 通用 UI 提供者、注册表与账号状态/启动能力；不提供 Microsoft 凭据 |
| `Ruri/Application/ExperimentalHost.swift` | 将 AppModel 的公开操作绑定给通用宿主能力 |
| `Ruri/Application/Page.swift`、`RootView.swift` | 扩展页面与 URL 路由 |
| `Ruri/Features/Servers/ServersView.swift` | 注册的服务器分区与过滤器 |
| `Ruri/Features/Settings/PreferencesView.swift` | 注册的设置分区 |
| `Ruri/Application/RuriApp.swift`、`RuriCLI/CLI.swift` | 应用与 CLI 启动时调用统一注册入口 |
| `RuriCommandKit/ExperimentalCommands.swift` | 任意扩展的 CLI 发现、帮助、schema、配置和执行协议 |
| `RuriCommandKit/CLIApplication.swift`、`CommandDiscovery.swift`、`CommandHelp.swift`、`CommandSchemas.swift` | 接入通用 CLI 提供者 |
| `RuriCommandKit/CommandProtocol.swift`、`CommandOutput.swift` | 向同一 package 内的命令模块提供必要类型和输出操作 |
| `RuriLocalization/LocalizationCatalogRegistry.swift`、`LocalizationContext.swift`、`LocalizationResources.swift`、`LocalizationCommandLine.swift` | 注册模块资源、按表查找和独立资源校验 |
| `scripts/localization.py`、`scripts/lib/localization.py`、`scripts/lib/localization_catalog.py` | 按模块读取和验证 String Catalog；保持全部翻译与格式检查 |
| `Package.swift`、`Sources/RuriFeatureRegistration/` | 构建组件选择与唯一组合位置 |
| `Package.resolved`、`.gitignore` | 固定依赖解析结果、忽略 Nix 生成目录 |
| `flake.nix`、`flake.lock`、`nix/` | 上游源码打包方式和两个组件配置 |
| `SoftwareUpdater.swift`、`GameRunDirectoryChangeView.swift`、`LocalContentMetadataCache.swift`、两个 Info.plist | 上游 8bd4878 的 Nix 构建相关调整 |

Vex UI 不引用 AppModel；业务模块通过通用能力读取指定实验配置、查询实例、准备和安装 MRPack、设置 Java 要求、启动游戏及查询会话。MRPack 的内部字段与 ModpackRegistry 写入操作集中在 Core 的通用能力实现中。Microsoft 账号和凭据仍由宿主启动流程处理。真实临时状态和会话数据库测试验证配置隔离、实例身份、取消启动以及错误实例查询。

## R2 功能

- GitHub 登录采用 `POST /v2/auth/device {}` 和 `/poll {handle,client}`；GUI 显示用户码、复制和打开 GitHub 操作，退出弹窗取消轮询。CLI 提供 `vex login --github`；文本模式尝试打开浏览器。错误码 `expired_token`、`access_denied`、`account_not_linked` 有各自提示。
- Git 页显示 Pull Requests、作者、fork 标记、预览房间与当前 head 的 Build 状态。CLI 提供 `vex pulls`。PR 房间继续通过 start 和租约加入。
- 准备使用真实上游 MRPack/ModpackUpdater，并固定 `buildId` 与 `generation` 查询条件；包的 `versionId` 必须精确等于 `<room>-g<generation>-<build>-a<attempt>`。托管文件三种摘要匹配、内容集合与 clientFingerprint 一致时，跳过包下载和同步。托管文件改动与覆盖冲突立即失败；非托管文件保留。
- GUI 只接受 HTTPS。CLI 的 HTTP 地址限制为 localhost、127.0.0.0/8 和 IPv6 loopback。Info.plist 没有添加 ATS 例外，没有引入 AsyncHTTPClient。

## 决策依据与签名事实

用户选择同仓库静态模块的四项理由：保持上游 Nix 的源码构建方式；控制组件加载所需的签名维护；避免添加运行时下载并执行组件的入口；保持 Ruri 内嵌页面并减少需要长期维护的 IPC 协议。

上游默认 contributor debug 使用 ad hoc 签名。本机验收应用通过 `~/.local/bin/ruri-build-local` 调用原构建脚本，并按上游证书分支的 flags/entitlements 由内向外使用 Apple Development、Team `59ZC6MZ73T` 签名。主程序与 CLI 使用完全相同、包含 Team OU 的 designated requirement。八个组件及 deep strict/all-architecture 检查通过；连续两次重建后的 `vex whoami`、`account list` 和指定验收账号的 Microsoft refresh 均成功。锁屏期间 `vex whoami` 用时 1.214 秒，`account list` 用时 0.340 秒，前后 IOConsoleLocked 均为 true，未出现 SecurityAgent。完整记录位于 `~/worklog/ruri-local-signing/README.md`，下载目录副本为 `RURI-LOCAL-SIGNING.md`。仓库中的签名脚本保持上游内容。

此产物已实际运行 MC 26.3 到主菜单：GameHost 为 `flags=0x10000(runtime)`，日志记录 `TitleScreen::extractRenderState` 的 JIT 编译、OpenGL native 方法和 SDL_GL_SwapWindow；运行 159.30 秒后通过原生窗口关闭按钮正常退出，exit code 为 0。临时 PrintCompilation 与 PerfDisableSharedMem 参数已经恢复继承，证据为 `.work/vex-c-title-evidence.json`。当前源码的 CLI 正常退出条件仅识别 GLFW，因此这次使用原生窗口操作；独立 SDL 修复分支由主任务管理。

EventSource 版本为 1.4.0，许可证为 MIT。源码目录占用 44 KiB（`du -sk .work/build/checkouts/EventSource/Sources`）；许可证随 `Ruri_RuriVex.bundle` 分发。当前 debug 应用的实际磁盘占用分别为启用 163008 KiB、关闭 153636 KiB，相差 9372 KiB；差值包括三个业务模块、资源和 EventSource，无法单独归因于 EventSource。

## 真实验收范围

正式 HTTPS 地址为 `https://vex.inner.imdomestic.com:8790`。隔离目录为 `/Users/linwhite/ruri-vex-experimental/.work/vex-real-it`。正式服务器与 `https://vex.inner.imdomestic.com:8792` 独立实例均已在 GUI 完成邀请登录。临时实例停止后，仅其分组出现“实时更新已断开，将自动重新连接”；服务恢复后提示自动消失；通过该实例的管理 socket 撤销会话后，GUI 自动显示“登录已失效”。全过程使用明确 PID 的 AX 属性与动作，未使用视觉定位。

10 月 20 日正式 GUI 的 GitHub Device Flow 已成功，CLI whoami 确认 linwhite/admin。退出验收中，GUI 创建条目可由 CLI 读取，CLI 删除返回 -25244；GUI 已成功删除该条目并清除登录状态。当前正式 `encore` 为未登录，隔离 Microsoft 登录保留。`VexSettings.remove` 已改为查询唯一条目引用并通过 `kSecMatchItemList` 删除，编译与稳定签名包装通过，双向真实删除等待接续；没有修改 ACL、签名配置或原版 Ruri 条目。

独立 8792 的真实 GitHub 授权返回账号未关联；客户端 `github-unlinked` 条目没有用户身份。另一个未授权请求实际等待 888.008 秒并到达 expiresAt 后返回超时；取消验证也已通过。证据为 `.work/vex-oct20-secondary-device-expiry.log` 与 `.work/vex-oct20-device-integration.log`。正式 Ruri CLI 的 GitHub 授权尚未开始。

正式 P2 已部署。真实 Swift API/SSE 测试通过；chorus/main 的固定 generation 12 包准备耗时 11.48 秒，example/main 的固定 generation 3 包准备耗时 5.31 秒。测试核对了实例、运行时、QuickPlay 地址和服务端房间状态保持不变。关闭实验组件的 Ruri 在独立 `.work/upstream-mrpack-it` 目录实际导入 example 包，结果为 installed=true、MC 26.3、Fabric 0.19.5、3 个文件、零警告。证据分别为 `.work/vex-c-real-p2-https-api.log`、`.work/vex-c-real-p2-prepare-{chorus,example}.log`、`.work/vex-c-upstream-mrpack-import.json`。三个正式包及来源摘要位于 `.work/vex-c-real-packs/`。

Prism 11.1.1 在独立 `.work/prism-mrpack-it` 完成同一 example 包的原生导入。`mmc-pack.json` 为 MC 26.3/Fabric 0.19.5，两个 jar 和 `servers.dat` 与包内文件逐字节相同；证据为 `.work/vex-c-prism-import.log` 和 `.work/vex-c-prism-verification.json`。Prism 与 HMCL 的 Microsoft 登录和 PR generation 2 包导入均已完成，真实游戏等待统一窗口。

包含独立 SDL 验收在内，已核实当前真实游戏启动次数为 5/8，P2 GUI PR 尚未启动玩家。实际 Git 页面完成固定目标 MRPack 准备，并显示共享进度与取消控件；取消后连接状态保持正常。原生 AX 与一次有界 System Events 遍历完成，随后主线程采样 275/275 均等待正常事件，没有持续布局；尚未运行 VoiceOver。用户已授权唯一验收账号 `8D8D27B5-9A5A-4779-B36D-EB65EAE35A0B` 使用默认 Ruri 支持目录中的 `.account-locks` 锁文件；账号和实例数据继续使用隔离目录。

用户指定的命令行入口为 `/usr/local/bin/ruri`，指向 `/Users/linwhite/ruri-vex-experimental/build/Ruri.app/Contents/Helpers/ruri-cli`。安装 receipt 与链接一致，版本为 0.11.0；`cli status` 返回 installed、owned、onPath 均为 true。
