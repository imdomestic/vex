# P0：Mac 与 ruri

核对日期：2026-10-14（Asia/Tokyo）。执行目录：`/Users/linwhite/chorus-testbed/.work/ruri-upstream`。

## 环境事实

| 项目 | 实际值与证据 |
| --- | --- |
| macOS | `26.5.2`，系统构建 `25F84`；`sw_vers` |
| 架构 | `arm64`；`uname -m` |
| 内存 | `17179869184` 字节，即 16 GiB；`sysctl -n hw.memsize` |
| 工作目录所在磁盘 | 核对时可用约 58 GiB；`df -h /Users/linwhite/chorus-testbed` |
| 开发目录 | `/Applications/Xcode-26.6.0.app/Contents/Developer`；`xcode-select -p` |
| Xcode | `26.6`，构建 `17F113`；`xcodebuild -version` |
| 首次启动准备 | `xcodebuild -checkFirstLaunchStatus` 退出码 `0` |
| Swift | Apple Swift `6.3.3`，`swiftlang-6.3.3.1.3`，`clang-2100.1.1.101`；`xcrun swift --version` |
| macOS SDK | `26.5`；`/Applications/Xcode-26.6.0.app/Contents/Developer/Platforms/MacOSX.platform/Developer/SDKs/MacOSX26.5.sdk` |
| `actool` | `/Applications/Xcode-26.6.0.app/Contents/Developer/usr/bin/actool`；测试宿主图标编译通过 |
| `xcstringstool` | `/Applications/Xcode-26.6.0.app/Contents/Developer/usr/bin/xcstringstool`；中英文资源编译通过 |
| 已安装的 Ruri | `/Applications/Ruri.app`，版本 `0.10.5`，构建 `33`；通过 `plutil` 读取应用信息 |

## 上游来源

- 仓库：`guh0613/ruri`，通过公开 HTTPS 克隆。
- 分支：`main`。
- SHA：`4353f8d046a11e59c64ce58e5f4f6ae6ac3e5f6c`。
- 提交时间：`2026-10-09T00:38:19+08:00`。
- `Resources/Info.plist` 的版本：`0.10.6`，构建 `21`。
- `Package.swift` 要求 Swift tools `6.2`、最低 macOS `14.0`。
- 仓库中没有 `AGENTS.md`。
- 构建和测试后 `git status --porcelain=v1` 输出为空；源码和 `Package.resolved` 保持上游原样。

固定的直接依赖为 `swift-argument-parser 1.8.2`、`ZIPFoundation 0.9.20`、`swift-markdown 0.8.0`、`TOMLDecoder 0.4.5`、`Sparkle 2.10.0`。

## 构建方式

在本机 Xcode 26.6 下，构建、测试和产物目录查询统一传入 `--build-system swiftbuild`。该构建方式编译 String Catalog，生成 `en.lproj`、`zh-Hans.lproj` 及其 `.strings`、`.stringsdict` 文件。本机 SwiftPM 默认的 `native` 构建规则对 `.xcstrings` 使用 `copy-tool`，运行时所需的语言资源必须经过编译。

本机 `swift build --help` 和 `swift test --help` 均提供 `swiftbuild` 选项，并标注为 preview。本次验收以实际二进制、完整语言资源和真实测试结果为依据。String Catalog 的编译任务由 [Swift Build 的 XCStringsCompiler](https://github.com/swiftlang/swift-build/blob/main/Sources/SWBApplePlatform/XCStringsCompiler.swift)提供。

## debug 构建

实际执行：

```sh
env DEVELOPER_DIR=/Applications/Xcode-26.6.0.app/Contents/Developer \
  TMPDIR=/Users/linwhite/chorus-testbed/.work/ruri-tmp \
  RURI_BUILD_DIR=/Users/linwhite/chorus-testbed/.work/ruri-swiftbuild \
  PYTHONDONTWRITEBYTECODE=1 \
  scripts/swift-build.sh -c debug --product Ruri --build-system swiftbuild --jobs 4
```

退出码 `0`；构建报告耗时 `56.01` 秒，包含依赖准备与入口检查的完整命令耗时 `78.20` 秒。构建产物目录由同一入口加 `--show-bin-path` 查询得到：`.work/ruri-swiftbuild/out/Products/Debug`。

`file` 确认其中的 `Ruri` 为 Mach-O 64-bit arm64 可执行文件。`xcrun vtool -show-build` 确认平台为 `MACOS`、最低系统版本为 `14.0`、SDK 为 `26.5`、链接器版本为 `1267.0`。

本地化检查通过：`3943` 条消息、`209` 个访问器分组，`en` 和 `zh-Hans` 均为 `100.0%`，缺失和过期条目均为 `0`。资源 bundle 中两个语言目录各包含 `16` 个 `.strings` 和 `13` 个 `.stringsdict`。

日志：`.work/ruri-logs/debug-swiftbuild-xcode26.6.log`。

## 测试

实际执行：

```sh
env DEVELOPER_DIR=/Applications/Xcode-26.6.0.app/Contents/Developer \
  TMPDIR=/Users/linwhite/chorus-testbed/.work/ruri-tmp \
  RURI_BUILD_DIR=/Users/linwhite/chorus-testbed/.work/ruri-swiftbuild \
  PYTHONDONTWRITEBYTECODE=1 \
  scripts/swift-build.sh test --build-system swiftbuild --jobs 4 \
  --filter 'NetworkRoutingTests|FileTreeManifestTests|FileTreeDirectoryTests|FileExtendedAttributesTests|StateStoreTests|(^|[.])(EnglishCatalogTests|LocalizationTests)/|(^|[.])LocalizationFallbackTests/fallbackUsesTheTranslationLanguageForPluralRules'
```

退出码 `0`，`28` 项测试全部通过。测试模块编译耗时 `50.19` 秒，包含测试宿主准备与执行的完整命令耗时 `72.10` 秒。

| 测试范围 | 数量 | 实际验证 |
| --- | --- | --- |
| `NetworkRoutingTests` | 3 | 下载地址、认证请求与 HTTP 方法的路由规则 |
| `FileTreeManifestTests` | 5 | SHA-256、文件复制、元数据变动、内容变动、取消 |
| `FileTreeDirectoryTests` | 3 | 目录复制与 AppleDouble 文件处理 |
| `FileExtendedAttributesTests` | 4 | Darwin 扩展属性、资源分支与目录属性保留 |
| `StateStoreTests` | 7 | 真实文件锁、并发事务、JSON 持久化、版本迁移 |
| `EnglishCatalogTests` | 2 | 全部英文消息完整性、参数格式化与单复数规则 |
| `LocalizationTests` | 3 | 语言匹配、实际 bundle 读取、并发语言上下文与参数脱敏 |
| `LocalizationFallbackTests.fallbackUsesTheTranslationLanguageForPluralRules` | 1 | 翻译缺失时的语言选择与复数规则 |

测试入口实际完成 `RuriGame.app` 的 arm64/x86_64 编译、`actool` 图标编译、代码签名和严格签名验证。Swift Testing 日志分别记录本地化 `6` 项、核心功能 `22` 项通过；其他测试模块在该筛选条件下选中 `0` 项。

测试范围依据用户禁止 mock 的要求确定，逐项检查所选测试源码。上游全量测试包含模拟 HTTP 响应和 `unittest.mock`，本次未执行全量测试。测试代码及断言保持上游原样。

日志：`.work/ruri-logs/test-swiftbuild-xcode26.6.log`。P0 的 Mac 构建与测试检查通过。

## 工作目录约束

克隆、编译产物、测试宿主、临时文件与日志位于根仓库忽略的 `.work/` 目录。执行前已经阅读构建入口、共享脚本、本地化入口和所选测试源码。构建入口的本地化命令使用 `--check`；测试宿主的 `mktemp` 显式使用 `RURI_BUILD_DIR` 下的目录，文件测试通过 `TMPDIR` 使用项目目录。
