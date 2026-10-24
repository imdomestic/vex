# Vex 构建 bundle

`vex-action` 是独立 Go 模块。项目 workflow 完成自己的构建后，Action 读取该提交的 `vex.toml`，收集每个 loader 的产物、下载并校验全部依赖，生成以下文件。一次 artifact 包含清单中全部 loader。

- `vex-build.json`
- `files/<loader>/<filename>.jar`

`vex-build.json` 使用 UTF-8 JSON，schema 为 1：

```json
{
  "schema": 1,
  "minecraft": "26.3",
  "java": 25,
  "loaders": {
    "fabric": {
      "loader_version": "0.19.5",
      "files": [
        {
          "name": "example.jar",
          "path": "files/fabric/example.jar",
          "side": "both",
          "size": 3453,
          "sha1": "小写十六进制，40 个字符",
          "sha256": "小写十六进制，64 个字符",
          "sha512": "小写十六进制，128 个字符",
          "origin": "build"
        }
      ]
    }
  }
}
```

示例中的摘要说明由实际文件摘要替换。`loaders` 可包含 `fabric`、`neoforge`；每个 loader 使用自己的版本和文件集合。文件 `side` 为 `client`、`server` 或 `both`。`origin` 为 `build` 或 `dep`；`dep` 必须另有 `url` 字段，与提交清单里的依赖 URL 一致，且实际字节包含在 artifact 中。`build` 文件不得提供 URL。

平台根据提交清单的 `agent=true` 注入服务端 agent，发布后的 BuildFile 将其标记为 `origin=agent`。输入 bundle 不允许自行提供 agent；vexd 解析 jar 元数据后拒绝 `vex_agent` 模组 ID。

## 权威信息与导入

GitHub API 决定提交 SHA、head 仓库、事件、workflow、run ID、run attempt 和关联 PR。bundle 不包含这些身份字段。vexd 从相应 Git 提交读取 `vex.toml`，提交不存在该文件时读取 operator 指定的 `manifest_fallback`。

导入必须满足提交清单的 Minecraft、Java、loader 版本、文件使用侧以及依赖集合和摘要。Java 必须在 operator 配置的 runtimes 内，loader 必须启用，房间内存必须满足配额。世界额外根目录项和房间内存均来自经过校验的提交清单，随后写入 Build 元数据。

vexd 只读取 artifact 中的实际文件，对每个文件重新计算大小、SHA1、SHA256、SHA512，并解析 Fabric/NeoForge 模组元数据。bundle 和清单中的依赖 URL 仅用于校验来源信息，vexd 导入流程不会请求这些 URL。

同一 SHA 的 Build ID 为 `<loader>-<sha12>`。接受新的 CI run 或 run attempt 时增加构建 attempt；同一个 run 的重复导入保持幂等。更早的 run 不会覆盖较新的产物。同一 SHA 的多个 PR 可以关联同一 Build；已有房间引用时，只有所有文件内容、使用侧、目标版本、房间内存和世界允许项均相同，才能复用原有 attempt。不同内容返回 conflict。

## 文件与资源边界

| 项目 | 限制 |
| --- | --- |
| ZIP 文件大小 | 最大 2 GiB |
| 全部解压内容 | 最大 2 GiB |
| 单个 jar | 1 字节至 256 MiB |
| 文件数量 | 最大 2048 个产物文件 |
| JSON 元数据 | 最大 4 MiB |
| 解压前磁盘余量 | 至少 15 GiB 加本次解压内容大小 |

拒绝绝对路径、`..`、反斜线、NUL、符号链接、特殊文件、加密 ZIP 条目、重复路径、大小写冲突、未列出的文件、缺少的文件、未知 JSON 字段及尾随 JSON 数据。每个文件路径必须精确等于 `files/<loader>/<name>`，文件名称限定为安全的 ASCII jar 名称。任何摘要、大小或模组元数据错误都会终止导入，不发布 ready Build。

Action 使用成熟 TOML 和 glob 库。收集路径及父目录必须位于工作目录内，符号链接会被拒绝；每条文件规则必须至少匹配一个产物。依赖同时要求 SHA1、SHA512，可另提供 SHA256。下载仅允许公开 HTTPS、443 端口和安全重定向，拒绝凭据、查询参数以及解析到本地、LAN、tailnet 或 link-local 的地址。

## 共同向量与实际验证

两个仓库保存逐字节相同的 `testdata/vex-build.json`：主仓路径为 `controller/internal/build/testdata/vex-build.json`，独立 Action 路径为 `testdata/vex-build.json`。向量来自真实 example main 提交 `3cf4f9d4009a8a9e46810096e9e9366c8b2e8095` 的 3453 字节 jar，记录其三种实际摘要。

控制器的 `TestSharedBundleMatchesActionSource` 使用 `VEX_IT_ACTION_SOURCE` 指向独立 `vex-action` 工作目录，逐字节比较两个仓库中的向量。`TestActionBundleUsesSameManifestAndActualBytes` 使用 `VEX_IT_ACTION_GO` 指定从该目录构建的 Go bundler，收集两个 loader，比较结构及字节，然后使用真实 Git 和 SQLite 完成导入。两个变量应同时设置。Action 的 `TestActualProductionJarMatchesSharedVector` 可通过 `VEX_IT_VECTOR_JAR` 指定原始 jar，逐项验证共同向量。Action 源码统一保存在 `imdomestic/vex-action`。

客户端指纹采用 UTF-8 紧凑 JSON，字段顺序固定为 `minecraft`、`loader`、`loaderVersion`、`sha512`。最后一项为全部客户端文件的 SHA512 去重集合，按 ASCII 升序排列；服务端专用文件不进入集合。对该 JSON 字节计算 SHA256，小写十六进制结果写入 `clientFingerprint`。名称、下载地址、文件排列顺序不参与指纹。
