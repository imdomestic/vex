# Ruri Vex 按需组件代码边界调查

调查目录：`/Users/linwhite/ruri-vex-experimental`。P1 提交为 `61dc1df78cd961714f2034b751940495bea3f4e1`；当前 `feat/vex-mrpack` 工作区包含尚未提交的 R2 文件。下文标注【代码】、【实测】和【判断】，行号对应当前工作区。调查没有修改源码，没有增加 Swift 依赖。

## 当前默认关闭的范围

| 事实 | 依据 |
|---|---|
| 【代码】EventSource 1.4.0 是 Package 的固定依赖，并直接链接到 RuriCore；全部 Vex Core 文件与普通启动器功能属于同一个 target。 | `Package.swift:21`、`:29` |
| 【实测】当前 debug 主程序包含 8607 个名称涉及 Vex 或 EventSource 的符号；动态依赖表没有独立 RuriCore/EventSource 动态库。 | `nm -j build/Ruri.app/Contents/MacOS/Ruri \| rg -c 'Vex\|EventSource'`；`otool -L build/Ruri.app/Contents/MacOS/Ruri` |
| 【代码】关闭开关时不创建服务器连接，并取消已有连接和加入任务；RootView 仍创建一个空的 VexPresentation 对象。 | `Sources/RuriCore/Vex/VexSettings.swift:11`；`Sources/Ruri/Features/Vex/VexPresentation.swift:196`、`:201`；`Sources/Ruri/Application/RootView.swift:20` |
| 【代码】CLI 根据开关选择 RuriCommand 或 VexRootCommand，隐藏 Vex 命令和 schema。解析与执行代码始终编译在 ruri-cli 中。 | `Sources/RuriCommandKit/VexCommands.swift:18`、`:19`；`CLIApplication.swift:20`、`:49`、`:108` |
| 【代码】Vex 字符串和 EventSource 许可证随默认应用打包。 | `Package.swift:26`、`:29`；`Sources/RuriLocalization/Generated/MessageCatalog.swift:216`；`scripts/build-app.sh:49` |

【判断】用户要求的“未启用时没有组件”涉及构建产物和分发边界。当前运行时开关已有停止网络行为的作用；仅延迟创建 HTTPClient 无法移除已进入默认二进制的业务、UI、CLI 和依赖。

## 已存在的宿主接入

P1 相对上游 `ebcf5ab663b46e85efbfe12a50a33bbdeb426bec`，Vex 专属目录之外涉及以下 14 个接入文件；R2 工作区增加 `Resources/Info.plist`，合计 15 个。计数按 R1 的接入文件范围，排除 Vex 专属测试、资源文件与报告。【实测】命令：`git diff --name-only ebcf5ab663b46e85efbfe12a50a33bbdeb426bec HEAD`，并检查当前暂存文件。

| 位置 | 当前耦合 | 按需化时的宿主职责【判断】 |
|---|---|---|
| `Package.swift:21`、`:29`；`Package.resolved` | 默认构建解析并链接 EventSource | 默认产品排除 Vex target 和依赖；组件单独构建、解析和发布 |
| `Sources/Ruri/Application/Page.swift:4` | Page 枚举包含 vexGit | 提供通用扩展页面注册与选择 |
| `Sources/Ruri/Application/RootView.swift:20`、`:75`、`:92`、`:203` | 直接构造 Vex 状态、页面和环境，显示专属侧栏 | 提供组件生命周期、页面容器和状态订阅 |
| `Sources/Ruri/Features/Servers/ServersView.swift:208`、`:309` | 调用 Vex 服务器过滤与房间分区 | 提供服务器分区扩展和通用过滤入口 |
| `Sources/Ruri/Features/Settings/PreferencesView.swift:22` | 直接构造 VexSettingsSection | 提供组件安装、启停、状态及组件设置容器 |
| `Sources/RuriCore/Infrastructure/Persistence/AppSettings.swift:25` | Codable 设置直接引用 ExperimentalSettings/VexSettings | 宿主保存通用组件启用信息；组件拥有其具体配置结构 |
| `Sources/RuriCore/Operations/LaunchService.swift:95` | 启动普通流程直接调用 VexServerPolicy | 提供受约束的实例启动前检查与文件整理入口 |
| `Sources/RuriCommandKit/CLIApplication.swift:20`、`:108`；`CommandDiscovery.swift:8`；`CommandHelp.swift:16`；`CommandSchemas.swift:105` | CLI 注册、帮助、schema 和执行直接依赖 Vex 类型 | 提供已安装组件的命令发现和执行协议；关闭时保持上游输出 |
| `Sources/RuriLocalization/Generated/MessageCatalog.swift:216`；`scripts/lib/localization-baseline.json` | 主应用聚合专属字符串与资源摘要 | 组件使用自己的资源包与本地化校验，宿主只保留通用组件管理文本 |
| R2 `Resources/Info.plist:25` 与 `RootView.swift:93` | 注册 ruri scheme，并直接解析 Vex PR 链接 | 宿主注册通用入口，再按已安装组件路由；未安装时显示安装确认 |

【判断】默认应用需要保留这些职责对应的通用入口，才能在用户启用后安装组件、展示页面、发现 CLI 命令并调用启动器能力。具体 Vex 网络协议、项目模型、页面和命令可由下载组件提供。删除全部入口会使默认应用失去安装和使用该组件的能力。

## 业务与凭据边界

| 能力 | 可移至下载组件的内容 | 需要跨边界保留的内容 |
|---|---|---|
| Vex API 与 SSE | 【代码】`VexClient.swift:12` 的请求、`VexEventStream.swift:54` 的重连、`VexModels.swift` 的模型、登录与每服务器会话缓存 | 【判断】向宿主提供项目/房间状态、进度和明确错误；关闭后取消全部任务 |
| Vex 凭据 | 【代码】`VexSettings.swift:81` 的 `dev.ruri.launcher.vex.api` Keychain 条目按规范化 URL 保存 | 【判断】Keychain 调用继续由主程序与 ruri-cli 执行；下载组件通过受限宿主服务取得所属服务器的 Vex 会话。Microsoft 凭据继续由宿主 LaunchService 读取 |
| 加入判定 | 【代码】`VexLaunchCoordinator.swift:16` 与 `VexJoinGate.swift` 合并 ready、本地准备、generation/build 和单次启动条件 | 【判断】组件可持有这套判定；请求宿主启动时需携带明确实例、服务器、房间和加入操作身份，取消与启动的先后关系必须保留 |
| 存储身份 | 【代码】`VexInstanceManager.swift:18` 使用服务器 URL、项目和 runtime 组成实例键；`:52` 定义实例标记 | 【判断】组件可拥有映射与标记，宿主提供受约束的实例操作；避免组件与 GUI 同时任意重写 StateStore |
| 服务器清理 | 【代码】`LaunchService.swift:95` 调用启动前清理，`ServersView.swift:209` 过滤通用列表 | 【判断】两个入口均需通用宿主协议；仅把 Vex 页面下载到外部仍会保留这些直接耦合 |

## SwiftUI 和 AppModel

【代码】`Sources/Ruri/Application/AppModel.swift:7` 是 executable target 内的 internal `@MainActor @Observable final class`。它持有 PersistentState、账户、实例、操作和页面状态；它没有作为独立插件接口导出。`VexSettingsSection.swift:10`、`VexGitView.swift`、`VexRoomsSection.swift:7` 都直接使用 `@Environment(AppModel.self)`。`VexPresentation.swift:219` 接收 AppModel，检查账号并调用 LaunchService。`git diff` 确认 AppModel 文件本身相对上游没有改动。

【判断】现有 SwiftUI 文件不能直接编译成外部 bundle 并继续依赖宿主内部 AppModel。需要将它们依赖的能力缩为通用宿主接口：只读账号是否可用、实例导入/更新、启动与取消、打开登录界面、通知和错误显示。组件自己的 SwiftUI 状态可以继续使用 Observation。

【代码】当前源码中没有 `NSXPCConnection`、`Bundle.principalClass` 或通用代码 bundle 加载协议；已有 Bundle 使用主要负责本地化资源。`Sources/RuriGameHost/main.m:61` 的 `dlopen` 用于加载 JVM 的 JLI 动态库。搜索：`rg -n 'NSXPC|dlopen|principalClass|loadAndReturnError' Sources`。现有 ProcessRunner 用于执行程序，未提供组件生命周期和双向调用协议（`Sources/RuriCore/Java/JavaRuntime.swift:26`）。

## MRPack 的可复用范围

【代码】上游 `InstanceTransfer.prepare/install` 和 `ModpackUpdater.prepare/apply` 已有公开入口：`Sources/RuriCore/Instances/Transfers/InstanceTransfer.swift:163`、`:169`、`:240`；`Sources/RuriCore/Modpacks/ModpackUpdater.swift:4`、`:9`。MRPack 解析仍在原有 `Modpacks/MRPack.swift:27`；文件路径、SHA1/SHA512 和 runtime 检查位于 `:30` 至 `:62`。这些上游文件相对基线没有功能修改。

【代码】当前未提交的 R2 调用还依赖 RuriCore 内部成员：`PreparedInstanceImport.packFiles/game/workspace/modpack`（`InstanceTransfer.swift:26`、`:52`）；`ModpackRegistry.save`（`ModpackRegistry.swift:68`）；`GameQuickPlay`（`Launch/LaunchDestination.swift:4`）。使用点为 `VexMRPack.swift:62`、`:69`、`:104` 和 `VexInstanceManager.swift:169`。因此，把现有 Vex 文件移动到另一个 target 后会遇到访问权限错误。

【判断】MRPack 导入与更新可以留在宿主，组件提交本地 mrpack、目标实例、严格文件校验要求，并获得不暴露内部目录的操作结果与进度。如此可以复用上游现有文件事务和锁。若让独立 helper 直接拥有完整 RuriCore 副本，还需要处理两个进程的状态版本、路径和文件写入协调，不能依靠共享 Codable 文件自动获得一致性。

## A：按需完整原生 bundle

【判断】Vex 的 GUI、CLI 命令、Core、EventSource、HTTP 传输实现和本地化资源全部放入单独构建、签名和下载的组件。主程序保留通用组件管理与嵌入容器。安装并启用后，服务器分区、Git 页面和设置继续显示在 Ruri 主窗口。默认 Package 的依赖声明也要移除 EventSource，组件通过独立 Package 管理自己的依赖，才能同时满足默认应用和默认构建过程均没有完整 Vex 组件。

| 宿主接入 | 必要接口【判断】 | 现有代码依据 |
|---|---|---|
| 原生页面 | 一个明确版本的 Objective-C 或 C 加载入口，由组件返回 NSViewController；组件在控制器内承载自己的 SwiftUI，宿主使用通用容器嵌入。接口不传递内部 AppModel 或依赖其布局的 Swift 类型 | `AppModel.swift:7`；`VexSettingsSection.swift:10`；`RootView.swift:75` |
| 宿主能力 | 只读账号可用状态、打开登录界面、导入与更新 MRPack、查询托管实例、准备与启动游戏、取消操作及进度事件；实例和账号操作由现有宿主服务执行 | `VexPresentation.swift:219`；`LaunchService.swift:34`、`:61`、`:106`；`InstanceTransfer.swift:163` |
| CLI 命令 | 通用扩展命名空间、版本化命令描述与执行入口；组件提供参数、帮助、schema 和结果。现有具体 ParsableCommand 类型的静态注册需要替换为通用分发接口 | `CLIApplication.swift:49`、`:108`；`CommandDiscovery.swift:8`；`CommandSchemas.swift:105` |
| 服务器列表与启动前整理 | 宿主允许组件登记服务器条目过滤规则、实例关联与启动前操作，并控制适用的实例范围；Vex 的 zone 与房间规则留在组件 | `ServersView.swift:208`、`:309`；`LaunchService.swift:95` |
| 配置与资源 | 宿主只存储通用组件安装状态与启用状态；组件保存服务器配置、Vex 字符串和专属资源。URL 路由经通用入口交给组件，并保留未启用时的用户提示 | `AppSettings.swift:25`；`MessageCatalog.swift:216`；`RootView.swift:93` |

【判断】动态 bundle 进入主程序或 ruri-cli 的进程，Vex Keychain 访问主体仍是实际执行调用的宿主进程。组件可以保持 URL 作为凭据键的规则；GUI 与 CLI 的代码签名身份仍须满足现有钥匙串访问要求。加载第三方代码进入同一进程也意味着它具有该进程的实际权限；“受约束的宿主接口”用于控制数据与功能耦合，无法单独构成进程权限隔离。因此组件来源、完整性与代码签名检查是执行前的必要条件。当前实际构建采用上游允许的 contributor ad hoc 路径，不能据此认定后续组件已经具备稳定的发布签名身份（`scripts/release/signing.py:108`、`:128`、`:170`）。

【判断】在启用状态下，bundle 内部可直接维持当前 ready/generation/build、本地准备和单次启动判定。关闭时必须取消全部网络与加入任务，并移除页面和命令入口。SwiftUI 对象或异步任务仍引用代码时，不能承诺立即卸载动态代码；文件删除和版本替换需要遵循实际生命周期。宿主与组件需要显式兼容版本，现有 RuriCore 内部成员不能充当稳定的动态接口。

【判断】把现有 URLSession 代码装入 bundle 后，它仍运行在主程序进程，正式 HTTP FQDN 的 ATS 限制依然存在。允许显式配置 HTTP 的专用传输库可以全部归属下载组件，默认宿主无需携带该依赖；AsyncHTTPClient 当前仍待授权，没有实施。

## B：按需独立伴随应用

【判断】Vex GUI、Core、CLI、EventSource、HTTP 传输实现和资源全部位于独立下载的应用中，Vex 的服务器、Git 和登录页面使用独立窗口。Ruri 保留通用组件安装入口、启动该应用的入口，以及供组件调用的宿主能力服务。默认 Ruri 的服务器与设置页面不再构造 Vex 专属 View 或状态对象；需要入口的位置只显示通用组件入口。

| 边界 | 责任【判断】 | 现有代码依据 |
|---|---|---|
| UI 与连接 | 伴随应用拥有服务器分组、项目/房间页面、邀请和 GitHub 登录、SSE、连接状态、加入协调；关闭它时结束相关任务 | `VexPresentation.swift:196`、`:201`；`VexServerConnection.swift:26`、`:87`；`VexSettingsSection.swift:10` |
| IPC | 使用 NSXPC 等已有机制定义带版本的请求、进度、取消和结果；宿主核验调用进程身份和支持的协议版本。允许的操作列举为实例查询、MRPack 导入/更新、准备、启动与取消；不提供任意命令执行或任意路径写入能力 | 当前 `Sources/` 没有 NSXPC 接入；`InstanceTransfer.swift:169`、`:240`；`LaunchService.swift:34` |
| 实例状态 | 宿主执行实例与 StateStore 写入，负责路径和并发协调；组件拥有服务器/项目/房间映射，通过返回的实例 ID 继续操作。跨进程消息包含操作 ID，连接失效时明确处理未完成请求 | `VexInstanceManager.swift:18`、`:52`；`VexMRPack.swift:62`、`:104`；`LaunchService.swift:38` |
| Microsoft 账号 | 宿主按现有 AccountService → LaunchService → GameMonitorClient 流程读取和使用凭据；伴随应用只接收可用账号的展示信息与启动结果 | `LaunchService.swift:40`、`:61`、`:99`、`:106` |
| Vex Keychain | 主程序与 ruri-cli 的受限宿主服务继续负责 Keychain 读写；伴随应用拥有独立代码签名，通过身份核验后只能请求已授权服务器的 Vex 会话。请求使用规范化服务器 URL，宿主验证组件和服务器归属，返回值只在内存中使用；禁止任意 Keychain 查询。Microsoft 凭据保持在宿主内部 | `VexSettings.swift:81`、`:85`、`:104`；`LaunchService.swift:61`；`scripts/release/signing.py:170` |
| CLI | 主 CLI 的通用扩展入口调用已安装组件的 CLI 或 IPC 服务，组件负责 Vex 参数、帮助和 schema；宿主关闭开关时不启动伴随进程，不发现 Vex 命令 | `VexCommands.swift:18`；`CLIApplication.swift:49`；`CommandHelp.swift:16` |
| 普通服务器列表 | 宿主仍需通用条目过滤、托管实例身份与启动前整理接口。组件登记适用规则，宿主执行这些通用操作，以保留目前的列表与 servers.dat 行为 | `ServersView.swift:208`；`LaunchService.swift:95` |

【判断】伴随应用拥有独立的网络配置和故障生命周期，宿主的普通 URLSession 行为可以保持现状。伴随进程与宿主之间的协议仍需完成签名核验、实例范围限制、连接失效处理和更新兼容；仅创建另一个可执行文件不能直接获得这些保障。Vex 的会话读取、写入、失效和退出登录继续经过主程序或 ruri-cli，维持现有两个 Keychain 调用主体。伴随应用的独立窗口与方案 A 的主窗口嵌入是明确的交互差别。

【资料】Apple 的 [Bundle.load](https://developer.apple.com/documentation/foundation/bundle/load()) 支持把插件代码加载到当前进程，并要求代码仍在使用时保留 Bundle 对象；[NSXPCConnection](https://developer.apple.com/documentation/foundation/nsxpcconnection) 提供双向进程通信、连接失效通知、接口对象和代码签名要求。两份官方 Markdown 文档已完整读取。

【判断】仅下载 HTTP helper，同时继续把 Vex UI、Core 和 CLI 编译进默认 Ruri，仍会保留本报告开头列出的默认产物内容，无法满足此次完整组件按需安装的要求。

## 按需分发需要的宿主基础能力

1. 【判断】用户启用时下载兼容当前宿主版本与 CPU 架构的完整组件；校验签名和摘要，安装失败时保持禁用，避免运行不完整组件。默认包和默认构建依赖图均不包含 Vex 或 EventSource。
2. 【判断】组件启停和卸载需要分离“停止执行”与“删除已下载文件”。未安装时不读取组件 Keychain、不启动 helper、不加载 bundle；关闭时取消连接、轮询、下载与加入操作。
3. 【判断】页面、服务器分区、设置、URL 路由和 CLI 命令使用通用扩展契约。方案 A 提供主窗口容器，方案 B 打开伴随应用窗口；CLI 的 help/schema/golden 必须继续覆盖未安装与关闭两种状态。
4. 【判断】实例导入、严格更新、准备完成和 QuickPlay 启动由宿主服务执行；组件不获取 Microsoft token。调用协议需要操作 ID、取消、进度和明确结果。
5. 【代码】当前打包脚本只处理预编译的主程序、CLI、monitor、RuriGame 和资源包（`scripts/build-app.sh:23`、`:49`）；当前签名检查也是固定列表（`scripts/release/signing.py:184`）。【判断】按需产物需要独立发布、签名验证与兼容版本清单，现有脚本尚未提供这些能力。
6. 【代码】当前本地化查找固定为 `Ruri_RuriLocalization.bundle`（`Sources/RuriLocalization/LocalizationResources.swift:6`、`:23`）。【判断】组件专属资源需要独立 bundle 和验证入口，避免默认宿主继续携带全部 Vex 翻译及其生成代码。

## 当前未验证部分

【实测】本次只读调查检查了源代码、当前二进制依赖与符号、官方 API 文档，没有构建按需组件原型。动态代码的实际签名接受条件、独立组件的协议与生命周期、默认产物中的依赖清除、组件资源本地化、更新兼容和真实游戏流程都需要在设计确定后实施并验收。独立 SDL 正常退出修复位于 `fix/sdl-normal-quit`，没有合入当前 P1 分支；它属于普通游戏生命周期能力，可以单独审阅。
