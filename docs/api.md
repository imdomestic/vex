# vex API v2

本文是 vexd、vex CLI、Ruri 实验性 Vex 功能和 vex-agent 的接口契约，描述 P1 当前实现。路由注册位于 `controller/internal/api/routes.go`，数据模型位于 `controller/internal/model/model.go`。修改接口时同时修改本文和相关测试。

## 通用约定

- JSON API 路径以 `/v2` 开始，邀请说明页使用 `/i/{code}`。项目资源的前缀为 `/v2/projects/{p}`，下文端点表用 `P` 表示这个完整前缀。`{p}` 是项目 ID；对象 ID 只在项目内唯一。客户端缓存和对象索引必须包含服务器和项目 ID。
- JSON 字段使用 camelCase。日期采用 RFC 3339；服务端产生的时间使用 UTC。字段表中的 `?` 表示响应值可以为 `null`；`SeedResolved.id/rev` 和 `Error.details` 没有值时省略。
- 普通 JSON 请求最多读取 1 MiB，拒绝未知字段和多个顶层 JSON 值。只有明确注明可空的端点接受空请求体；其余请求提供表中的 JSON 对象。JSON 响应带 `Cache-Control: no-store`。
- 列表直接返回数组。除提交历史使用 `before` 以外，当前列表没有分页游标。
- SHA 使用小写十六进制。提交 SHA 为完整的 40 个字符；文件 SHA-1、SHA-256、SHA-512 分别为 40、64、128 个字符，均根据实际入库字节计算。
- `url`、`log` 是 API 相对路径，可相对于 `Meta.publicBaseURL` 使用；`path` 是项目数据目录内的相对路径，供归档与文件定位使用。公开客户端使用 HTTPS，房间 Pod 可以通过受限的内部 HTTP 入口下载文件。
- JAR、世界归档、资源包分别返回 `application/java-archive`、`application/gzip`、`application/zip`；文件响应支持 HTTP Range。日志返回 `text/plain; charset=utf-8`，已隐藏 Bearer 凭据和 URL 的 `sig/token` 值。
- 项目、世界和资源包 ID 采用 `^[a-z][a-z0-9-]{0,22}$`，禁止 `--` 和末尾 `-`。房间主机名为 `<roomId>--<project>.<zone>`，组合后的第一个 DNS label 最长 63 个字符。

## 认证和权限

### 监听器与 TLS

`server.listeners` 是 `{address,tls,scope}` 数组。`scope` 必须为 `public` 或 `internal`；TLS 监听器使用 `server.tls_cert_file` 与 `server.tls_key_file` 指定的绝对路径。旧的 `server.listen` 地址列表仍受支持，配置证书时该列表全部启用 TLS；两种列表不能同时显式配置。默认 loopback 地址会被显式 listeners 替代。

服务启动时校验证书和私钥。每次完整 TLS 握手重新读取文件并检查匹配及有效期，文件变化不需要重启进程；无效的替换文件导致该次握手失败。SIGHUP 立即执行同样的校验并记录结果，NixOS 服务通过 `systemctl reload vexd` 提供入口。已有连接继续由原连接管理，API 和 SSE 不因证书重新读取而关闭。

`internal` 入口在路由鉴权前检查真实连接来源：本机访问、实际 mc-router Pod，以及所属项目 namespace 内的平台房间 Pod 和归档 Job Pod。房间和归档来源均核对实际所有者 UID、项目、房间、build、generation 与 ServiceAccount；归档 Job 还须匹配平台生成的名称。容器先于 kubelet 发布 Pod IP 发出请求时，等待真实 Kubernetes 状态更新后核验；所有项目并行检查，共用最多 10 秒的期限。无关 Pod 或其他机器返回 403，来源查询错误返回 500。请求的 Bearer、签名 URL 与项目权限仍须单独验证，转发头不改变来源判定。宿主防火墙和项目 NetworkPolicy 同时限制内部 HTTP 的网络访问。

`server.public_base_url` 决定公开元数据、邀请及对外文件链接的地址。切换 HTTPS 后，客户端使用新地址与重新签发的链接。服务器不通过明文入口重定向用户凭据。内部房间下载使用 `server.internal_base_url`，其地址和路由权限保持独立。

### 用户与房间身份

用户请求携带 `Authorization: Bearer <credential>`。P1 人工登录使用一次性邀请，成功后签发 32 字节随机数的 base64url 会话凭据，数据库仅保存 SHA256。静态 `token_sha256` 供服务账号使用，与会话凭据共用 Bearer 入口。会话每次通过认证后更新 lastUsedAt，并将 expiresAt 顺延 auth.session_ttl；吊销后立即失效。API 不返回凭据摘要。签名密钥从 `auth.signing_key_file` 读取。

邀请使用 8 位 Crockford base32，显示为 `XXXX-XXXX`。输入允许大小写、分组连字符及标准 O→0、I/L→1 别名。服务端只保存邀请码 SHA256，兑换操作和会话创建在同一个数据库事务内完成。邀请码默认有效期为 auth.invite_ttl；已兑换、未知、格式无效或过期均返回 `400 invite_invalid`。兑换按连接来源 IP 和整个服务进行两级限速，分别为每分钟 10 次、100 次，突发容量分别为 10、100；代理转发头不参与来源判定。超限返回 `429 rate_limited` 和 `Retry-After` 秒数。

公开兑换请求的 JSON 正文读取期限为 10 秒。会话与邀请的到期时间在取得 SQLite 事务并读取记录后核验；等待数据库事务不会延长过期凭据的可用时间。

`auth.admin_socket` 提供 Unix HTTP 管理入口，socket 权限为 0660，属组 vex-admin。它只注册邀请和用户会话管理端点，不对 TCP 监听公开额外管理路径。admin 和本机管理入口为尚未登记的用户创建邀请时只登记身份；admin 与项目角色由 operator 配置授予。身份、邀请和会话保存在保留的 `@platform` 作用域，项目查询不会包含这些记录。

同一 Kubernetes 集群中的独立服务实例使用不同的 `runtime.kubernetes.namespace_prefix` 和 `system_namespace`。默认值分别为 `vex-p-`、`vex-system`。前缀必须由合法 DNS label 和末尾 `-` 组成，长度最多 40 个字节；项目 namespace 为前缀加项目 ID。系统 namespace 为最多 63 个字节的 DNS label，不能等于该实例任何项目 namespace。独立实例还需要独立的数据目录、监听地址、zone 和管理 socket。

项目角色依次为 `viewer`、`developer`、`owner`。`admin` 可以访问所有配置项目，项目角色按 `owner` 计算。已认证用户请求不可见项目时返回 `404 not_found`；项目成员权限不足时返回 `403 forbidden`。无效或缺失的用户凭据返回 `401 unauthorized`。

| 路由权限 | 可接受的身份及约束 |
| --- | --- |
| `public` | 无需认证。 |
| `optional-user` | 无凭据时返回公开内容；提供无效凭据时返回 401；有效用户凭据返回完整内容。 |
| `user` | 任意已认证用户。 |
| `viewer` | 当前项目 viewer 及以上。 |
| `developer` | 当前项目 developer 及以上。 |
| `owner` | 当前项目 owner。 |
| `admin` | 平台 admin。 |
| `room` | 当前项目的有效房间凭据。 |
| `room-or-viewer` | 项目 viewer 及以上，或满足目标房间/租约检查的房间凭据。 |
| `download` | 项目 viewer 及以上、当前房间自己的构建凭据，或当前 URL 的有效签名。 |
| `developer-download` | 项目 developer 及以上，或当前 URL 的有效签名。 |
| `seed-download` | 项目 developer 及以上、房间固定种子匹配的房间凭据，或当前 URL 的有效签名。 |
| `pack-download` | 项目 viewer 及以上，或当前 URL 的有效签名。 |
| `internal` | mc-router 来源检查：连接的源 IP 同时符合配置的来源地址范围和运行后端确认的 router 来源。 |

房间凭据使用 HMAC-SHA256 签名，包含 `project`、`roomId`、`generation`、`buildId`、`expires`。除检查签名和过期时间，还必须与数据库中当前房间的项目、generation、buildId 匹配，且房间尚未 `closed`。房间凭据访问其他项目或需要用户认证的平台接口返回 404；访问同项目中未授予房间身份的端点返回 403。房间读取目标房间时，目标必须是自身或具有有效 hop 租约；租约必须匹配源房间和源 generation，以及目标 generation。

文件签名通过 `exp`、`sig` 查询参数传递，覆盖 GET 方法、URL 转义后的完整路径和到期时间，只在四种下载权限的路由有效。下载授权不会授予其他 API 权限。房间凭据不能下载任意项目构建或任意世界种子；快照和资源包使用已签名 URL 注入服务器。

## 端点

### 平台和 Git

| 方法 | 路径 | 权限 | 请求 | 成功响应 |
| --- | --- | --- | --- | --- |
| GET | `/v2/health` | public | 无 | 200 `{status:"ok"}` |
| GET | `/v2/meta` | optional-user | 无 | 200 公开 `PublicMeta` 或认证后的 `Meta` |
| GET | `/v2/me` | user | 无 | 200 `User` |
| POST | `/v2/auth/invites` | user | JSON `{user?:string,ttl?:duration}`；普通用户只允许自身，admin 可以指定用户 | 201 `{link,code,expiresAt}`，仅本次返回 |
| POST | `/v2/auth/invites/redeem` | public | JSON `{code:string,client:string}` | 200 `{token,user:User,expiresAt}` |
| GET | `/i/{code}` | public | 无 | 200 固定说明页；不检查或兑换邀请码 |
| GET | `/v2/me/sessions` | user | 无 | 200 `Session[]`，只包含当前用户未过期的会话 |
| DELETE | `/v2/me/sessions/{id}` | user | 无 | 204；仅能吊销自己会话，其他用户或未知 ID 返回 404 |
| POST | `/v2/auth/logout` | user | 无 | 204；吊销当前会话；服务账号令牌返回 409 conflict |
| GET | `/v2/projects` | user | 无 | 200 可见的 `Project[]`，按 ID 排序 |
| GET | `P` | viewer | 无 | 200 `Project` |
| GET | `/v2/nodes` | admin | 无 | 200 `Node[]` |
| GET | `/v2/events` | user | 可选查询 `project=p1,p2` | 200 SSE，见事件章节 |
| POST | `/v2/hooks/router` | internal | JSON `{event,timestamp,status,server,player?,backend?,client?,error?}` | 204；合并调和信号 |
| POST | `P/git/refresh` | developer | 无 | 204；完成远端拉取、分支登记及事件发布 |
| GET | `P/branches` | viewer | 无 | 200 `Branch[]` |
| GET | `P/commits` | viewer | 查询 `branch`、`before`、`limit` | 200 `Commit[]` |

`commits.branch` 默认使用项目默认分支；`limit` 默认 50，必须是正整数。顺序来自 `git rev-list --topo-order`。`before` 是上一页最后一个提交的完整 SHA，返回该提交之后的历史；游标不在所选分支历史中时返回 404。`parents` 包含完整父提交 SHA，`refs` 提供该提交的引用名称。

显式刷新同步等待 fetch，超时为 10 秒，失败时请求返回错误。启动尚未运行的分支房间也会同步 fetch；这一启动路径在 fetch 失败且具有缓存时继续使用缓存，并在 `phase` 标注“使用缓存的分支信息”。周期刷新频率由项目 Git 配置决定。新分支登记对应房间；分支推进后正在运行的房间保留当前构建，`behindHead=true`，下一次启动使用新 head。

邀请 ttl 使用 Go duration，必须大于零。client 为 1–200 个字符，禁止控制字符。GET 说明页正文固定为“请在 ruri 的 设置 → 实验性功能 → Vex 服务器 → 添加服务器 中粘贴这个链接，或执行 `vex login <链接>`”，响应不包含输入 code，聊天软件预取不会消耗邀请。创建及兑换响应使用 `Cache-Control: no-store`；会话 token 只在兑换成功时返回。长期 SSE 每 15 秒重新检查会话有效性并顺延有效期，吊销后结束该连接。

Unix 管理接口为 POST `/v2/auth/invites`（必须指定 user）、GET `/v2/admin/users/{user}/sessions`、DELETE `/v2/admin/users/{user}/sessions/{id}`。命令 `vexd user invite <name> [--ttl DURATION]`、`vexd user sessions <name> [--revoke ID]` 只通过这个 socket 请求，不直接修改数据库。

### 构建

| 方法 | 路径 | 权限 | 请求 | 成功响应 |
| --- | --- | --- | --- | --- |
| GET | `P/builds` | viewer | 可选查询 `sha`、`loader`、`status` | 200 `Build[]` |
| POST | `P/builds` | developer | JSON `{sha,loader,force?}` | 200 已有 `Build`；202 新构建或重建 |
| GET | `P/builds/{id}` | viewer | 无 | 200 `Build` |
| GET | `P/builds/{id}/log` | developer | 无 | 200 日志文本，最多读取 64 MiB |
| GET | `P/builds/{id}/files/{name}` | download | 无 | 200/206 指定 manifest 文件 |

`status` 允许 `queued/building/ready/failed`，`loader` 必须在项目内启用。列表按 `queuedAt` 降序，同时间按 ID 升序。Build ID 为 `<loader>-<sha12>`；完整 SHA 不同而短 ID 相同时返回 409 `conflict`。

构建来源固定为 `{kind:"host"}`；只有 `trust="trusted"` 且 `build.source="host"` 的项目可以提交构建，否则返回 `sandbox_unavailable`。`force` 默认 false：已存在的记录直接返回；queued/building 即使 force 也保持原任务。failed 可以显式 force 重建；ready 只有在没有未关闭房间引用时可以 force，受引用保护时返回 `conflict`。每个真正新建的执行尝试增加 `attempt`，Build ID 保持不变。

构建产物、解析的依赖和配置的 agent 全部出现在 `Build.files`。客户端下载 `side=both/client` 的文件；服务端注入 `side=both/server` 的文件。全部文件采用构建文件端点下载，manifest 提供三个摘要和字节长度。`modIds` 从模组元数据读取，用于世界兼容性检查。

### 房间和租约

| 方法 | 路径 | 权限 | 请求 | 成功响应 |
| --- | --- | --- | --- | --- |
| GET | `P/rooms` | viewer | 可选单值查询 `kind`、`state` | 200 `Room[]` |
| POST | `P/rooms` | developer | JSON `{kind,sha,loader,seed?,world?}` | 201 新 `Room`；200 已有 `Room` |
| GET | `P/rooms/{id}` | room-or-viewer | 无 | 200 `Room` |
| PATCH | `P/rooms/{id}` | developer | JSON `{label:"testing"\|"finished"}` | 200/202 `Room` |
| DELETE | `P/rooms/{id}` | owner | 无 | 204，保留 generation 墓碑 |
| POST | `P/rooms/{id}/start` | viewer | 可空，或 JSON `{seed?:string}` | 200/202 `StartResult` |
| POST | `P/rooms/{id}/stop` | developer | 无 | 200/202 `Room` |
| POST | `P/rooms/{id}/restart` | developer | 无 | 200/202 `Room` |
| DELETE | `P/rooms/{id}/leases/{lease}` | room-or-viewer | 无 | 204 |
| GET | `P/rooms/{id}/log` | developer | 无 | 200 日志文本 |
| GET | `P/rooms/{id}/events` | viewer | 无 | 200 SSE，仅房间及关联构建 |

`kind` 为 `branch/commit/world`；手动创建只接受 `commit/world`。branch 由 Git 同步登记。commit 房间 ID 为 `c<sha8>-<loader>`，world 房间为 `w-<worldId>-<sha8>-<loader>`；短 ID 冲突返回 `conflict`。创建只登记 stopped 房间；实际启动通过 start 请求进行。世界房间固定使用 `seed="world:<worldId>"`。

`StartResult` 为 `{room:Room,lease:Lease}`。每次受理 start 都发放新租约；活跃周期保持 generation，不重复启动进程。开始新周期时 generation 增加，已删除房间从独立墓碑继续计数。`accept_eula=false` 时 start 返回 409 `eula_not_accepted`。已 ready 房间会在响应前通过 router 进行 SLP 核验；匹配则返回 200；失败则返回 202、`state="starting"`、`phase="re-verify"`。其余受理情况返回 202。

`seed` 仅适用于 commit 房间。当前周期已有进程、已固定世界或仍保留世界时，修改种子返回 `conflict`。seed 表达式：

| 表达式 | start 时解析规则 |
| --- | --- |
| `empty` | 全新世界。 |
| `branch:<name>` | 查找同项目、同 loader 的分支房间；取最新已发布快照，其次取源房间的 `seedResolved`，最后取 empty。源房间缺失返回 404。 |
| `snapshot:<id>` | 固定指定快照，要求 Minecraft 版本匹配。 |
| `world:<id>` | 固定该世界当前 `latestRev`；要求 active、恢复完成且 Minecraft/loader 兼容。 |

解析结果写入 `seedResolved`。本地不存在的快照会使房间进入 queued，phase 为 `restoring seed`，自动恢复后继续启动；恢复失败写入 `archive_failed`，`details.snapshotId` 保留快照 ID。世界命名空间必须包含于 Build.modIds；失败返回或记录 `build_mismatch`，message 说明缺少的命名空间。

租约包含 `{id,generation,expiresAt}`，有效期来自 `rooms.join_lease`。客户端等待期间每 120 秒重新 start 续租，放弃或完成连接时删除自身租约。删除不存在的租约返回 204；用户只能删除自己创建的租约，房间只能删除由其当前源 generation 创建的 hop 租约。租约存在期间不开始空闲关闭计时。

stop 对 stopped/closed 房间返回 200；其他状态进入停止流程，返回 202。restart 在停止后开始新周期；stopped/closed 可以直接进入启动流程。lost 无法明确世界状态，stop/restart 返回 `node_lost`。label=finished 会取消自动重启并进入关闭流程；重新启动前需要将 label 设回 testing。

DELETE 仅删除当前项目已 closed、没有保留世界或待重启请求、没有有效租约、没有受保留规则保护快照的记录；否则返回 `conflict`。删除保留 generation 墓碑。commit/world 房间关闭超过 30 天且不受保留快照或世界引用保护时也可被自动清理。

### 世界、资源包和快照

| 方法 | 路径 | 权限 | 请求 | 成功响应 |
| --- | --- | --- | --- | --- |
| GET | `P/worlds` | viewer | 无 | 200 `World[]` |
| GET | `P/worlds/{id}` | viewer | 无 | 200 `World` |
| PATCH | `P/worlds/{id}` | owner | JSON `{state:"active"\|"archived"}` | 200/202 `World` |
| POST | `P/worlds/{id}/revisions` | developer | multipart，见下文 | 201 新 `WorldRevision`；200 相同内容 |
| GET | `P/worlds/{id}/revisions/{rev}` | viewer | 无 | 200 `WorldRevision` |
| GET | `P/worlds/{id}/revisions/{rev}/download` | seed-download | 无 | 200/206 tar.gz |
| POST | `P/worlds/{id}/promote` | developer | JSON `{snapshot:"<snapshotId>"}` | 201 新 `WorldRevision`；200 相同内容 |
| POST | `P/worlds/{id}/restore` | developer | 可空，或 `{}` | 200 本地就绪 `World`；202 已排队 |
| GET | `P/packs` | viewer | 无 | 200 `PackRevision[]` |
| POST | `P/packs/{id}/revisions` | developer | `application/zip`，最多 8 GiB | 201 新 `PackRevision`；200 已有内容 |
| GET | `P/packs/{id}/{file}` | pack-download | `{file}` 为 `<rev>.zip` | 200/206 ZIP |
| GET | `P/snapshots` | viewer | 可选查询 `room` | 200 `Snapshot[]` |
| POST | `P/snapshots` | room | 查询 `room/build/generation`，`application/gzip` | 201 新 `Snapshot`；200 已有相同快照 |
| GET | `P/snapshots/{id}` | viewer | 无 | 200 `Snapshot` |
| GET | `P/snapshots/{id}/download` | developer-download | 无 | 200/206 tar.gz |
| POST | `P/snapshots/{id}/restore` | developer | 可空，或 `{}` | 200 本地就绪 `Snapshot`；202 已排队 |

世界上传必须包含且仅包含两个 multipart 字段：`metadata` 的 Content-Type 为 `application/json`，内容为 `WorldMetadata`，最多 1 MiB；`world` 的 Content-Type 为 `application/gzip`，内容为世界 tar.gz。总请求最多 32 GiB + 1 MiB。metadata.id 必须匹配路径中的 ID。世界归档必须通过目录安全检查、Minecraft 版本检查和项目额外根目录规则；服务端扫描实际命名空间，若显式声明 namespaces 则必须与扫描结果一致。

每个世界独立使用递增十进制整数字符串 revision（`"1"`、`"2"`）。上传世界的 SHA-256 等于当前 latestRev 时返回原 revision；相同文件且 metadata 不同也保持已有 revision。否则新增 revision 并更新 latestRev。promote 使用目标世界的编辑快照，遵守相同的内容幂等规则。资源包 rev 等于 ZIP 的 SHA-256；GET 文件名必须是 64 个小写十六进制字符加 `.zip`。

快照列表按 capturedAt 降序、同时间按 ID 升序。上传最多 32 GiB；query 中的 room/build/generation 必须与当前有效房间凭据相符，归档由服务端校验。Snapshot ID 为 `<roomId>-g<generation>`，同 ID 内容不一致会冲突。快照记录实际保存时刻 capturedAt 和入库时刻 createdAt。当前周期的快照只有通过停止/归档身份与状态检查后才可发布。

`tankVerifiedAt` 表示归档后端已核验：快照文件完成核验时设置；世界 revision 的世界文件、元数据及所引用资源包全部核验后设置。字段名称同时用于 local 和 rsync-ssh 后端。restore 使用 `queued/running/succeeded/failed` 状态与 attempt；本地淘汰后通过 restore 拉回文件。归档或恢复失败通过 Error 表达。世界设为 archived 前要求全部关联文件完成归档核验，尚未完成时返回 `archive_failed`；设为 active 会执行恢复流程。

### 游戏内跳转

| 方法 | 路径 | 权限 | 请求 | 成功响应 |
| --- | --- | --- | --- | --- |
| GET | `P/hop/destinations` | room | 查询 `fromRoom` | 200 `HopDestination[]` |
| POST | `P/hop` | room | JSON `{fromRoom,dest,player:{uuid,name}}` | 200/202 `StartResult` |

源房间必须匹配房间凭据且处于 ready。player.uuid 必须是有效 UUID，name 非空且不含 NUL/换行。目的地为 `room:<roomId>` 或 `world:<worldId>`；裸 ID 仅在目标无歧义时受理。目标限制在同项目、同 buildId，世界还需要 Minecraft、loader 和命名空间兼容。

响应租约记录源房间、源 generation 和玩家 UUID。vex-agent 每 2 秒读取目标 Room，只有 project、id、generation、buildId 均匹配且 state=ready 才 transfer；等待期间按租约规则续约，300 秒超时。`HopDestination` 的 state 可以是实际房间状态；世界尚无房间时为 `available`。

## 数据模型

### 平台、Git 与构建

| 类型 | 字段 |
| --- | --- |
| `PublicMeta` | `name:string`、`version:string`、`auth:{methods:["invite"]}`。name 来自 server.name，默认主机名；version 为 `0.3.0`。 |
| `Meta` | PublicMeta 全部字段，以及 `zone:string`、`publicBaseURL:string`、`runtimes:{name:{java:int,serverImage:string}}`、`agents:[{minecraft,loader,version}]`。 |
| `User` | `id:string`、`admin:bool`、`minecraft:string[]`、`identities:string[]`、`projects:{projectId:role}`。 |
| `Session` | `id:string`、`user:string`、`client:string`、`createdAt`、`lastUsedAt`、`expiresAt`、`current:bool`。响应不包含凭据及摘要。 |
| `Project` | `id`、`remote`、`defaultBranch`、`role`、`minecraft`、`java:int`、`loaders:string[]`、`policy:{trust,buildSource,acceptEULA}`、`quota:{maxRooms,roomMemoryMax,buildCache,snapshotKeep,snapshotKeepDays}`。 |
| `Node` | `name`、`roles:string[]`、`priority:int`、`ready:bool`、`schedulable:bool`、`maxRunning:int`、`rooms:string[]`、`rootFreeBytes:int64?`。不可取得根目录可用容量时该字段为 null。 |
| `Branch` | `project`、`name`、`head`、`rooms:{loader:roomId}`、`builds:{loader:BuildSummary?}`。 |
| `Commit` | `project`、`sha`、`parents:string[]`、`subject`、`author`、`time`、`refs:string[]`、`builds:{loader:BuildSummary?}`。 |
| `BuildSummary` | `id`、`status`。 |
| `Build` | `project`、`id`、`source:{kind:"host"}`、`sha`、`loader`、`status`、`attempt:int`、`queuedAt`、`startedAt?`、`finishedAt?`、`minecraft:string?`、`loaderVersion:string?`、`java:int?`、`builtAt?`、`durationSec:number?`、`modIds:string[]`、`files:BuildFile[]`、`log:string`、`lastError:Error?`。 |
| `BuildFile` | `name`、`sha1`、`sha256`、`sha512`、`size:int64`、`side:"both"/"client"/"server"`、`url`、`path`。 |

### Room

| 字段 | 含义 |
| --- | --- |
| `project`、`id` | 项目 ID 与项目内房间 ID。 |
| `kind` | `branch/commit/world`。 |
| `branch:string?` | 分支房间的原始 Git 分支名称。 |
| `sha`、`loader`、`buildId` | 当前周期完整提交 SHA、loader 和构建 ID。 |
| `generation:int64` | 周期计数，创建记录时为 0 或墓碑保存值，每次新启动加一。 |
| `behindHead:bool` | 分支 head 已经超出当前周期 SHA。 |
| `host`、`port:int` | Minecraft 连接地址。 |
| `state`、`phase` | 机器可判定状态，以及展示用进度说明；客户端不得把 phase 当枚举使用。 |
| `label` | `testing/finished`；finished 阻止 start。 |
| `node:string?` | 当前选定的运行节点。 |
| `players:Player[]` | 当前 SLP 玩家样本，每项 `{name,uuid}`。样本数量可能少于实际在线人数。 |
| `idleSince:time?` | 无在线玩家且无有效租约时的空闲起点。 |
| `seed` | 请求的种子表达式。 |
| `seedResolved:SeedResolved?` | 当前周期固定种子，`{kind:"empty"/"snapshot"/"world",id?,rev?}`。 |
| `world:string?` | world 房间对应的世界 ID。 |
| `lastSnapshot:time?` | 最近快照的捕获时间。 |
| `lastError:Error?` | 最近房间错误。 |
| `createdAt`、`updatedAt`、`closedAt:time?` | 创建、更新与关闭时间。 |

房间状态及主要转移：

| 状态 | 条件和下一步 |
| --- | --- |
| `stopped` | 已登记或分支世界保留且进程停止；start 开始 queued。 |
| `queued` | 等待构建、种子恢复或容量；条件满足后 building/starting。 |
| `building` | 等待对应 Build ready；构建失败进入失败收尾。 |
| `starting` | 创建或复核服务器；进程 Ready 且 router SLP 身份匹配后 ready。 |
| `ready` | 没有玩家和有效租约持续到对应房间类型配置的空闲期限后 stopping；用户 stop/restart/finished 同样进入停止流程。 |
| `stopping` | 保存世界并停止进程；有世界则 archiving，完成后 stopped/closed。 |
| `archiving` | 发布本周期快照；分支保留世界，临时房间成功归档后可以释放世界。 |
| `closed` | 本周期结束；label=testing 可以 start 新周期，或在满足保护规则后删除记录。 |
| `failed` | 构建、版本、恢复或启动失败；先保存诊断日志并停止进程，保留世界。可处理错误后重试 start。 |
| `lost` | 所在节点持续不可用至少 60 秒；节点恢复后，根据存活进程与身份重新核验或停止归档。 |

进程仍 Ready 而 SLP 不通时保持 starting，phase 为 `route unavailable` 并重试。进程未 Ready 超过启动期限才触发启动失败。Room.state=ready 的条件包括 router 返回的 MOTD 中 project、room、build、generation 全部匹配。

### 世界、资源包与归档

| 类型 | 字段 |
| --- | --- |
| `Requirements` | `loaders:string[]`、`mods:string[]`、`namespaces:string[]`。兼容检查使用 Minecraft 和 loaders，并要求 mods 与 namespaces 都包含于 Build.modIds。 |
| `PackRef` | `{id:string,rev:string}`。 |
| `Source` | `repo:string`、`commit:string`、`scale:number`、`notes:string`。 |
| `WorldMetadata` | `id`、`title`、`minecraft`、`requires:Requirements`、`resourcePack:PackRef?`、`source:Source`。 |
| `World` | `project`、`id`、`title`、`state:"active"/"archived"`、`latestRev`、`minecraft`、`requires`、`resourcePack:PackRef?`、`restore:WorldRestore?`。 |
| `WorldRevision` | WorldMetadata 的全部顶层字段，另加 `project`、`rev`、`snapshotId:string?`、`world:WorldFile`、`createdAt`、`tankVerifiedAt:time?`。 |
| `WorldFile` | `sha1`、`sha256`、`sha512`、`size:int64`、`url`、`path`。 |
| `PackRevision` | `project`、`id`、`rev`、`sha1`、`sha256`、`sha512`、`size:int64`、`url`、`path`、`createdAt`。 |
| `Snapshot` | `project`、`id`、`roomId`、`roomGeneration:int64`、`buildId`、`minecraft`、`world:PackRef?`、`capturedAt`、`createdAt`、`sha1`、`sha256`、`sha512`、`path`、`size:int64`、`localAvailable:bool`、`tankVerifiedAt:time?`、`restore:Restore?`。Snapshot.world 表示世界 ID 和 revision。 |
| `Restore` | `attempt:int`、`status`、`requestedAt`、`startedAt?`、`finishedAt?`、`error:Error?`。 |
| `WorldRestore` | Restore 的全部字段，加 `revision:string`。 |
| `HopDestination` | `project`、`dest`、`kind`、`id`、`title`、`roomId:string?`、`buildId`、`state`。 |

## SSE 与客户端进入条件

全局 SSE 默认订阅用户全部可见项目；`project` 查询参数可以用逗号选择其中的项目，任何不可见项目返回 404。单房间 SSE 固定在一个项目和房间内，随后跟踪该房间当前引用的 Build。每次成功连接第一条事件始终为 sync。

| 事件名 | JSON data | 发送时机 |
| --- | --- | --- |
| `sync` | `{project,projects:string[],rooms:Room[],builds:Build[],snapshots:Snapshot[],worlds:World[],packs:PackRevision[]}` | 每次连接的初始数据库快照。单项目时 project 为该 ID，多项目时为空字符串；每个对象始终有自身 project。单房间订阅只含该 Room 和已存在的当前 Build，其他三个数组为空。 |
| `room` | 完整 Room | 房间记录新增或改变。 |
| `build` | 完整 Build | 构建记录新增或改变。 |
| `ready` | `{project,id,generation,host,port,buildId}` | 变更后的 Room 为 ready，随 room 事件发送。 |
| `failed` | `{project,id,generation,error:Error?}` | 变更后的 Room 为 failed，随 room 事件发送。 |
| `snapshot` | 完整 Snapshot | 快照记录新增或改变。 |
| `world` | 完整 World | 世界记录新增或改变。 |
| `pack` | 完整 PackRevision | 资源包 revision 新增或改变。 |
| `room_deleted` | `{project,id}` | 房间记录被删除。 |
| `build_deleted` | `{project,id}` | 构建记录被删除。 |
| `branch_added` | 完整 Branch | 刷新发现新分支。 |
| `branch_moved` | 完整 Branch | 刷新发现 head 变化。 |
| `branch_deleted` | `{project,name}` | 刷新发现分支删除。 |

记录变化通常由事件循环每秒读取数据库后发布；Git 事件由刷新直接发布。单房间订阅只接收相关 room/ready/failed/room_deleted 和当前 Build 的 build/build_deleted。每 15 秒输出 `: ping` 注释。事件 `id` 是进程内递增序号，仅供诊断；服务器忽略 Last-Event-ID，每次重连重新 sync。订阅缓冲满时关闭连接，客户端重连并以 sync 替换本地状态。

客户端从 start 响应固定目标 `(project,id,generation,buildId)`。start、sync、room 或 ready 任一来源表明目标精确匹配且 ready，并且本地模组准备完成，才可以启动游戏；一次加入操作最多启动一次。观察到 generation 改变，或 failed/stopping/archiving/closed/lost 时终止该加入操作并显示原因。重复 ready 事件不能触发重复启动。

## 错误

处理器错误响应统一为 `{"error":{"code":"…","message":"…","details":{"snapshotId":"…"}}}`，details 可省略。异步错误使用相同 Error 结构写入 Build.lastError、Room.lastError 或 Restore.error。客户端根据 code 判定，message 用于展示。HTTP 自身对未注册路径或不支持方法的响应由标准路由器处理。

| HTTP | code | 含义 |
| --- | --- | --- |
| 400 | `invalid_request` | 参数、JSON、归档内容、版本表达或过滤条件无效。 |
| 400 | `invite_invalid` | 邀请码无效、已经兑换或已经过期。 |
| 429 | `rate_limited` | 邀请兑换达到来源 IP 或全局频率限制；Retry-After 表示等待秒数。 |
| 401 | `unauthorized` | 凭据缺失/无效/过期、房间周期失效或租约所有权不符。 |
| 403 | `forbidden` | 已识别身份权限不足，或凭据类型没有该操作权限。 |
| 404 | `not_found` | 项目不可见、对象不存在、Git 引用或游标不在本地镜像。 |
| 409 | `conflict` | 状态冲突、短 ID 冲突、受保护对象、固定种子冲突。 |
| 409 | `build_failed` | 所需构建失败，需要显式重建。 |
| 409 | `build_mismatch` | Minecraft、loader、命名空间或 hop 构建不兼容。 |
| 409 | `sandbox_unavailable` | 请求的构建来源与项目可信策略无法支持当前执行方式。 |
| 409 | `eula_not_accepted` | 项目没有接受 Minecraft EULA。 |
| 409 | `room_finished` | 房间标记 finished，不能启动。 |
| 409 | `node_lost` | 节点不可用，无法安全执行请求。 |
| 409 | `start_timeout` | 服务器未在启动期限内就绪；通常作为异步房间错误。 |
| 409 | `archive_failed` | 归档、恢复或存储空间检查失败。 |
| 500 | `internal_error` | 未分类的服务端错误；响应隐藏内部错误详情。 |

## 路由契约校验

下面的 JSON 区块是完整路由目录，每项依次为方法、完整路径、权限。`controller/internal/api/contract_test.go` 使用标准 JSON 序列化从 `Routes()` 生成同一目录，直接比较完整区块。新增、删除、改名或权限变更均需要同步文档。P2 及后续的 CI、mrpack、OAuth、委托与大厅接口尚未注册，不属于当前端点。

```json vex-routes
[
  ["GET","/v2/health","public"],
  ["GET","/v2/meta","optional-user"],
  ["GET","/v2/me","user"],
  ["POST","/v2/auth/invites","user"],
  ["POST","/v2/auth/invites/redeem","public"],
  ["GET","/i/{code}","public"],
  ["GET","/v2/me/sessions","user"],
  ["DELETE","/v2/me/sessions/{id}","user"],
  ["POST","/v2/auth/logout","user"],
  ["GET","/v2/projects","user"],
  ["GET","/v2/projects/{p}","viewer"],
  ["GET","/v2/nodes","admin"],
  ["GET","/v2/events","user"],
  ["POST","/v2/hooks/router","internal"],
  ["POST","/v2/projects/{p}/git/refresh","developer"],
  ["GET","/v2/projects/{p}/branches","viewer"],
  ["GET","/v2/projects/{p}/commits","viewer"],
  ["GET","/v2/projects/{p}/builds","viewer"],
  ["POST","/v2/projects/{p}/builds","developer"],
  ["GET","/v2/projects/{p}/builds/{id}","viewer"],
  ["GET","/v2/projects/{p}/builds/{id}/log","developer"],
  ["GET","/v2/projects/{p}/builds/{id}/files/{name}","download"],
  ["GET","/v2/projects/{p}/rooms","viewer"],
  ["POST","/v2/projects/{p}/rooms","developer"],
  ["GET","/v2/projects/{p}/rooms/{id}","room-or-viewer"],
  ["PATCH","/v2/projects/{p}/rooms/{id}","developer"],
  ["DELETE","/v2/projects/{p}/rooms/{id}","owner"],
  ["POST","/v2/projects/{p}/rooms/{id}/start","viewer"],
  ["POST","/v2/projects/{p}/rooms/{id}/stop","developer"],
  ["POST","/v2/projects/{p}/rooms/{id}/restart","developer"],
  ["DELETE","/v2/projects/{p}/rooms/{id}/leases/{lease}","room-or-viewer"],
  ["GET","/v2/projects/{p}/rooms/{id}/log","developer"],
  ["GET","/v2/projects/{p}/rooms/{id}/events","viewer"],
  ["GET","/v2/projects/{p}/snapshots","viewer"],
  ["POST","/v2/projects/{p}/snapshots","room"],
  ["GET","/v2/projects/{p}/snapshots/{id}","viewer"],
  ["GET","/v2/projects/{p}/snapshots/{id}/download","developer-download"],
  ["POST","/v2/projects/{p}/snapshots/{id}/restore","developer"],
  ["GET","/v2/projects/{p}/worlds","viewer"],
  ["GET","/v2/projects/{p}/worlds/{id}","viewer"],
  ["PATCH","/v2/projects/{p}/worlds/{id}","owner"],
  ["POST","/v2/projects/{p}/worlds/{id}/revisions","developer"],
  ["GET","/v2/projects/{p}/worlds/{id}/revisions/{rev}","viewer"],
  ["GET","/v2/projects/{p}/worlds/{id}/revisions/{rev}/download","seed-download"],
  ["POST","/v2/projects/{p}/worlds/{id}/promote","developer"],
  ["POST","/v2/projects/{p}/worlds/{id}/restore","developer"],
  ["GET","/v2/projects/{p}/packs","viewer"],
  ["POST","/v2/projects/{p}/packs/{id}/revisions","developer"],
  ["GET","/v2/projects/{p}/packs/{id}/{file}","pack-download"],
  ["GET","/v2/projects/{p}/hop/destinations","room"],
  ["POST","/v2/projects/{p}/hop","room"]
]
```
