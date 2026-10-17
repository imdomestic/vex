# 2026-10-15 晨报

## 1. 一句话结论

chorusd 已运行，main-fabric 与 plaza 已完成真实进入和往返传送，tank 中断续传及世界恢复通过；独立 Ruri Chorus 等待首次登录。

## 2. 阶段表

| 阶段 | 状态 |
| --- | --- |
| P1 | 通过 |
| P2 | 部分：encore 验收通过；调度与失联验收待 seraph |
| P3 | 部分：构建、144 项回归与 41 项附加检查通过；真实进入待用户登录 |
| P4 | 部分：Git 图、旧提交启动计划与失败展示通过；真实进入待用户登录 |
| P5 | 通过；资源包 N/A |
| P6 | 通过；生产同步等待用户提供 SSH 身份 |

## 3. 需要你做的事

- 提供 hank 的 Minecraft 用户名，并安全交付 encore `/var/lib/chorusd/secrets/users.toml` 中的 `users.hank`。
- 确定 chorus-testbed 与 ruri fork 的 GitHub 远端位置。
- 打开下载目录中的 `ruri/Ruri Chorus.app`，登录 Microsoft 账号；在服务器页进入 main-fabric，在 Git 页启动旧提交。通过后按 P3→P4 顺序将两个功能分支合入 ruri 的 dev。
- 在 Headscale 配置 `chorus.internal → 100.64.0.43`；通过 `ssh encore 'sudoedit /var/lib/chorusd/config.toml'` 把 `minecraft.zone` 改为 `chorus.internal`，再执行 `ssh encore 'sudo systemctl restart chorusd'`。
- 审阅下载目录中 d2-map-dev 的说明与补丁；对应 encore 仓库分支为 `feat/chorus-publish`。
- 提供 tank 生产 SSH 账号、身份文件及允许访问的目录，随后配置并启用归档；当前同步关闭，热缓存全部保留。
- 将 seraph 加入 k3s，再执行房间调度与节点失联验收。

## 4. 我替你做的决定

- 构建使用固定 systemd 模板与全局文件锁；SSE 使用 EventSource 1.4.0 和专属钥匙串。
- Minecraft 26.3 使用直接 Java 启动与 SDL 正常退出请求；Tailscale 故障测试使用独立 LAN 控制和定时恢复保护。

## 5. 异常与风险

05:12 JST 收尾检查通过。临时 zone 为 `100-64-0-43.sslip.io`；根盘可用 32.22 GiB，两房间加一次构建时最低可用内存 10.80 GiB。游戏启动 4/8 次，全部会话已结束；临时房间与测试命名空间已清理，main-fabric 停止并保留 PVC。

Tailscale、两个监控 timer 与生产服务均已恢复。tank 保留 `/srv/archive/chorus/` 下的 `it-20261014T190016Z-archive/`（4 个文件）、`it-20261014T200226Z-tailscale/`（2 个文件）、`it-20261014T200706Z-tailscale/`（4 个文件）。
