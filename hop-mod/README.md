# Chorus Hop

Fabric 服务端模组，Minecraft 26.3、Java 25、Fabric Loader 0.19.5、Fabric API 0.161.0+26.3。客户端通过原版 transfer 数据包切换房间。

`/server` 显示可点击的目的地；`/server asset:plaza` 和 `/server room:main-fabric` 发起传送。目的地启动期间每 2 秒读取 Room，每 120 秒更新加入租约，总等待上限为 300 秒。generation、buildId 与目标一致且 state=ready 时，在服务器主线程发送传送数据包。网络请求在虚拟线程执行；离线、失败、目标周期变化与等待超时会取消未使用的租约。

必需环境变量为 `CHORUS_ROOM_ID`、`CHORUS_BUILD_ID`、`CHORUS_CONTROLLER_URL`、`CHORUS_ROOM_TOKEN`。token 只进入 HTTP Authorization 标头。模组启动时检查配置，HTTP 请求禁止自动跳转。每次请求的完整响应限时 10 秒、限量 1 MiB，同时受 300 秒总等待期限约束；主线程发送之前再次检查期限，取消后排队的发送任务不会执行。

构建使用 Gradle Wrapper 9.7.1。设置项目内的 `TMPDIR`、`JAVA_TOOL_OPTIONS=-Djava.io.tmpdir=<项目工作目录>` 和 `GRADLE_USER_HOME`，执行 `./gradlew --no-daemon --max-workers=2 build`。共享 encore 时使用 `/srv/chorus-build.lock` 的只读文件描述符取得排他 flock，并把整个构建放入 MemoryHigh=5G、MemoryMax=6G、CPUQuota=200% 的 systemd 单元。

`build` 同时执行 JUnit 的就绪状态、终止状态、generation 与 buildId 验证，以及真实 Brigadier 对带冒号目的地的解析和非法目的地拒绝测试。实际传送验收使用真实 Minecraft 玩家和 chorusd，执行记录由项目晨报保留。

产物 `build/libs/chorus-hop-26.3-1.0.0.jar` 放到 `<data_dir>/mods/chorus-hop/26.3/`。该目录配置一个 jar，controller 为后续 Fabric Build 加入 `side=server` 的固定下载条目。已经发布的 Build manifest 保持原样；需要该模组的构建使用新的 attempt。
