# vex-action

`mode: build` 读取工作目录中的 `vex.toml`，收集全部声明的 Fabric、NeoForge 文件，下载并核验依赖，上传一个完整 GitHub Actions artifact。构建命令由调用方 workflow 执行。Action 使用独立 Go 模块，在运行时编译；`actions/setup-go` 管理模块和构建缓存，依赖版本与摘要保存在 `go.mod`、`go.sum`。

```yaml
permissions:
  contents: read
steps:
  - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
    with:
      ref: ${{ github.event.pull_request.head.sha || github.sha }}
      persist-credentials: false
      clean: false
  - uses: actions/setup-java@b6effb05e454b25005698d916606bdc6ffcbf961
    with:
      distribution: temurin
      java-version: '25'
      cache: gradle
  - run: ./gradlew build --no-daemon
  - uses: imdomestic/vex-action@v0
    with:
      mode: build
      artifact: vex-build
```

`artifact` 必须与服务端项目的 `ci.artifact` 相同。每次运行上传一次包含全部 loader 的目录，目录根包含 `vex-build.json`，文件位于 `files/<loader>/<filename>.jar`。单文件限制为 256 MiB，全部文件限制为 2 GiB、2048 个文件。所有文件携带 SHA1、SHA256、SHA512；依赖必须在 manifest 声明 SHA1 和 SHA512。

文件 glob 支持 `*`、`?`、`**`、字符集合和替代集合，使用 `/` 作为分隔符；相对路径和全部父目录必须留在工作目录。符号链接、重复文件名称、大小写冲突以及空选择会立即报错。`exclude` 中包含 `/` 的模式匹配项目相对路径，其余模式匹配文件名称。

依赖只通过 HTTPS 下载，拒绝本地网络地址、凭据、查询参数和不安全重定向。下载后核验声明的全部摘要。artifact 包含依赖实际字节；服务端导入只读取 bundle 文件。构建 SHA、PR、workflow 与 run attempt 均由服务端从 GitHub API 取得。平台 agent 由服务端注入。

本地执行：

```sh
mkdir -p .work/tmp
TMPDIR="$PWD/.work/tmp" go test ./...
TMPDIR="$PWD/.work/tmp" go build -o .work/vex-bundle .
.work/vex-bundle --root /absolute/project --manifest vex.toml --output .work/vex-bundle
```

`testdata/vex-build.json` 与 Vex controller 中的格式向量保持逐字节相同。独立 Action 可生成完整 bundle，再由 controller 的真实 Git/SQLite 导入测试验证两种实现处理相同字节。

本项目采用 [MIT 许可证](LICENSE)。
