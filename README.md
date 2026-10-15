# Chorus Testbed

chorusd 管理 Chorus 构建、Minecraft 房间、世界快照、资产与 tank 归档；独立客户端 Ruri Chorus 提供服务器和 Git 页面。执行结果见 [第二轮报告](docs/reports/2026-10-15-round2.md)，接口定义见 [API](docs/api.md)，配置选择见 [决策](docs/decisions.md)。

## 运行位置

| 项目 | 位置 |
| --- | --- |
| API | `http://encore.inner.imdomestic.com:8790` |
| Minecraft 当前域名 | `<room>.chorus.internal:25565` |
| 生产配置 | encore `/var/lib/chorusd/config.toml` |
| 数据 | encore `/srv/chorus` |
| 用户 token | encore `/var/lib/chorusd/secrets/users.toml` |
| 本地服务端仓库 | `/Users/linwhite/chorus-testbed` |
| 本地客户端仓库 | `/Users/linwhite/ruri-chorus` |
| encore Git remote | `encore:/home/linwhite/git/chorus-testbed.git` |

源码目前读取 HCHogan/chorus 的 main，后续仓库更换要求见 [REPOSITORY-MIGRATION.md](REPOSITORY-MIGRATION.md)。生产归档通过专用账号 `chorus-archive` 同步到 tank，带宽限制为 `5M`；远端摘要与大小验证成功后才允许淘汰热缓存。

## 客户端

打开 `Ruri Chorus.app`，登录 Microsoft 账号，在设置中检查 Chorus 地址。API token 已存入独立钥匙串。服务器页面可以启动 main-fabric；Git 页面可以构建指定提交并启动临时房间。

应用内置 CLI 可查询当前状态：

```sh
"/Users/linwhite/ruri-chorus/build/Ruri Chorus.app/Contents/Helpers/ruri-cli" chorus rooms
"/Users/linwhite/ruri-chorus/build/Ruri Chorus.app/Contents/Helpers/ruri-cli" chorus builds
```

完整客户端构建、测试和登录验收说明位于 ruri-chorus 的 `CHORUS.md` 与 `CHORUS-VALIDATION.md`。

## 开发与部署

Go 单元测试从 `controller/` 执行 `go test ./...`，集成测试使用 `-tags integration`，连接真实组件；临时目录与 Go 缓存放在仓库 `.work/` 下。Fabric 模组位于 `hop-mod/`，世界扫描工具位于 `tools/world-scanner/`。

系统配置由 `imdomestic/nix-config` 管理；应用由仓库根目录的 Nix flake 构建并安装到 `/nix/var/nix/profiles/chorus`。系统部署执行构建、dry-activate、定时恢复、test、新 SSH 检查与持久切换流程。具体说明见 [部署说明](deploy/README.md)。

用户凭据和签名密钥由文件路径提供；凭据内容不得写入命令参数、日志、文档或 Git。旧 Minecraft 文件、归档和服务 masked 状态保持保留。
