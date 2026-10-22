# 开发说明

## 测试环境不得依赖被测试项目

Vex 的代码、测试、脚本与生产数据的可用性，都不能依赖任何被测试项目的历史、内容或分支状态。被测试项目改写历史、删除分支或重新开始，Vex 都必须继续正常工作：

- 记录在创建时写入之后需要的全部内容（提交元数据与当时解析的清单），之后的显示、启动、恢复、mrpack 导出与世界提升只读取记录。镜像只用于取得新的提交并构建。
- 测试不读取任何实际项目仓库、镜像、Build 或项目数据目录。需要 Git 的测试在测试过程中自建仓库；需要 Build 的测试用固定测试文件经真实 CI bundle 导入得到 Build（`controller/internal/testproject`）。
- 测试与运行脚本不出现具体项目的仓库、提交或 Build。GitHub 集成测试使用运维通过环境变量指定的专用测试仓库。

## 固定测试文件

`controller/internal/testfixture/fixtures.json` 列出全部固定测试文件及其 SHA-256 与大小；测试在使用前逐个校验。

| 位置 | 内容 |
| --- | --- |
| `controller/internal/testfixture/testdata/` | 示例模组 `vex-example-fabric-1.1.1.jar`、平台 agent `vex-agent-26.3-1.0.0.jar`、小型 26.3 世界 `world-26.3.tar.gz`、Minecraft 26.3 版本数据 `minecraft-26.3-version.json` |
| encore `/srv/vex-fixtures/v1`（只读，root 所有） | `fabric-api-0.161.0+26.3.jar`、`neoforge-26.3.0.45-beta-universal.jar`、较大的 26.3 世界 `world-26.3-large.tar.gz`（归档中断测试需要至少 512 KiB） |

外部文件通过 `VEX_IT_FIXTURES` 指定目录；缺少该变量时相关测试跳过。新增或更换固定文件时同时更新 `fixtures.json`，旧文件保留在原版本目录，新内容使用新的版本目录。

## 测试命令

```sh
cd controller
go build ./...
go vet ./...
go test ./...
go test -tags integration -run '^$' ./...
test -z "$(gofmt -l .)"
```

集成测试使用 `integration` 构建标签，在 encore 上以独立 namespace、数据目录与端口运行。常用环境变量：

| 变量 | 含义 |
| --- | --- |
| `VEX_IT_FIXTURES` | 外部固定测试文件目录，即 `/srv/vex-fixtures/v1` |
| `VEX_IT_CONFIG` | 只包含平台设置的运维配置（运行后端、节点、Java 运行时、world scanner）；不包含项目 |
| `VEX_IT_WORK` | 已忽略的证据目录，路径必须包含 `/.work/` |
| `VEX_IT_RUN_ID` | 1–8 位小写字母与数字，区分测试项目 ID 与 namespace |
| `VEX_IT_KUBECONFIG`、`VEX_IT_HTTP_BIND`、`VEX_IT_NODE` | Kubernetes 凭据、Pod 可达的测试 API 地址、运行节点 |
| `VEX_IT_TANK`、`VEX_IT_IDENTITY`、`VEX_IT_KNOWN_HOSTS` | rsync-ssh 归档测试的 `/srv/archive/vex/it-*` 目标与凭据；tank 上的测试文件不删除 |
| `VEX_IT_SCANNER`、`VEX_IT_CLI`、`VEX_IT_BINARY` | 当前构建的 world scanner、`vex` 与 `vexd` |
| `VEX_IT_GITHUB_*` | GitHub App 与专用测试仓库；修改 GitHub 状态的测试还需要 `VEX_IT_GITHUB_MUTATIONS=1` |

部署、系统切换保护与真实组件测试见 [部署说明](../deploy/README.md)。
