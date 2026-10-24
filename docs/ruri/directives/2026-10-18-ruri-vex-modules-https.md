# 执行指令：ruri 中 Vex 的形态改为"同仓库独立模块"，vexd 提前支持 HTTPS

- **日期**：2026-10-18
- **答复对象**：`RURI-OPTIONAL-COMPONENT.md`（按需安装组件评估）
- **结论**：A（原生扩展包）和 B（伴随应用）都不采用，按本指令的方案 C 执行。
- **对 P1/P2 的影响**：本指令只影响 ruri。vex 的 P1 合入 dev，以及 P2 服务端的工作，不需要等待本指令，照常推进。

## 决定理由（写进报告，便于以后向上游说明）

1. **与上游的 Nix 打包方向冲突。** 上游已经有 `flake.nix` 和 `nix/package.nix`，最新提交 `8bd4878` 还在完善 Nix 构建。从源码构建出来的宿主，无法匹配一个绑定了"发布版本 + 工具链 + 签名"的预编译组件。另外，nixpkgs 原则上不接受运行时下载的二进制。A 会直接堵死"进入 nixpkgs"这个目标。
2. **签名负担反而落到上游。** 主程序启用了 library validation，目前只有 GameHost 关闭了它。要加载组件，组件就必须用上游的 Team ID 签名，或者主程序关闭 library validation。无论哪种，都要上游来构建、签名、发布我们的组件，比直接合入代码的负担更大。
3. **安全面变大。** 无论用户是否启用，每一份 Ruri 都会带上一条"下载并执行代码"的路径。
4. **B 需要上游长期维护一份版本化的 IPC 协议**，而且 Vex 会跑在独立窗口里，与 ruri 内集成的目标不符。

"关闭时对上游零影响"这个目标是对的，但可以用更轻的办法实现：关闭时的行为一致性 R1 已经做到（golden 逐字节相同，不访问网络和钥匙串），剩下要解决的是代码边界、依赖范围和可删除性。

## 方案 C

### C1 Vex 移入独立的 SwiftPM target

- 新建 `RuriVex`（Core 部分）、`RuriVexUI`、`RuriVexCLI` 三个 target。所有 Vex 代码、资源、本地化和测试数据都放在这些 target 里。
- 宿主 target 中**不出现任何 Vex 类型**。

### C2 宿主只提供通用的实验性功能扩展点

宿主新增一组与 Vex 无关的协议和注册表，形态参考报告中 A 的宿主接口，但采用静态链接，不涉及下载、加载器或签名。扩展点包括：

- 页面提供者、设置分区、服务器页分区和列表过滤；
- CLI 命令提供者，负责命令发现、帮助、schema 和执行；
- 启动前回调；
- URL 处理器；
- 受限的宿主能力：账号状态、实例查询、MRPack 准备与更新、文件校验、发起和取消启动、查询会话。宿主不向扩展提供 Microsoft 凭据。

执行要求：

- Vex 通过一行注册代码接入。现有 14–15 个接入文件，应收敛为"扩展点本身 + 一处注册"。
- 去掉 Vex 视图对 `AppModel` 和 Core 内部成员（`PreparedInstanceImport` 内部字段、`ModpackRegistry.save` 等）的直接访问，一律改走宿主能力接口。
- generation/build 校验、"一次加入最多启动一次"、取消、托管文件冲突立即失败，这些语义保持不变。

### C3 构建期开关

- 优先使用 SwiftPM 的 package traits，SE-0450，Swift 6.1 起提供。定义 trait `Vex`，让 Vex 的三个 target 和 EventSource 只在启用该 trait 时才进入依赖图和产物。
- 先实测 `--build-system swiftbuild` 和上游的 Nix 构建是否支持 traits。不支持时退回到一个编译条件，并在报告中说明。
- 默认是否启用 trait 由上游决定。本轮默认启用，以便我们自己使用。
- 验收时，trait 关闭的构建要满足：依赖解析结果、包内文件、链接的库和符号中都没有 Vex 和 EventSource。

### C4 不引入 AsyncHTTPClient

- **根因**：上游 `Info.plist` 没有 ATS 例外，GUI 的 URLSession 会拒绝 `http://` 的 vex 地址。用 SwiftNIO 绕过 ATS 不是正确的修法，上游也不会接受。
- **修法**：在服务端提供 HTTPS（见下面 S1）。
- **客户端要求**：GUI 只接受 `https://` 服务器地址。CLI 若要接受 `http://`，仅限 loopback 地址，并在帮助中说明。

### C5 EventSource

- 继续保留，但只在 `Vex` trait 下引入。
- 在报告中注明许可证（MIT）和体积，供用户和上游讨论时使用。

### C6 兜底方案

如果上游最终不接受集成，就把 Vex 做成独立的小应用，只通过上游已有的公开 CLI（`pack import`、`launch start --server` 等，均带 JSON 输出）驱动 ruri，上游零改动。本轮不实现，只在报告中确认这些 CLI 能力足够。

## vexd 侧：S1 提前提供 HTTPS（原计划在 P6）

- **vexd 原生支持 TLS**：
  - 配置 `server.tls_cert_file` 和 `server.tls_key_file`；
  - 证书文件变化后热加载，不需要重启；
  - 支持分监听器配置：对外监听使用 HTTPS；集群内房间 Pod 访问的 `internal_base_url` 可以保留 LAN 上的 HTTP，并且只对 NetworkPolicy 放行的来源开放。
- **我们的部署**：
  - 在 `imdomestic.com` 下选一个名字，例如 `vex.inner.imdomestic.com`，在 tailnet 内解析到 `100.64.0.43`；
  - 用 NixOS `security.acme` 的 DNS-01 签发证书，无需对公网开放端口；
  - 续期后触发证书重新加载；
  - DNS 服务商的 API 凭据放进 SOPS。
  - 如果现有 DNS 服务商不支持 DNS-01，停下来报告，由用户决定。
- **地址切换**：`public_base_url` 改为 HTTPS 地址，已经签发的邀请链接和 mrpack 签名链接随之更换。

## 验收

1. **构建产物**：
   - 启用 trait 的构建：功能、真实联测与 R1、R2 的结论一致。
   - 关闭 trait 的构建：依赖图、产物文件、链接库和符号中都没有 Vex 和 EventSource。
   - 两种构建都通过上游的测试和本地化校验。
2. **宿主代码**：
   - 不引用任何 Vex 类型，用 rg 检查；
   - 列出 Vex target 以外的改动文件，并说明每处是否与 Vex 无关、属于通用扩展点；
   - Vex 的注册集中在一处。
3. **运行时开关关闭时**：CLI golden 逐字节相同，不访问网络，不读钥匙串，不运行 SSE。
4. **HTTPS 联测**：GUI 通过 HTTPS 正式地址完成以下检查：邀请登录、多服务器分组、会话失效、单台服务器断开、SSE 恢复、进入房间。用 `http://` 地址时，GUI 给出明确的错误提示。
5. **未引入 AsyncHTTPClient**，`Info.plist` 中也没有新增 ATS 例外。
6. **vexd 证书**：证书由公开 CA 签发；热加载经过实测；房间 Pod 到 vexd 的内部访问不受影响。
7. **报告**：写到 vex 仓库 `docs/ruri/reports/`。

