# 2026-10-20 暂停与接续记录

用户要求保存进度后暂停。暂停期间不启动构建、游戏、授权或验收；等待用户明确继续。

## 工作目录与分支

| 内容 | 位置与状态 |
| --- | --- |
| Vex | `/Users/linwhite/chorus-testbed`，`feat/vex-p2`；HEAD `eeb2492685aeeeb7e88f3bbeb917e6fd37cec768`。当前修复已暂存；暂存树记录位于 `.work/vex-oct20/vex-staged-tree.txt`。 |
| Ruri | `/Users/linwhite/ruri-vex-experimental`，`feat/vex-mrpack`；HEAD `61dc1df78cd961714f2034b751940495bea3f4e1`，暂存树 `286a09df0c61fa309af0337e1d688d4ca59b5b9e`。详细接续记录为 `docs/ruri/reports/2026-10-20-ruri-vex-resume.md`。 |
| 实验应用 | `/Users/linwhite/ruri-vex-experimental/build/Ruri.app`。最新本机证书签名包装成功，应用完整。 |
| Ruri 验收数据 | `/Users/linwhite/ruri-vex-experimental/.work/vex-real-it`。 |
| 本机签名包装 | `/Users/linwhite/.local/bin/ruri-build-local`；实现位于 `/Users/linwhite/worklog/ruri-local-signing/`，不提交到 Ruri。 |
| 基础设施工作目录 | `/Users/linwhite/chorus-testbed/.work/vex-oct20/nix-config`。仅 `flake.lock` 的 Vex 输入修改到 `c53d038`，尚未提交。 |
| 证据 | Vex 的 `.work/vex-oct20/` 与 Ruri 的 `.work/vex-oct20-*`。 |

`STATE-REPORT.md` 和 `deploy/github-app-setup.py` 不加入提交。代码恢复使用文件编辑，不使用 Git 恢复命令。中间文件继续放在忽略的 `.work/`，不使用 `/tmp`。

## 本轮已完成的验证

- 用户决定 `vex-action` 使用 MIT。公开仓库 `imdomestic/vex-action` 的 `main` 已推送 `bbee986ecdc7860b25764d0e0275e2cd3adc776d`；完整 Go 测试、构建和全部历史扫描通过，GitHub 已识别许可证。Vex 中的 `action/` 同步提交为 `eeb2492`，尚未推送。
- 正式 `vex login --github` 经用户授权成功，`whoami` 返回 linwhite/admin。退出后，验收会话与本地凭据文件均已删除；实际数据库和服务日志的 GitHub token 检查为零。证据：`.work/vex-oct20/device-cli-{whoami,privacy}.json`。
- Ruri GUI 的真实 GitHub 授权成功，已验证身份读取。GUI 正常退出该会话后，本机条目已删除。
- Ruri 对独立 HTTPS 8792 服务的真实 GitHub 授权返回 `account_not_linked`；该服务没有用户身份映射，客户端未保存会话。
- 真实未授权 Device Flow 等待 888.008 秒并到达服务端截止时间，超时处理通过。实际取消操作也已验证。
- Ruri 在本次钥匙串删除修改之前通过启用组件的 638 项测试、关闭组件的 593 项测试、本地化 4048 项检查，以及两种 Nix 构建。关闭组件的四份 CLI golden、包内资源、依赖、符号和 Info.plist 检查通过。
- Vex 当前路由修复的完整 Go build、vet、普通测试、集成测试编译、格式检查通过；Nix 应用构建通过，候选为 `/nix/store/sd5j15vbvj9ff7isyvw426rnapcmfgw7-vex-applications`。
- encore 的真实休眠路由测试通过，耗时 64.75 秒。覆盖权限拒绝、直接来源拒绝、限速、实际 Pod 调度、SLP 身份验证、停止归档、PVC 保留与关闭后拒绝唤醒。证据：`.work/vex-oct20/wake-encore-o20e1.log`。
- 完整密钥扫描通过：30 个提交、693 个历史文件对象、353 个当前文件、68 个归档成员及六种编码的可读字符串。实际凭据和未分类候选均为零，扫描期间文件未变化。证据：`.work/vex-source-audit-20261020T073903Z/review.json`。

## 已保存、尚待验收的代码

### Vex 的跨节点路由恢复

当前实现通过已有的 Kubernetes Pod exec，在实际房间中执行 `mc-monitor status -json`，核对 project、room、build、generation 后恢复 Service selector；最终 ready 仍要求经 mc-router 的 SLP 验证。游戏端口的 NetworkPolicy 只允许受管理的 mc-router Pod。源码中的 vexd 使用原有地址族权限。

修改位于 `controller/internal/runtime/kubernetes/{client,wake,project}.go`、相关真实测试、`controller/internal/slp/slp.go` 和 `nix/module.nix`，尚未提交或部署。

seraph 第一轮测试在初始世界下载阶段超时。实测原因：验收 HTTP 随机端口没有正式 8790 所具备的来源保留和防火墙规则；宿主请求成功，seraph Pod 请求超时。Minecraft 尚未运行，因此该轮没有完成路由恢复验收。

下次使用固定端口 `10.1.2.137:8793`。临时 Nix 配置位于 `.work/vex-oct20/temporary-system.nix` 与 `temporary-seraph.nix`。encore 的文件仍包含已经完成验收的 8792 放行项与 `vex-auth-validation` 表；继续时先用文件编辑删除这些部分，只保留 8793 的限定来源规则，再重新构建。现有临时候选不能直接激活。部署前核对实际系统状态，并执行保护切换流程。禁止中断 encore 的网络服务。

待执行命令：

```sh
sudo -n bash /home/linwhite/vex-work/.work/vex-oct20/runtime-regression/run-wake-two-node.sh seraph o20s2 8793
```

该命令在 encore 执行，使用真实 example CI 产物、独立镜像和真实 seraph Pod。必须先确认前一轮测试资源已清理、8793 规则已生效。

### Ruri 的钥匙串删除

GUI 创建的 Vex 会话可以由 GUI 正常删除；CLI 读取成功，但原有删除请求返回 `-25244`。当前修改先查找当前服务器对应的精确条目引用，要求唯一结果，再使用 `kSecMatchItemList` 删除。

此修改已通过定向测试和本机稳定签名包装。真实的双向跨进程删除、最新源码完整测试和最终扫描尚未执行；正式 Ruri CLI GitHub 登录也尚未开始。继续时先读取 `Sources/RuriVex/VexSettings.swift` 的实际内容和 Ruri 接续报告。

交接时的只读检查还确认：两个 Device Flow 联网测试和 lease 联网测试尚未共同要求 `VEX_IT_URL`，需要按 R1 补齐离线跳过条件。现有 lease 测试验证请求取消和租约清理，实际等待 120 秒后触发自动续租的路径尚未经过真实计时验收。

仓库中的 `signing.py`、`build-app.sh`、`scripts/lib/build.sh` 与 `RuriSigning.cer` 保持原样。禁止用临时测试程序写入钥匙串。

## 客户端游戏验收

已核实累计玩家游戏启动 **5 次**：P1 进房三次、独立 SDL 正常退出验收一次、Hardened Runtime 主菜单验收一次。10 月 20 日没有启动游戏。继续时保守沿用 8 次上限，三个客户端各保留一次；开始前检查是否存在新增会话。两项独立游戏的证据为 Ruri `.work/sdl-native-quit/.work/game-exit.json` 与 `.work/vex-c-title-finished.json`。

Prism 和 HMCL 已原生导入实际 `example/pr1-fabric` 的 generation 2、Build `fabric-901a48fd73d0`、attempt 1 整合包；版本、两个 JAR 与 `servers.dat` 均已验证。用户已完成两个启动器的 Microsoft 登录。

- 正式包：Ruri `.work/vex-c-real-packs/example-pr1-fabric.mrpack`。
- 包 SHA-256：`28297a744f20ffee64503326e6c827c14c0e40eff9c07060d23a7a7e1255275c`。
- 服务器地址：`pr1-fabric--example.vex.internal:25565`。
- Prism 隔离目录：Ruri `.work/prism-mrpack-it`。
- HMCL 隔离目录：Vex `.work/vex-p2/launchers/hmcl-import-pr`。

继续时先验证生产房间状态、generation 和 Build，必要时重新导入当前包。顺序为 Ruri GUI PR 进入、正常退出、Prism 实际唤醒与进入、正常退出、HMCL 实际进入。三个启动器使用同一 Microsoft 账号，严格依次执行。每次确认实际服务端加入日志、SLP 和运行版本。

## 提交、部署与后续步骤

1. 读取本报告与两个仓库当前文件，保留用户暂停期间的修改。
2. 核对两台机器、临时规则、应用 profile、房间和 PVC。生产应用仍为 `/nix/store/7j85yn0z413hva8aw6ggqxvn0rj7qk34-vex-applications`，对应 `3fac605`；新候选尚未部署。
3. 完成 seraph 的真实路由恢复测试。通过后，对准备提交的最终内容再次扫描，提交 Vex 修复并同步 origin 与 encore。
4. 从确切提交重新构建应用；应用 profile 和系统配置都按保护流程部署，nix-config 输入使用同一提交。临时 8792/8793 规则用完清理。
5. 完成 Ruri CLI GitHub 登录、钥匙串真实双向删除、最新源码的完整验证和上述游戏验收。
6. 当前代码尚未满足合入 `dev` 的全部门槛，保持功能分支。
7. 等待 hank 合并 chorus PR #1；随后才切换 chorus 的 CI 来源并验证世界保留。无仓库写权限账号的 fork 评论拒绝测试仍需真实账号持有人参与。

系统切换仍需 build、dry-activate、600 秒保护计时器、test、从 Mac 新建 SSH 验证、通过后 switch。新提交推送前扫描完整历史、文件对象、归档成员、字符串和提交信息；不输出凭据。

## 暂停收尾

- Ruri GUI、Prism、HMCL 均已正常退出；没有本轮玩家游戏、授权轮询或构建继续运行。最后一次本机签名包装正常完成。
- 两个真实测试 namespace 及关联 PV 已清理，测试进程已经退出。生产 main 和 PR 的三个 PVC 保留。
- 独立 8792 服务已停止，MainPID 为零，端口监听消失；只读数据库检查通过，会话、会话 token、邀请和全部 records 均为零。独立数据目录保留。
- Ruri 的独立 API 验收会话已经正常退出并撤销，两份私密会话文件已删除。Ruri 的正式 Vex 登录已退出，Microsoft 登录保留；继续时需要重新完成正式 Vex 登录。
- encore 已通过保护切换移除临时 8792 放行规则，运行系统与持久系统均为 `/nix/store/byd1yvrajcwrnq79157g8r4xfxd83jrj-nixos-system-encore-26.05.20260911.21a67dc`。8793 规则没有激活，保护计时器全部停止。生产应用仍为 `3fac605`，系统当前保留 `AF_NETLINK` 权限，待下一次应用与模块同时升级时调整。
- seraph 本轮没有切换系统，当前为 `/nix/store/s1i7jkdj2wrzpcchfvz08cjf7w05sy3d-nixos-system-seraph-26.05.20260911.21a67dc`。两个节点 Ready，正式服务健康；encore 根分区剩余 67,887,390,720 字节。
- example PR 房间已正常停止，generation 2 快照 `pr1-fabric-g2` 已生成并通过 tank 校验。三个 StatefulSet 均为 0/0，没有运行中的 Minecraft Pod；三个原 PVC UID 保持且均为 Bound。停止操作使用的短期管理会话已注销。
- 基础设施接续记录位于 `.work/vex-oct20/infrastructure-handoff.md`；最终证据为同目录的 `pause-health.log`、`pause-room-after.json`、`pause-room-stop.log` 与 `cleanup-8792-*.log`。

完整结果另见 `docs/reports/2026-10-20-vex-p2-validation.md` 和 `docs/ruri/reports/`。

下载目录中的接续入口为 `/Users/linwhite/Downloads/VEX-RESUME-2026-10-20.md`，报告副本集中在 `/Users/linwhite/Downloads/vex-progress-2026-10-20/`。
