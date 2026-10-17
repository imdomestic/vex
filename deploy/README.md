# Vex 构建与部署

应用 flake 输出 `.#vex`，包含 `vexd`、`vex`、`vex-world-scanner`。系统由独立的 `imdomestic/nix-config` 管理，通过 `nixosModules.vexd` 引入本仓库模块。应用 profile 默认 `/nix/var/nix/profiles/vex`，服务配置默认 `/etc/vex/vexd.toml`。

全部中间文件、Go 缓存和日志位于源码目录的 `.work/`。Nix 构建使用 `NIX_BUILD_DIRECTORY`，默认 `/nix/var/nix/builds`；部署前确认 Nix daemon 允许并能够使用该目录。脚本使用 Bash、Git、Go、Nix、jq、curl、coreutils、util-linux、Python 3 和 Kubernetes CLI；DNS 检查另外使用 dig。

## 源码与应用构建

从干净的已提交工作目录同步至目标主机上的全新目录。目标父目录必须存在；脚本拒绝覆盖既有目录，仅传输 Git 已跟踪的内容。

```sh
REMOTE_SOURCE="/home/linwhite/vex-source-$(git rev-parse --short HEAD)"
bash deploy/sync-source.sh encore "$REMOTE_SOURCE"
```

在同步后的 Linux 目录运行：

```sh
bash deploy/build-controller.sh
bash deploy/build-application.sh
```

第一条命令通过 PATH 中的 Go 检查格式、运行 vet 与全部普通测试，并生成 `.work/bin/vexd`、`.work/bin/vex`；`GO` 可指定完整可执行文件路径。格式检查只读。第二条命令构建 Nix 应用并保存 `.work/application/candidate` 与 GC root。更新依赖后，可运行 `bash deploy/nix-vendor-hash.sh` 获取 vendor hash，再使用文件编辑工具更新 `nix/vendor-hash`。

## 配置与应用 profile

安装前准备完整 Vex 配置、独立 `vexd` / `vex-build` 账户、只读全局构建锁、数据目录与凭据文件。配置检查使用候选产物中的实际 `vexd` 和共用配置解析器。

| 环境变量 | 默认值或用途 |
| --- | --- |
| `DEPLOY_HOST` | 当前主机名，对应 nix-config 中的主机输出 |
| `DEPLOY_CONFIG` | `/etc/vex/vexd.toml` |
| `DEPLOY_PROFILE` | `/nix/var/nix/profiles/vex` |
| `DEPLOY_BUILD_LOCK` | `/var/lib/vex/build.lock`，须与配置完全一致 |
| `DEPLOY_API_URL` | `http://127.0.0.1:8790` |
| `NIX_BUILD_DIRECTORY` | `/nix/var/nix/builds` |

```sh
candidate=$(cat .work/application/candidate)
sudo -n -u vexd "$candidate/bin/vexd" config check --config /etc/vex/vexd.toml
bash deploy/install-application.sh prepare "$candidate"
```

`prepare` 在 vexd 尚未运行时安装应用 profile。已有运行服务的应用更新使用 `deploy` 参数；脚本取得构建锁、确认全部 worker 已退出、正常停止 vexd、安装 profile、启动服务，并验证公开 `/v2/health` 返回正常状态和匿名 `/v2/me` 返回 401。脚本不会停止 Minecraft 房间。

worker 单元为 `vex-build@<project>--<buildId>.service`，读取 `<data_dir>/jobs/<project>--<buildId>.json`，独立账户只运行 Gradle。每次构建使用 `<data_dir>/projects/<project>/work/<buildId>/attempt-<n>/source`，任务文件校验相同的 attempt。Gradle 环境设置 `CI=true`，CPU、内存和 worker 数量继续受配置及 systemd 单元限制。vexd 负责 Git checkout、SQLite 状态、产物收集和三个文件摘要；停止 worker 后清理对应源码目录，清理失败会明确记录。已迁移的 Gradle 缓存必须具有 `vex-build` 组的读写权限；凭据、配置中的密钥文件和 SQLite 数据目录保持独立权限。

平台 agent 使用明确的源文件、安装目标和 SHA256：

```sh
bash deploy/install-agent.sh "${AGENT_SOURCE:?设置已验证的 agent JAR 路径}" "${AGENT_TARGET:?设置配置引用的安装路径}" "${AGENT_SHA256:?设置已验证的 SHA256}"
```

配置的 `agents.<id>.file` 指向安装目标；项目 loader 通过 `agent = "<id>"` 引用。已经发布的 Build 持有独立产物，agent 更新由新的 Build attempt 采用。

## 系统切换与 SSH 检查

在目标主机使用独立 nix-config checkout 构建候选：

```sh
bash deploy/build-system.sh "${NIX_CONFIG_SOURCE:?设置独立 nix-config checkout 的绝对路径}"
```

审阅 `.work/activation/dry-activate.log`、`closure-diff.log` 和 `candidate-system`。默认保护 sshd、tailscaled、k3s、mysql、postgresql 的服务单元内容；通过 `DEPLOY_PROTECTED_SERVICES` 指定该主机实际需要保持的完整服务列表。已有 masked service/socket 必须继续 masked，且新 SSH 检查要求它们 inactive。

Mac 上设置目标主机对应的参数，再传入已经审阅的系统路径：

```sh
export DEPLOY_HOST=encore
export DEPLOY_DNS_SERVER="${DNS_SERVER:?设置 DNS 地址}"
export DEPLOY_DNS_NAME="${ROOM_HOST:?设置实际房间域名}"
export DEPLOY_DNS_EXPECTED="${ROUTER_ADDRESS:?设置实际路由地址}"
bash deploy/activate-from-mac.sh encore "${REMOTE_SOURCE:?设置远端 Vex 源码路径}" "${REMOTE_NIX_CONFIG:?设置远端 nix-config 路径}" "${EXPECTED_SYSTEM:?设置已审阅的完整 Nix store 系统路径}"
```

脚本重新构建并确认候选路径一致，检查受保护服务和构建空闲状态，安排 600 秒自动恢复任务后执行 `test`。每一步使用禁用 SSH 连接复用的新连接。新连接检查节点 Ready、运行服务、配置、健康接口、认证边界和指定的 DNS UDP/TCP 结果。默认检查服务为 sshd、tailscaled、k3s、mysql、postgresql、vexd、vex-dns；主机配置不同则通过 `DEPLOY_REQUIRED_SERVICES` 提供完整列表。

只有候选系统一致且新 SSH 验证时间不超过 120 秒，才能停止恢复 timer，随后更新持久系统 profile 并执行 `switch`。新 SSH 检查失败会返回非零状态并保留恢复任务；恢复脚本将运行系统与持久系统恢复至先前 generation。持久化完成后再次检查运行路径、持久系统路径、服务和健康接口。

`DEPLOY_DNS_SERVER`、`DEPLOY_DNS_NAME`、`DEPLOY_DNS_EXPECTED` 三项共同设置时执行 DNS 验证。未设置时，该次检查不包含 DNS；使用上述部署命令时应填写实际部署值。

## 实际组件验收

Kubernetes 基础集成只使用明确提供的 kubeconfig 和隔离运行 ID；脚本不导出管理员凭据。测试会建立 `vex-p-vex-it-*` namespace 并清理自身资源，不启动生产房间。

```sh
export VEX_IT_KUBECONFIG="${INTEGRATION_KUBECONFIG:?设置已有受限 kubeconfig 路径}"
export VEX_IT_RUN_ID="$(date -u +%H%M%S)"
bash deploy/test-kube.sh
```

真实世界目录发布使用 CLI 集成测试，显式指定实际服务、token 文件、项目、世界 ID 和源目录。测试会发布世界，随后验证 tar.gz 重复发布幂等、列表、revision 以及实际下载的 SHA1/SHA256/SHA512。

```sh
export TMPDIR="$PWD/.work/temp"
export GOCACHE="$PWD/.work/go-cache"
export GOMODCACHE="$PWD/.work/go-modcache"
export VEX_IT_URL="${INTEGRATION_API_URL:?设置实际 Vex 服务地址}"
export VEX_IT_TOKEN_FILE="${INTEGRATION_TOKEN_FILE:?设置实际 token 文件路径}"
export VEX_IT_PROJECT="${INTEGRATION_PROJECT:?设置测试项目 ID}"
export VEX_IT_WORLD_ID="${INTEGRATION_WORLD_ID:?设置世界 ID}"
export VEX_IT_WORLD_SOURCE="${INTEGRATION_WORLD_SOURCE:?设置源世界目录}"
cd controller
go test -tags=integration ./cmd/vex -run '^TestLiveWorldDirectoryPublication$' -v
```

`TestLiveBuildManifestDownloadsAndCache` 使用同样的 URL、token 和 project 参数，并增加 `VEX_IT_BUILD_SHA`、`VEX_IT_LOADER`，校验实际 ready Build 的三个摘要、modIds、Gradle 日志和缓存复用。真实本地产物可通过 `VEX_IT_AGENT_JAR`、`VEX_IT_NEOFORGE_JAR` 执行 build 包中对应的 JAR 解析集成测试。

`verify-state.py <database> [--quiescent]` 只读检查 SQLite 和所有记录、counter 的项目前缀。`verify-room-mods.py --database <database> --project <id> --room <id>` 根据实际 Pod 标签选取房间，校验 generation、Build、全部服务端 JAR 名称、大小和三个摘要；Kubernetes 访问使用已有 `KUBECONFIG`。`test-dns.sh` 检查实际解析器；`test-pod-network.sh <固定摘要镜像> <v2健康地址>` 用临时 Pod 检查真实网络。

首次环境搭建的历史资料保存在 [bootstrap 归档](../docs/reports/bootstrap/README.md) 和 [部署脚本归档](../docs/reports/archive/deploy/README.md)。当前系统迁移和真实验收结果记录在 [P1 报告](../docs/reports/2026-10-17-vex-p1.md)。
