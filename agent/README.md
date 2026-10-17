# Vex Agent

Fabric 服务端模组，Minecraft 26.3、Java 25、Fabric Loader 0.19.5、Fabric API 0.161.0+26.3。mod ID 为 `vex_agent`，Java 包为 `com.imdomestic.vex.agent`。客户端通过原版 transfer 数据包切换同一项目中兼容的房间。

`/server` 显示可点击的目的地；`/server world:plaza` 和 `/server room:main-fabric` 发起传送。目的地启动期间每 2 秒读取 Room，每 120 秒更新加入租约，总等待上限为 300 秒。project、generation、buildId 与目标一致且 state=ready 时，在服务器主线程发送传送数据包。网络请求在虚拟线程执行；离线、失败、目标周期变化与等待超时会取消未使用的租约。

必需环境变量为 `VEX_PROJECT`、`VEX_ROOM_ID`、`VEX_BUILD_ID`、`VEX_GENERATION`、`VEX_CONTROLLER_URL`、`VEX_ROOM_TOKEN`。token 只进入 HTTP Authorization 标头。模组启动时检查配置，HTTP 请求禁止自动跳转。项目路由为 `/v2/projects/{project}/hop`、`/hop/destinations`、`/rooms/{room}` 和租约删除端点。每次请求的完整响应限时 10 秒、限量 1 MiB，同时受 300 秒总等待期限约束；主线程发送之前再次检查期限，取消后排队的发送任务不会执行。

构建使用 Gradle Wrapper 9.7.1。设置项目内的 `TMPDIR`、`JAVA_TOOL_OPTIONS=-Djava.io.tmpdir=<项目工作目录>` 和 `GRADLE_USER_HOME`，执行 `./gradlew --no-daemon --max-workers=2 build`。生产构建通过既有 systemd 构建模板限制 CPU 与内存，并串行使用 Gradle。

`build` 同时执行 JUnit 的就绪状态、终止状态、project、generation 与 buildId 验证，以及真实 Brigadier 对带冒号目的地的解析和非法目的地拒绝测试。实际传送验收使用真实 Minecraft 玩家和 vexd。

产物为 `build/libs/vex-agent-26.3-1.0.0.jar`。operator 在 `agents.<name>` 中登记 Minecraft、loader、version 和 file，并由项目 loader 配置的 `agent` 选择该项。controller 将该文件作为 `side=server` 的平台注入文件纳入 Build manifest，统一通过 Build 文件路由分发。P1 保持 `/server` 命令与传送行为；大厅、委托和账号绑定属于 P4。
