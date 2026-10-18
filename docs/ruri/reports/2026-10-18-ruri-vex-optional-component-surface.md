# Ruri Vex 按需安装组件：现有接入范围核对

调查日期：2026-10-18。源码目录：`/Users/linwhite/ruri-vex-experimental`。本次读取源码、Git 差异、既有报告和已存在的 debug 应用；没有运行构建、测试、应用或钥匙串操作，没有修改 Ruri 源码、分支和暂存内容。

## 比较基线与文件数量

【实测】当前分支为 `feat/vex-mrpack`，HEAD 为 `61dc1df78cd961714f2034b751940495bea3f4e1`。本机 `upstream/main` 与共同祖先均为 `ebcf5ab663b46e85efbfe12a50a33bbdeb426bec`。比较使用这些本地引用，没有执行 fetch。当前工作目录还包含未提交的 R2 改动。

| 比较范围 | 修改文件总数 | 新增行数 | 删除行数 | 被修改的上游既有文件 |
| --- | ---: | ---: | ---: | ---: |
| `upstream/main` 至 HEAD | 56 | 5,403 | 15 | 14 |
| `upstream/main` 至当前工作目录 | 63 | 6,110 | 15 | 15 |

复核命令：

```sh
git rev-parse HEAD upstream/main
git merge-base HEAD upstream/main
git diff --shortstat upstream/main HEAD
git diff --shortstat upstream/main
git diff --name-status upstream/main
```

【实测】当前 63 个文件包含 15 个上游既有文件和 48 个新增的 Vex 专属文件。48 个文件分别为：Core 的 13 个 Swift 文件及 1 个许可证、GUI 的 6 个 Swift 文件、CLI 的 2 个 Swift 文件、4 个生成的本地化 Swift 文件及 4 个 xcstrings 文件、13 个测试及测试资源文件、5 个指令或报告文件。这里按照 Git 文件清单计数，测试资源和既有报告中的图片仅计数，没有使用视觉功能。

## 上游既有文件的 15 个接入位置

以下路径均相对于 Ruri 源码目录。行号对应本次工作目录；“组件化后的归属”为设计判断，尚未实施。

| 文件与代码位置 | 当前实际改动 | 组件化后的归属 |
| --- | --- | --- |
| `Package.swift:21`、`:29`、`:39`、`:41` | 添加 EventSource 1.4.0；RuriCore 直接依赖其产品；复制许可证；两个测试 target 复制 VexFixtures。 | Vex 源码、依赖、许可证和测试资源可以进入独立组件的构建清单。默认产品仅保留宿主接口需要的依赖。 |
| `Package.resolved:5` | 固定 EventSource 1.4.0，revision 为 `bd64824505da71a1a403adb221f6e25413c0bc7f`。 | 依赖记录可以完全移至组件自己的锁定文件。 |
| `Resources/Info.plist:24` | R2 新增 `ruri` URL scheme，名称为 `dev.ruri.launcher.vex`。 | 若未安装组件时也需要打开链接并展示启用入口，宿主仍需注册 URL 和分发入口；Vex 链接解析可以放入组件。 |
| `Sources/Ruri/Application/Page.swift:4`、`:7`、`:8` | 添加 `vexGit` 页面枚举、标题和图标。 | 保留在原窗口展示页面时，宿主需要通用页面扩展入口；Vex 页面定义可以进入组件。 |
| `Sources/Ruri/Application/RootView.swift:20`、`:75`、`:92`、`:101`、`:102`、`:105`、`:203` | 创建 Vex 状态对象、展示页面、注入环境、处理链接与预览、连接设置变化和禁用清理、增加侧栏。 | 宿主保留组件生命周期、页面容器和 URL 分发；Vex 状态、视图和网络连接逻辑可以进入组件。 |
| `Sources/Ruri/Features/Servers/ServersView.swift:208`、`:209`、`:309` | 根据开关过滤 Vex 服务器，并添加 Vex 房间分区。 | 保留原服务器页面时，宿主需要通用分区与过滤入口；Vex 房间数据和过滤规则可以进入组件。 |
| `Sources/Ruri/Features/Settings/PreferencesView.swift:22` | 直接插入 `VexSettingsSection`。 | 宿主需要安装与启用入口；完整服务器、项目、会话设置可以进入组件。 |
| `Sources/RuriCommandKit/CLIApplication.swift:20`、`:29`、`:49`、`:108`、`:109` | 读取共享设置，检查启用状态，使用 Vex 命令解析入口，分发执行和配置修改。 | 宿主保留组件状态、命令分发和共享配置接口；Vex 命令实现可以进入组件。 |
| `Sources/RuriCommandKit/CommandDiscovery.swift:8` | 将 `VexCLI.commands` 纳入命令发现。 | 宿主需要组件命令元数据的注册入口；具体命令列表可以进入组件。 |
| `Sources/RuriCommandKit/CommandHelp.swift:16`、`:52`、`:56` | 使用 Vex 根命令、子命令列表和帮助文本。 | 宿主保留通用帮助接口；Vex 帮助定义可以进入组件。 |
| `Sources/RuriCommandKit/CommandSchemas.swift:105` | Vex 命令交给 `VexSchemas.payload`。 | 宿主保留通用 schema 分发；Vex schema 可以进入组件。 |
| `Sources/RuriCore/Infrastructure/Persistence/AppSettings.swift:25` | 添加 `ExperimentalSettings?`，类型目前定义在 Vex 源码中。 | 宿主保留组件启用及安装状态；Vex 业务配置可以由组件解释。普通设置写入仍需保存未安装组件的不透明配置。 |
| `Sources/RuriCore/Operations/LaunchService.swift:95`、`:96` | 启用状态下调用 `VexServerPolicy.cleanBeforeLaunch`。 | 宿主保留启动前生命周期接口与文件操作保护；Vex 服务器整理规则可以进入组件。 |
| `Sources/RuriLocalization/Generated/MessageCatalog.swift:216` 至 `:219` | 合并 Vex、VexDevices、VexLogin、VexRooms 四组消息定义。 | Vex 本地化可以进入组件。宿主仅需要组件安装、启停和错误提示的文字。 |
| `scripts/lib/localization-baseline.json:3968` 至 `:4052` | 登记 Vex 翻译的源文摘要。 | 业务翻译校验可以随组件构建迁移；宿主组件管理文字继续使用宿主本地化校验。 |

【代码】GUI target 的声明位置为 `Package.swift:31`，RuriCommandKit 为 `:34`，RuriCLI 为 `:35`。Vex 目录目前位于这些 target 的源码路径中，没有独立的可下载模块边界。

【判断】使用按需 helper 或动态代码 bundle，都需要宿主保留组件启用、安装、真实性校验、版本检查、生命周期和受控能力入口。若保留原 GUI 页面及 `ruri vex` 的使用位置，还需要页面、设置和 CLI 分发接口。独立进程中的 SwiftUI 对象不能直接作为当前 `AppModel` 的环境对象使用；独立进程方案需要明确的 IPC 及宿主渲染接口，或者由组件提供独立窗口。

## EventSource 与二进制实际包含内容

【代码】`Package.swift:21` 将 EventSource 声明为根 Package 依赖，`:29` 让 RuriCore 直接依赖 EventSource。`Sources/RuriCore/Vex/VexEventStream.swift:2` 导入 EventSource，`:62` 使用 URLSession 的字节流，`:69` 通过 `bytes.events` 解析事件。

【代码】现有依赖源码 `.work/build/checkouts/EventSource/Package.swift:18` 使用未指定链接类型的 `.library` 产品；`:24` 声明 `AsyncHTTPClient` trait，`:35` 至 `:38` 将相应产品依赖限定在该 trait。当前宿主清单没有启用这个 trait；当前 `Package.resolved` 也没有 AsyncHTTPClient 及其依赖条目。新增 HTTP 库尚未实施。

【实测】检查对象为现存 `build/Ruri.app`。执行 `nm -U` 仅检查已定义符号，结果如下：

| 文件 | 名称匹配 EventSource 的已定义符号 | 名称包含 Vex 的已定义符号 |
| --- | ---: | ---: |
| `Contents/MacOS/Ruri` | 598 | 7,955 |
| `Contents/Helpers/ruri-cli` | 598 | 6,030 |
| `Contents/Helpers/ruri-monitor` | 0 | 396 |

复核命令：

```sh
nm -U build/Ruri.app/Contents/MacOS/Ruri | rg -c '\$s11EventSource'
nm -U build/Ruri.app/Contents/Helpers/ruri-cli | rg -c '\$s11EventSource'
nm -U build/Ruri.app/Contents/MacOS/Ruri | rg -c Vex
nm -U build/Ruri.app/Contents/Helpers/ruri-cli | rg -c Vex
nm -U build/Ruri.app/Contents/Helpers/ruri-monitor | rg 'EventSource|Vex'
otool -L build/Ruri.app/Contents/MacOS/Ruri
otool -L build/Ruri.app/Contents/Helpers/ruri-cli
```

【实测】GUI 和 CLI 的 `otool -L` 清单没有 EventSource 动态库；两者都存在 EventSource 的已定义符号。当前产物中的 EventSource 代码通过静态链接进入 GUI 和 CLI。该结论限定于已检查产物，SwiftPM 清单本身允许工具链选择链接方式。

【实测】monitor 的 396 个匹配符号全部属于 `RuriLocalization`；没有找到 EventSource 或 Vex Core 的已定义符号。其来源与 `Package.swift:26` 统一处理本地化资源、`:36` 依赖 RuriLocalization 一致。

【实测】当前应用资源包含：

- `Ruri_RuriLocalization.bundle/Contents/Resources/{en,zh-Hans}.lproj/` 下的 `Vex.strings`、`VexRooms.strings`、`VexDevices.strings`、`VexLogin.strings`，合计 8 个文件。
- `Ruri_RuriCore.bundle/Contents/Resources/EventSource-LICENSE.txt`。
- `Ruri_RuriCoreTests.bundle/Contents/Resources/VexFixtures`。
- `Ruri_RuriCLITests.bundle/Contents/Resources/VexFixtures`。

复核命令为 `find build/Ruri.app/Contents/Resources \( -name '*Vex*' -o -name '*EventSource*' \) -print`。`scripts/build-app.sh:49` 至 `:51` 会复制当前构建输出目录内所有 `.bundle`。本次确认的是已有 debug 应用，未据此推断每次干净 release 构建都会包含测试资源。

【代码】`VexSettings.swift:11` 默认关闭，`:20` 在字段缺失时仍取 false。`VexPresentation.swift:201`、`:202` 在关闭状态执行 stop 后返回，`:196` 至 `:199` 取消现有任务。`VexCommands.swift:18`、`:19` 按开关提供命令，`:20` 至 `:28` 为 CLI 读取共享设置。`RootView.swift:20` 仍直接创建 VexPresentation。

【判断】这些开关控制运行行为。已编译的 Vex 和 EventSource 代码、本地化、许可证仍存在于默认应用文件中。实现默认包完全不含组件，需要调整源码 target、依赖解析及资源打包边界。

## 必须通过宿主接口处理的能力

【代码】`Sources/Ruri/Features/Vex/VexSettingsSection.swift:11`、`:12` 直接读取 AppModel 和 VexPresentation；`VexPresentation.swift:219` 的加入流程接收 AppModel。这些类型与当前主程序内部状态直接耦合。

【代码】当前 R2 的 `VexInstanceManager.swift:92`、`:98` 调用 MRPack 准备及更新，`:102` 执行安装。`VexMRPack.swift:62`、`:104` 使用现有导入流程与内部成员。移动到独立编译模块时，需要提供公开、明确且可校验的宿主操作接口。

【代码】`LaunchService.swift:61` 调用已有 AccountService 的账号认证。Vex 的游戏启动最终复用这条宿主流程。账号身份、MRPack 文件事务、实例管理、实际启动和游戏会话管理可以由宿主继续提供；组件传递项目、房间、generation、构建身份、本地包及文件校验要求。

【代码】`VexServerPolicy.swift:17` 说明调用方需要持有 GameRunLease；`:18` 至 `:41` 执行托管标记、路径检查及服务器列表整理。组件化时需要继续明确该回调所处的租约和启动阶段，不能将业务整理规则直接搬入缺少宿主生命周期约束的任意后台任务。

【代码】`VexSettings.swift:81` 至 `:85` 使用独立 API 凭据服务名 `dev.ruri.launcher.vex.api`；后续调用 SecItemUpdate、SecItemAdd、SecItemCopyMatching 和 SecItemDelete。当前设计没有单独组件身份或跨进程凭据协议。

【判断】Vex API、SSE、登录、项目与房间模型、提交图、加入判定、实例映射、文件指纹、页面、命令、翻译以及 EventSource 和新增 HTTP 依赖均可以进入可选组件。宿主接口应避免传递 Microsoft 凭据，并限制正常调用所能执行的文件操作。进程内原生 bundle 与宿主共享进程权限；接口约定本身无法构成独立的系统安全边界。发布签名和完整性验证用于确认可信代码来源，宿主能力设计仍需单独审阅。

## 签名、打包与上游基础设施差异

【实测】对 `.github`、GameHost 配置与权限、证书、签名和打包脚本执行限定路径的 `git diff upstream/main --name-only -- <路径>`，没有输出。对原有 SoftwareUpdater、Microsoft Authentication、CurseForgeKeyStore、LauncherPaths、CLIInstallation、LocalizationContext、RuriApp、AppModel 和 localization-exceptions.json 执行相同检查，也没有输出。

【实测】本次核对中，既有签名、打包、应用身份、账号目录、自动更新和 CLI 安装实现的差异文件数量为 **0**。当前 `Resources/Info.plist` 的差异是前文列出的 R2 URL scheme，没有添加 ATS 例外。

【代码】`scripts/build-app.sh:5`、`:6` 调用现有 signing.py；`:23` 至 `:25` 复制 GUI、CLI 和 monitor；`:35` 签名 monitor；`:39` 为 CLI 保留 `dev.ruri.launcher` 标识；`:42` 至 `:47` 打包并签名 Sparkle；`:54` 至 `:58` 签名并验证整个应用。`scripts/lib/build.sh:65` 至 `:68` 的 GameHost 签名逻辑保持上游内容。`scripts/release/signing.py:129` 与 `:180` 保留既有 contributor 与固定证书要求。

【实测】`codesign -dvv build/Ruri.app/Contents/MacOS/Ruri` 显示 Identifier 为 `dev.ruri.launcher`、Signature 为 `adhoc`、TeamIdentifier 为 `not set`、架构为 arm64。现存 CLI 同样使用 ad hoc 签名。本次没有读取签名私钥或钥匙串内容。

【判断】可选组件需要新增独立的构建、签名、版本清单、下载校验与发布流程。当前仓库没有可下载原生组件的安装器或加载协议。已有宿主签名和打包流程继续承担宿主发布；组件安装目录、发布者身份、动态代码加载条件和更新兼容性必须完整定义后实际验证。

## 与 P1/R1 已有报告的关系

【文档】Ruri 的 `docs/reports/2026-10-17-ruri-vex-r1.md:119` 记录关闭状态的四组上游 CLI golden 比较，`:120` 记录上游脚本 debug 打包，`:134` 起列出 P1 的 14 个既有文件，`:191` 记录当时的 ad hoc 签名。该文件的测试与状态记录对应各自执行时间；本次没有重复执行这些测试。

【代码与实测】本报告新增的是当前 R2 工作目录的 15 个接入文件核对、现有二进制和资源包含情况，以及组件边界分析。运行时关闭行为通过的既有证据，不能单独证明默认产物排除了组件；本次二进制检查已经确认默认应用仍包含这些代码。

【范围】本次没有实现组件拆分、helper、动态 bundle、组件安装器或新的 HTTP 依赖。组件体积、加载耗时、额外内存、发布签名接受条件和安装更新行为没有本次测量结果，需要在对应实现完成后进行实际验收。
