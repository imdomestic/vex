# P0：Chorus Fabric 构建

核对日期：2026-10-14。运行主机：encore。

## 来源与任务

来源为 `HCHogan/chorus` 的 `main`，提交 `fc16eaac252bc8404e99a2368fecd768d31c8067`。独立克隆位于 `/home/linwhite/chorus-testbed-p0/.work/chorus-upstream`。

产物任务为 `:fabric:assemble`，包含 Minecraft 编译依赖准备、common 与 Fabric 编译、资源处理、正式 jar、Javadoc jar 和源码 jar。实际执行前已检查任务依赖。选择依据见 [decisions.md](decisions.md)。

## 实际命令

在克隆目录执行：

```sh
export JAVA_HOME=/nix/store/dasnfgbvb4nff68czbr0461hl924nbh7-openjdk-25.0.4.1+1
export PATH="$JAVA_HOME/bin:$PATH"
export TMPDIR=/home/linwhite/chorus-testbed-p0/.work/temp
export GRADLE_USER_HOME=/home/linwhite/chorus-testbed-p0/.work/gradle-home
export JAVA_TOOL_OPTIONS="-Djava.io.tmpdir=$TMPDIR -XX:ActiveProcessorCount=2"
bash ./gradlew :fabric:assemble --rerun-tasks --console=plain --no-daemon \
  --max-workers=2 \
  "-Dorg.gradle.jvmargs=-Xmx1536m -XX:ActiveProcessorCount=2 -Djava.io.tmpdir=$TMPDIR" \
  "-Porg.gradle.java.installations.paths=$JAVA_HOME" \
  -Porg.gradle.java.installations.auto-download=false
```

命令通过独立的 `chorus-p0-fabric-assemble.service` 用户临时服务运行。systemd 资源设置为 `MemoryAccounting=true`、`MemoryHigh=5G`、`MemoryMax=6G`、`CPUQuota=200%`、`RuntimeMaxSec=20min`。

## 构建结果

- 退出码 `0`，Gradle 输出 `BUILD SUCCESSFUL`，`16 actionable tasks: 16 executed`。
- 此次完整产物任务重新执行耗时 `30.211` 秒，进程组内存峰值 `2G`，CPU 时间 `58.206` 秒。
- Minecraft 解编译与重新编译结果在本次任务中命中缓存。此前使用相同处理器参数生成这批真实编译依赖耗时 `136.54` 秒，记录到整个进程组峰值 `5,237,403,648` 字节，约 `4.88 GiB`。30.211 秒对应有 Minecraft 编译依赖缓存的结果。
- GNU time 的 RSS 仅覆盖其直接统计范围；容量判断使用上述 systemd cgroup 峰值。
- 构建后 `git status --porcelain` 为空，`git diff --exit-code` 成功。
- 工具会话 `92308` 已返回终态；临时服务为 `inactive/dead`。

## 产物与依赖

`fabric/build/libs/` 有三个 jar，排除 `*-sources.jar` 和 `*-javadoc.jar` 后恰好剩下一个正式产物：

| 文件 | 字节数 | SHA-256 |
| --- | --- | --- |
| `chorus-fabric-26.3-26.3.0.0.jar` | `2669907` | `d4434c5ca66274f1c3612a6e92f0547e5b576b4b1a9e2ed1a860a3382c3881ce` |
| `fabric-api-0.161.0+26.3.jar` | `2620967` | `86f16178a3cecc887a85a4cfe9a79d92fa7341d8f39b5951a4d6ad800ab657a6` |

Fabric API 从其官方 Maven 地址实际下载到独立 `.work/deps/`。通过 `unzip -p` 读取两份 jar 内的 `fabric.mod.json`：chorus 要求 Loader `>=0.19.5`、MC `~26.3`、Java `>=25` 与 Fabric API；所下载 Fabric API 声明 MC `~26.3-`、Java `>=25`、Loader `>=0.19.3`。

两份 jar 的安装范围为 `both`。chorus-hop 属于后续 P5 的服务端产物，当前尚未构建。

## 验收边界与证据

此结果证明真实源码可以生成 Fabric 产物。itzg 镜像内启动、在线模式与白名单、客户端进入需要继续执行 P0 / P1 的真实运行验收。

远端日志位于 `/home/linwhite/chorus-testbed-p0/.work/logs/`，本地副本位于 `.work/p0-build/`。所有 jar、日志、Gradle 缓存均位于忽略目录，未进入版本控制。
