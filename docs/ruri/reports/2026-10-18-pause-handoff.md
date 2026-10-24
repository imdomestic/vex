# 2026-10-18 暂停与接续记录

用户要求本轮保存进度后暂停，2026-10-20 继续。暂停期间不发起新的构建、验收、游戏或 GitHub 授权。

## 本机签名包装

- 命令：`/Users/linwhite/.local/bin/ruri-build-local debug --build-system swiftbuild`。
- 实现：`/Users/linwhite/worklog/ruri-local-signing/build.py`；说明：同目录 `README.md`，下载副本为 `~/Downloads/RURI-LOCAL-SIGNING.md`。
- 产物：`/Users/linwhite/ruri-vex-experimental/build/Ruri.app`；`/usr/local/bin/ruri` 指向其中的 CLI。
- Team `59ZC6MZ73T` 的 Apple Development 证书签名、八个组件的权限和 flags、主程序与 CLI 相同的 designated requirement、deep strict 验证全部通过。
- Minecraft 26.3/Fabric 0.19.5 在 Hardened Runtime 下实际进入主菜单；JIT 与 LWJGL/SDL native 调用已确认，正常退出码为 0。
- 两次连续重建后的 Vex whoami、account list、Microsoft refresh 均成功。锁屏下 whoami 用时 1.214 秒、account list 用时 0.340 秒，结束时仍为锁定状态，没有检测到钥匙串授权进程。
- 仓库的 `signing.py`、`build-app.sh`、`scripts/lib/build.sh` 与 `RuriSigning.cer` 保持原样。

## 已保存的服务器进度

- Vex 本地仓库：`/Users/linwhite/chorus-testbed`，分支 `feat/vex-p2`。应用实现提交 `3fac605`；真实 fork 审批验收与报告提交 `56a7802a68c3aae2d42123aaf3f21c1bc81188f8`，已经同步 origin 与 encore。尚未合入 dev。
- `56a7802` 的完整 Go build、vet、tests、集成测试编译和格式检查在 encore 独立目录通过。完整历史扫描覆盖 29 个提交、692 个 blob、349 个当前文件、68 个归档成员与六种编码的可读字符串；实际凭据与未分类候选均为零。证据 `.work/vex-source-audit-20261018T122102Z/review.json`。
- 生产应用 profile 指向 `/nix/store/7j85yn0z413hva8aw6ggqxvn0rj7qk34-vex-applications`，对应 `3fac605`；正式地址 `https://vex.inner.imdomestic.com:8790`。
- S1 的公开 CA 证书、热加载、内部 HTTP 来源限制均已完成实测。nix-config main 已推送到 `85dc1b9`。
- example 的 CI 产物、双 generation 世界/PVC 保留、连接唤醒、四种不合格 artifact 拒绝、真实个人 fork 的维护者批准流程均已验证。详细结果见 `docs/reports/2026-10-18-vex-p2.md`。
- chorus CI PR #1 已通过真实构建。用户已通知 hank，正在等待 hank 合并；生产 chorus 继续使用 host 构建。

## Ruri 工作目录

- 路径：`/Users/linwhite/ruri-vex-experimental`；分支 `feat/vex-mrpack`。独立模块、通用宿主接口、HTTPS、MRPack 和本轮取消处理均已保存并暂存；index tree 为 `80134f20b14a77924b8b9ca8e83831dbdc4ddcbc`，本轮源码尚未提交或推送。详细命令见 `2026-10-20-ruri-vex-resume.md`。
- 最近完整离线测试通过 635 项；之后的请求取消状态处理与恢复修改经过四项定向测试，包括正式 HTTPS。最终源码仍需执行完整测试、本地化、两种构建与扫描后才能提交。
- 暂停时两项尚在运行的 Nix 构建已终止，已完成的缓存保留；最新源码的两种 Nix 构建结果均待接续验证。此前完成的两种构建证据保存在独立模块报告中。
- 真实租约测试在正式 HTTPS 请求进行中取消续租，仍收到完整回执并释放两份租约；生产 SQLite 只读检查确认对应记录均为零。证据 `.work/vex-c-lease-cancellation-final.log`、`.work/vex-c-lease-database-verification.json`。本项验证租约管理组件，没有等待完整 Coordinator 的 120 秒定时周期。
- 正式验收数据：`.work/vex-real-it`。Microsoft 登录与正式 encore 的钥匙串会话保留。用户已授权唯一验收账号的原版 Ruri `.account-locks` 锁文件操作。
- 当前玩家游戏启动次数为 4/8；第 5 次 GUI PR 进入尚未执行。
- Prism 用户已完成 Microsoft 登录，独立目录为 `.work/prism-mrpack-it`，最新 main 包实例为 `instances/example-main-fabric(1)`。两个 JAR 与 `servers.dat` 均与正式包逐字节一致，窗口已正常关闭。Prism、HMCL 和关闭 Vex 的 Ruri 已通过实际包导入；Prism/HMCL 的实际进入尚未执行。HMCL 独立目录位于 Vex 仓库 `.work/vex-p2/launchers/hmcl-import-compatible/`；详细接续事项为该仓库 `.work/vex-p2/prism-hmcl-paused-2026-10-18.md`。

## 继续执行的顺序

1. 先读取两个仓库当前状态和本报告，保留期间用户的文件修改；检查运行服务及房间状态。
2. 在 Ruri 仓库完成最后源码的完整验证：

   ```sh
   env RURI_BUILD_DIR=.work/build TMPDIR="$PWD/.work/temp" \
     scripts/swift-build.sh test --build-system swiftbuild \
     --no-parallel --enable-swift-testing
   /Users/linwhite/.local/bin/ruri-build-local debug --build-system swiftbuild
   ```

3. 使用原生 AX 属性和控件操作完成 GUI 进入 example PR；原生 AX 已能连续读取当前页面。System Events 递归读取菜单时的持续布局问题尚需确认实际影响，不能据此宣称全部 GUI 验收通过。
4. 同一 Microsoft 账号按顺序验证 Ruri、Prism、HMCL，前一个游戏正常退出后再启动下一个。Prism/HMCL 必须导入目标 PR 当前 generation/build 的正式包；main 包的导入证据不能代替 PR 进房。
5. 完成 GUI 与 CLI GitHub Device Flow 的真实授权。未关联账号与无仓库写权限账号的负向操作仍需要实际账号持有人参与。
6. hank 合并 chorus PR 后，再切换 chorus 的 CI 来源，验证下一轮 generation 的世界保留；不代为合并该 PR。
7. 验证通过后，对新增部分和准备推送的完整分支历史执行密钥扫描，再按提交门槛提交与推送。`STATE-REPORT.md` 不加入暂存区；本机签名包装不提交到 Ruri 仓库。

## 用户待办

- 等待 hank 合并 [chorus CI PR #1](https://github.com/imdomestic/chorus/pull/1)。
- 决定公开 `vex-action` 的 LICENSE。
- 继续时完成具体 GitHub Device Flow 授权；HMCL 的 Microsoft 登录尚未执行。
- 提供实际未关联 GitHub 账号和无仓库写权限账号参与相应负向验收，或另行裁定这两项的验收范围。

## 暂停收尾

- 本机 App 注册监听 18793 已停止；第二 GUI 服务 8792 已停止并移除临时放行规则。
- 临时测试 namespace 与 Job 已清理。两个 main PVC 保留且 Bound：chorus `c583ce8c-07f8-4aa1-b054-8db34357eb6d`，example `c4fa6bb7-af46-4c3e-8c7d-741e35b084c4`。
- 本轮 Go 编译缓存已清理，Mac 可用空间约 22 GiB；用户其他文件和独立工作目录保持原样。
- Ruri 已停止发起新验收，最后一次本地签名包装通过，GUI 正常关闭。example/pr1-fabric 已正常停止，generation 1、players 0；正式 HTTPS 健康检查返回 ok。两个 main PVC 保留。
- 独立 API 测试会话已退出并撤销，私密测试会话文件已删除。正式 GUI 的 Vex 会话、Microsoft 登录和 Prism 登录保留。当前没有本轮玩家游戏、Swift 编译或临时注册监听在运行。
