# Ruri Vex 按需组件的平台约束

日期：2026-10-18。源码位置：`/Users/linwhite/ruri-vex-experimental`，基线提交 `61dc1df78cd961714f2034b751940495bea3f4e1`，包含当前 R2 未提交修改。本文只进行了源码、已有产物和官方文档的读取；没有实施组件拆分，没有执行动态加载、组件下载安装或新的构建测试。

## 本地代码与产物

| 位置 | 已核实内容 | 对组件拆分的约束 |
| --- | --- | --- |
| `Package.swift:12`、`:29`、`:31`、`:34` | `RuriCore` 未声明为独立动态产品；Vex 直接编入 Core、GUI 和 CommandKit；EventSource 是 Core 依赖。 | 默认产品需要移除这些直接编译关系，组件拥有独立构建清单。 |
| `RootView.swift:19`、`:75`、`:92`、`:97`、`:203` | 主程序直接创建 Vex 状态、显示视图、传递环境对象、处理链接和侧边栏入口。 | 宿主需要固定的通用入口与能力接口，具体 Vex 类型和页面进入下载组件。 |
| `VexInstanceManager.swift:60`、`:75`、`:78`、`:104` | 当前实现调用实例导入、ModpackUpdater 和宿主状态存储。 | 组件调用宿主公开的实例操作，避免独立复制宿主内部状态管理实现。 |
| `VexClient.swift:23`、`VexEventStream.swift:62`、`:69` | HTTP 使用 `URLSession`，SSE 使用 `URLSession.AsyncBytes` 和 EventSource。 | B 可以保留此网络组合；A 保持宿主 ATS 配置时，需要另外处理任意 HTTP 地址的传输。 |
| `scripts/build-app.sh:49` | 打包脚本复制 SwiftPM 生成的资源 bundle。 | 现有资源 bundle 没有承担下载代码组件的加载协议。组件资源需要独立归属，避免默认包继续携带 Vex 内容。 |
| `scripts/build-app.sh:35`、`:39`、`:54` | monitor、CLI 和主程序的签名命令未加入 `--options runtime`。CLI 使用与主程序相同的 `dev.ruri.launcher` 标识，脚本注释说明用于已有钥匙串访问。 | 当前主程序签名步骤尚未提供组件所需的完整 Hardened Runtime 发布流程。伴随应用应有独立标识，不能直接沿用 CLI 的身份安排。 |
| `scripts/lib/build.sh:64` | 非 ad hoc 的 GameHost 使用 Hardened Runtime，并应用 GameHost 的专用 entitlements。 | GameHost 的权限不构成主程序或新组件的权限配置。 |
| `scripts/release/signing.py:170` | 检查固定叶证书、Apple Development 证书类型与 Team ID。 | 接入 Developer ID 发布流程时，需要明确调整证书要求及验证逻辑。 |
| 现有 `build/Ruri.app`、`Contents/Helpers/ruri-cli` | `codesign` 显示 arm64、ad hoc、没有 Team ID；当前 GameHost 为 universal、ad hoc。 | 这些产物不能证明同 Team ID 的动态加载或 Developer ID 分发已经通过。 |
| 现有应用与 CLI 的 `otool -L` | 主程序列出的额外第三方动态依赖为 Sparkle；没有独立 RuriCore 或 Vex 动态库。 | 当前产物已经包含 Vex 实现，运行时开关仅控制功能行为。 |

表中的 Swift 文件位于 `Sources/Ruri/` 或 `Sources/RuriCore/Vex/`。签名检查只读取公开签名元数据，没有读取私钥、账号存储或钥匙串项目。

## A：完整原生代码 bundle，页面继续嵌入 Ruri

下载对象可以是包含 Mach-O 可执行代码、资源和私有依赖的插件 bundle。宿主在启用后通过 `Bundle` 加载入口，组件返回 `NSViewController`，宿主将其放入现有页面容器；组件内部可以使用 `NSHostingController` 管理 SwiftUI 页面。Apple 提供代码 bundle 加载与 SwiftUI/AppKit 容器机制；具体入口协议和页面组合仍需要实现验证。[Apple：Plug-in Architectures](https://developer.apple.com/library/archive/documentation/Cocoa/Conceptual/LoadingCode/Concepts/Plugins.html)、[Bundle.load](https://developer.apple.com/documentation/foundation/bundle/load())、[NSHostingController](https://developer.apple.com/documentation/swiftui/nshostingcontroller)

宿主边界可采用 Objective-C 可表达的协议、Foundation 数据和 AppKit 容器。具体的 Vex SwiftUI 类型、`AppModel`、内部 Swift struct、actor 和泛型留在各自实现内部。宿主提供账号选择结果、MRPack 准备与更新、实例操作、启动、取消及进度接口。这个接口设计属于工程判断，当前仓库没有对应二进制协议。

Swift 的稳定 ABI、模块稳定性与库演进分别处理运行时、编译器导入和独立更新。向外提供 Swift 二进制接口时，需要从首次发布启用库演进并生成 `.swiftinterface`；Xcode 对应 `BUILD_LIBRARY_FOR_DISTRIBUTION`，直接编译对应 `-enable-library-evolution` 与 `-emit-module-interface`。现有构建脚本没有启用这些选项。采用 Objective-C 边界可以减少共享 Swift ABI 的范围，接口本身仍需要版本管理。[Swift：Library Evolution](https://www.swift.org/blog/library-evolution/)、[Swift：ABI Stability and More](https://www.swift.org/blog/abi-stability-and-more/)

组件必须匹配宿主进程架构、最低 macOS 版本和宿主接口版本。当前项目最低 macOS 版本为 14。组件自己的第三方库和资源必须随组件提供；同一进程中的共享协议类型需要唯一、明确的定义来源。将第二份完整 RuriCore 链接进组件会引入重复类型和独立全局状态的风险，不能据此复用当前宿主内部对象。

Hardened Runtime 默认启用 library validation：加载代码需要由 Apple 签名，或与主可执行文件具有同一 Team ID。该要求覆盖组件的动态依赖。满足同 Team ID 的方案可以保留 library validation；当前 ad hoc 产物没有提供这项验证依据。Apple 同时说明，插件使用宿主进程的 entitlements。[Apple：Disable Library Validation](https://developer.apple.com/documentation/bundleresources/entitlements/com.apple.security.cs.disable-library-validation)、[Apple：Notarizing macOS Software Before Distribution](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)

A 与宿主共享地址空间。能力接口可以规定组件正常调用哪些操作，但无法提供针对组件代码的进程隔离。Microsoft 凭据继续由宿主管理、接口不返回这些凭据，属于实现约束；不得将其描述为同进程组件无法访问宿主内存的系统保证。[Apple：Plug-in Architectures](https://developer.apple.com/library/archive/documentation/Cocoa/Conceptual/LoadingCode/Concepts/Plugins.html)

## B：完整伴随应用，拥有独立窗口和 CLI

下载对象可以是完整 `.app`，包含 Vex 页面、模型、网络、SSE、命令实现和资源。Ruri 保留安装、启停及能力调用入口；`ruri vex` 委托伴随组件执行。Vex 窗口由伴随应用自己的进程管理，宿主通过版本化 IPC 提供账号状态、实例导入、更新和游戏启动。

进程间交换序列化请求、操作标识、结果与进度，可以避免跨进程共享 Swift 对象 ABI。若采用 NSXPC，接口使用 Objective-C 可表达的方法及允许的 Foundation／NSSecureCoding 类型，并明确消息和返回对象范围。随宿主嵌入的 app-private XPC service 使用 `Contents/XPCServices`；下载到外部目录的伴随应用需要自己的启动和连接机制，不能直接套用已嵌入服务的名称发现行为。[Apple：Creating XPC Services](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingXPCServices.html)

Microsoft 登录、钥匙串持久化和启动凭据继续归主程序与原 CLI 管理。伴随应用通过受限 IPC 取得指定服务器的 Vex 会话，接口不提供 Microsoft 凭据或任意钥匙串读取。连接身份、允许的操作、服务器归属和生命周期必须校验；相同 UID 或相同 Team ID 都不能单独替代这些校验。独立进程提供地址空间边界，额外的文件访问隔离仍取决于其实际 Sandbox 与权限配置。

伴随应用拥有独立可执行文件和签名身份，主程序的 library validation 不直接约束另一进程；伴随应用加载自身动态库时仍需要满足自己的签名与 Hardened Runtime 条件。当前仓库没有此伴随应用，也没有其 IPC 和权限实测证据。

## HTTP、ATS 与 EventSource

| 条件 | A：同进程代码 bundle | B：独立伴随 `.app` |
| --- | --- | --- |
| 主 Ruri 的 ATS 配置保持不变 | 可以保持 | 可以保持 |
| 组件自己的 `Info.plist` | 没有获得独立 URL Loading System 网络策略的文档契约 | 伴随应用可在自身 app 的 `Info.plist` 声明 ATS 例外 |
| 任意用户提供的 HTTP 服务器 | 需要组件采用不经过 ATS URL Loading System 的成熟 HTTP 实现 | 可通过伴随应用的 ATS 例外继续使用 `URLSession` |
| 当前 EventSource 调用 | 当前 `URLSession.AsyncBytes` 接入需要随传输改变；继续使用成熟 SSE 解析器 | 可以继续使用当前 `URLSession.bytes` 与 EventSource 组合 |
| CLI 的 HTTP 请求 | CLI 作为单独进程需要自己的加载和网络安排 | CLI 经 IPC 让伴随 `.app` 执行网络操作，避免假定独立 CLI 自动继承应用的 plist |

Apple 将 ATS 例外定义在 app 的 `Info.plist`，作用于该 app 通过 URL Loading System 发出的连接。结合 A 与宿主同进程、B 独立 app 的结构，可以推断 B 能独立配置 HTTP，A 的插件 plist 不能作为独立 ATS 覆盖入口。官方文档没有承诺插件专属 ATS 覆盖 API；本次没有执行 A/B 打包后的 HTTP 实测。[Apple：NSAppTransportSecurity](https://developer.apple.com/documentation/bundleresources/information-property-list/nsapptransportsecurity)

Apple 明确 ATS 适用于 `URLSession` 等标准 URL Loading System，低层 Network framework 与 CFNetwork 接口不受同一 ATS 规则约束。因此 A 可以把成熟 HTTP 库及其依赖完整放在下载组件内；B 的 ATS 需求本身无需增加 AsyncHTTPClient。HTTP 例外不应伴随关闭 HTTPS 的证书与主机名验证。[Apple：Preventing Insecure Network Connections](https://developer.apple.com/documentation/security/preventing-insecure-network-connections)

任意自定义域名的 HTTP 需求可以在伴随应用声明 `NSAllowsArbitraryLoads=true`。macOS 10.12 及以后，如果同时出现 `NSAllowsLocalNetworking`、`NSAllowsArbitraryLoadsForMedia` 或 `NSAllowsArbitraryLoadsInWebContent`，系统会忽略全局 `NSAllowsArbitraryLoads` 的值；这些配置不能按照简单叠加理解。最终 plist 应按实际网络需求配置并验证。[Apple：NSAppTransportSecurity](https://developer.apple.com/documentation/bundleresources/information-property-list/nsapptransportsecurity)

当前 `Resources/Info.plist` 没有 `NSAppTransportSecurity`；当前 HTTP/SSE 实现也没有独立伴随进程。上述 B 的可行性来自官方平台规则与现有调用形式，尚未取得实际伴随应用请求结果。A 中所选 HTTP 库还需验证流式读取、取消、重定向和 EventSource 接入。

## 组件位置、依赖与公开分发

组件应安装到 Ruri 支持目录内的独立版本目录。修改已签名 `Ruri.app` 的包内容会破坏签名封存，安装器不能把下载组件直接写入已安装应用。Apple 的插件文档允许宿主从外部目录查找组件；代码和资源的位置仍需遵守 bundle 的结构要求。[Apple：Embedding Nonstandard Code Structures in a Bundle](https://developer.apple.com/documentation/xcode/embedding-nonstandard-code-structures-in-a-bundle)、[Apple：Plug-in Architectures](https://developer.apple.com/library/archive/documentation/Cocoa/Conceptual/LoadingCode/Concepts/Plugins.html)

伴随应用可以将动态库放在 `Contents/Frameworks`、辅助程序放在 `Contents/Helpers`、资源放在 `Contents/Resources`。组件的所有依赖需要可重定位，采用 `@rpath`／`@loader_path` 等路径并检查实际 Mach-O 依赖；构建机器上的绝对路径不能成为发布前提。依赖路径修改必须发生在签名之前。[Apple：Placing Content in a Bundle](https://developer.apple.com/documentation/bundleresources/placing-content-in-a-bundle)、[Apple：Run-Path Dependent Libraries](https://developer.apple.com/library/archive/documentation/DeveloperTools/Conceptual/DynamicLibraries/100-Articles/RunpathDependentLibraries.html)

针对通过互联网自动下载的正式组件，应建立 Developer ID 签名、公证与系统验证流程。Apple 的公证要求包括有效签名、适用可执行文件的 Hardened Runtime、安全时间戳等；Apple Development 与 ad hoc 不能作为这项流程已经满足的证据。当前脚本固定的证书类型检查需要与正式分发身份一起审阅。[Apple：Notarizing macOS Software Before Distribution](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)

外层 ZIP 不直接签名，内部可执行代码可以签名；DMG、PKG 有各自签名方式。嵌套代码按内部到外部顺序签名，下载组件需要自身有效的分发与公证结果。离线验证还涉及 stapled ticket。组件安装器必须在加载前完成发布身份、完整性与兼容性检查，因为加载代码 bundle 可以立即执行初始化代码。[Apple：Packaging Mac Software for Distribution](https://developer.apple.com/documentation/xcode/packaging-mac-software-for-distribution)、[Bundle.load](https://developer.apple.com/documentation/foundation/bundle/load())

## 更新、禁用和卸载

| 行为 | A | B |
| --- | --- | --- |
| 版本接受条件 | 宿主接口、架构、最低系统、发布身份匹配；当前计划可限定到已验证的具体 Ruri 发布版本 | IPC 协议、能力集合、最低系统、架构和发布身份匹配 |
| 开始更新 | 下载到新的版本目录，验证完成后选择激活时机 | 下载到新的版本目录，停止旧进程后启动新版本 |
| 关闭功能 | 取消 SSE、轮询、下载及未完成加入，移除页面和命令，解除宿主回调 | 取消任务、关闭 IPC、等待伴随进程结束 |
| 已加载代码 | 不承诺当前进程内卸载；新代码版本在后续宿主进程中加载 | 进程结束后释放代码与进程资源 |
| 用户数据 | 组件文件与账号、实例、世界分开处理 | 组件文件与账号、实例、世界分开处理 |

Apple 的 `Bundle.unload()` 仅尝试卸载，并要求调用者保证对象和数据没有继续引用代码。Swift 编译器开发者进一步说明，Swift 运行时会缓存类型元数据与函数指针，已经执行的 Swift 代码无法按一般动态库卸载方式可靠处理。因此 A 的生命周期应明确保留已加载代码直到宿主进程退出，不将开关关闭等同于释放代码内存。[Apple：Bundle.unload](https://developer.apple.com/documentation/foundation/bundle/unload())、[Swift 官方论坛：Joe Groff 对 Swift 动态库卸载的解释](https://forums.swift.org/t/unloading-dll-stalls-when-depending-on-swift-certificates/75542)

关闭扩展不会自动结束已由宿主接管的 Minecraft 游戏。重新启用时，组件使用保留的配置和会话状态；不同服务器的 Vex 会话仍需隔离。GUI 与 CLI 同时运行时，需要共同的安装状态和互斥操作，避免一方删除另一方正在加载的版本。

## 尚未取得的实测证据

- 已签名 A bundle 的实际加载、原生页面嵌入、宿主能力调用，以及 GUI／CLI 两个进程各自加载的行为。
- A 的成熟 HTTP 库与 EventSource 接入；正式 HTTP 域名、HTTPS 证书校验、重定向、取消和 SSE 断线恢复。
- B 自身 ATS 配置下的 `URLSession` 请求、SSE 和 CLI 经 IPC 调用；主 Ruri ATS 配置保持原样。
- 正式 Developer ID 组件在带有 quarantine 的新下载、首次启动、公证及离线条件下的系统接受结果。
- 主程序和组件更新、签名拒绝、版本拒绝、禁用期间任务结束，以及卸载后默认产品的完整资源与依赖检查。

本次没有产生组件体积、首次下载耗时、动态加载耗时或内存释放的测量值。
