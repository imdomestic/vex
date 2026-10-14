# 接口契约裁决索引

十项接口事项已依据 [2026-10-15 执行指令](directives/2026-10-15-decisions.md)完成裁决。[docs/api.md](api.md) 是 chorusd、Ruri Chorus 和其他调用者唯一的接口依据。本文保留事项编号与实现阶段的对应关系；字段、路径、状态、错误和请求响应统一查阅 API 文档。

| 项目 | 已确定的契约 | API 位置 | 实现阶段 |
| --- | --- | --- | --- |
| 1. 构建列表 | GET /v1/builds，支持 sha、loader、status 过滤；缓存淘汰后的查询范围明确 | §2.2、§3.2 | P1 |
| 2. Build 字段与重建 | 固定阶段字段、attempt、时间、lastError、modIds；force 创建新尝试时重置字段；引用中的成功构建受保护 | §2.2、§3.2 | P1 |
| 3. SSE 与就绪 | 每次连接首条 sync，之后实时事件；ready 由 Pod Ready 与路由 SLP 身份核验共同确定；客户端按当前状态进入 | §4.2、§6 | P1 |
| 4. 固定房间的新周期 | generation 持久递增，closed ID 可再次启动；周期身份贯穿租约、token、Kubernetes、MOTD、快照和异步结果 | §2.3、§4 | P1，生命周期由 P2 完成 |
| 5. branch 种子 | 依次取最新成功快照、源房间 seedResolved、empty；源房间不存在返回404；只在 tank 的快照由 start 自动恢复 | §5 | P1；tank 恢复由 P6 完成 |
| 6. 资产 revision 下载 | 固定 revision 的世界下载路径，Bearer 或签名 URL，完整大小和 SHA-256 校验 | §3.3 | P5 |
| 7. 发布对象与 revision | 资产 revision 为每资产递增的整数字符串；相同最新世界摘要幂等；requires.namespaces 与 Build.modIds 校验；资源包 revision 为 ZIP SHA-256 | §2.4、§7 | P5 |
| 8. hop 与租约 | 明确 HopDestination；限定目的地；同 build 和资产依赖检查；start/hop 返回 room、lease | §2.6、§4.4、§8 | P5 |
| 9. Snapshot 与恢复 | 固定快照对象、snapshot seed、幂等上传、持久化恢复尝试，以及 start 自动恢复后的状态 | §2.5、§5、§9 | P6；P2 使用快照及上传结构 |
| 10. 资产恢复与归档 | 固定 latestRev 和资源包恢复，校验完成才 active；发布时登记同步任务，归档前完成房间停止和 tank 校验 | §7.4、§10 | P6；P5 使用发布事务与任务登记结构 |

所有状态冲突使用 HTTP 409 和 `conflict`；`invalid_request` 用于 HTTP 400。首次接入某个对象时就使用 API 中已确定的 generation、revision、modIds 和恢复字段。阶段验收进度记录在 [progress.md](progress.md)。

后续接口调整先修改 [api.md](api.md)，再记录 [decisions.md](decisions.md)，实现与测试共同使用更新后的契约。
