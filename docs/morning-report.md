# 2026-10-15 晨报

## 1. 一句话结论

P1 与 P2 的 encore 验收通过，独立 Ruri Chorus 已完成真实模组安装与启动计划验证；正在进行资产与归档验收。

## 2. 阶段表

| 阶段 | 状态 |
| --- | --- |
| P1 | 通过 |
| P2 | 部分：encore 验收通过；调度与失联验收待 seraph |
| P3 | 部分：自动验证通过；真实进入待用户登录 |
| P4 | 部分：Git 页、旧提交准备与启动计划、真实失败展示通过；真实进入待用户登录 |
| P5 | 未开始 |
| P6 | 未开始 |

## 3. 需要你做的事

- 提供 hank 的 Minecraft 用户名；其 token 已生成，位于 encore 的 `/var/lib/chorusd/secrets/users.toml`，按 users.hank 项安全交付。
- 在 Ruri Chorus 登录 Microsoft 账号，随后进行 P3/P4 真实进入验收。
- 在 Headscale 配置 `chorus.internal → 100.64.0.43` 的 split DNS，随后切回目标 zone。
- 提供 chorusd 访问 tank 的生产 SSH 身份；当前生产同步关闭，文件全部保留。
- 让 seraph 加入集群并进行调度、失联验收；今晚保持现有系统配置。

## 4. 我替你做的决定

- P1 使用用户目录中的验收数据和 systemd user 构建服务；P2 将完整数据迁入 `/srv/chorus`。
- SSE 使用 EventSource 1.4.0；独立应用的 API token 使用专属钥匙串服务。

## 5. 异常与风险

Mac 无免密码 sudo，临时域名已验证解析到 encore。seraph 可访问但尚未加入 k3s。真实游戏启动次数：3/8；测试实例已成功进房。
