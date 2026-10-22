# Vex

Vex 管理 Minecraft 模组项目的 Git 分支、构建、测试房间、世界 revision、资源包和快照。`vexd` 提供 v2 API，`vex` 提供项目操作命令，`agent/` 提供服务端房间转移功能。

当前实现使用 Kubernetes 运行后端，支持 Fabric 和 NeoForge。归档后端支持本地目录及 rsync-ssh。宿主机构建只接受 `trust = "trusted"` 的项目，由独立 `vex-build` 账户执行；所有项目共享一个构建名额。

配置按内置默认值、TOML 文件、`VEX_` 环境变量、命令行四层合并。服务端和 CLI 使用同一套解析器；未知字段和未支持的 loader 会使配置检查失败。项目 ID 为最多 23 个字符的小写标识符，禁止连续 `--` 和末尾 `-`。房间地址为 `<room>--<project>.<zone>`。

```sh
vexd config check --config /etc/vex/vexd.toml
vexd user invite operator
vex login https://vex.example/i/XXXX-XXXX
vex config show --sources
vex -p example-mod git branches
vex -p example-mod rooms list
vex world publish /path/to/world -p example-mod --world plaza
```

运行房间需要明确设置 `minecraft.accept_eula = true`。人工登录使用一次性邀请，会话凭据保存在 macOS Keychain 或 Linux Secret Service；系统凭据服务不可用时使用当前用户独占的 0600 文件。`vex logout` 吊销当前会话并清除本机凭据。自动化使用配置中的静态 `token_sha256`，通过 `vex login --token-stdin` 登录，或者配置 `token_file`。签名密钥、Git 凭据、kubeconfig 和归档凭据使用文件路径引用。

CLI 配置位于 `~/.config/vex/config.toml`，登录成功会登记 `[servers.<name>]` 的 URL 和显示名称；`default_server` 指定默认服务器，`--server <name>` 选择其他服务器。服务器 URL 经过规范化后作为独立的凭据索引。每台服务器只有一个可访问项目时自动选择该项目；存在多个项目时使用 `-p` 选择。

设计范围见 [Vex 设计](docs/design/vex.md) 和 [P1 补充指令](docs/directives/2026-10-17-vex-p1-amendment.md)。构建、NixOS 部署、真实组件测试和系统切换保护见 [部署说明](deploy/README.md)；测试原则、固定测试文件与集成测试变量见 [开发说明](docs/development.md)。实际验收记录见 [P1 报告](docs/reports/2026-10-17-vex-p1.md)。

系统配置由独立的 `imdomestic/nix-config` 仓库管理，通过本仓库 `nixosModules.vexd` 引入服务模块；应用由根 flake 的 `packages.x86_64-linux.vex` 构建。早期实施与归档工具保存在 [历史目录](docs/reports/archive/README.md)，首次系统搭建配置保存在 [bootstrap 归档](docs/reports/bootstrap/README.md)。
