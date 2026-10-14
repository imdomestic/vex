# 接口契约提议：等待用户确认

本文对应 `docs/interface-questions.md` 的十项问题，依据原计划 §5、§7–§11、附录 A/B 整理。全部内容均为等待用户确认的提议；现有规划、`docs/api.md` 和 P1 实现没有因此发生变化。文中的请求、对象和响应表示建议采用的契约，尚未经过接口实现或端到端验收。

以下时间统一使用 UTC RFC 3339 字符串；文件大小使用字节整数；SHA-256 使用完整的小写十六进制字符串；明确标注可空的字段保留键并使用 `null`。错误响应沿用 `{"error":{"code":"…","message":"…"}}`，使用附录 B 的既有错误码。需要恢复种子快照时，提议增加可选 `error.details.snapshotId`；其余错误可以省略 `details`。`message` 供人阅读，客户端根据 HTTP 状态、`code` 和明确约定的 `details` 处理流程。

## 1. 构建列表

**新增路径：** `GET /v1/builds`。

返回 `200` 和 `Build[]`，各对象与单项查询使用同一结构。支持可选查询参数 `sha`、`loader`、`status`，分别按完整 SHA、loader 名称及一个构建状态精确过滤；参数不合法返回 `400 invalid_request`。默认返回全部现存构建记录，按照 `queuedAt` 从新到旧排列，同一时间按照 `id` 排列。

列表包含排队、进行中、失败及仍保留在缓存中的成功构建。成功构建满足原计划的 LRU 淘汰条件后，其 manifest、文件和日志一起退出可查询集合；此时单项查询返回 `404 not_found`，Commit/Branch 的对应构建摘要恢复为 `null` 或不存在的 loader 键。再次请求该 `(sha,loader)` 时重新构建。被任何非 `closed` 房间引用的构建继续受到原有保留规则保护。

新增内容仅为列表路径及三个过滤参数。CLI 的 `builds` 操作直接使用这份列表。

## 2. Build 的阶段字段与重建

所有状态使用同一 Build 对象。原有字段的取值规则如下：

| 字段 | `queued` | `building` | `ready` | `failed` |
| --- | --- | --- | --- | --- |
| `id`、`sha`、`loader`、`status` | 必填 | 必填 | 必填 | 必填 |
| `minecraft`、`loaderVersion`、`java` | `null` | 元数据读取成功后填写 | 必填 | 保留本次已经确认的值，其余为 `null` |
| `builtAt` | `null` | `null` | 文件校验并发布的时间 | `null` |
| `durationSec` | `null` | `null` | 本次开始执行至完成的秒数 | 已开始执行时填写，否则为 `null` |
| `files` | `[]` | `[]` | 完整文件列表 | `[]` |
| `log` | 既有日志 URL | 既有日志 URL | 既有日志 URL | 既有日志 URL |

**新增字段：**

| 字段 | 类型及规则 |
| --- | --- |
| `attempt` | 正整数；每次实际构建尝试递增，同一 `buildId` 的计数在缓存淘汰后继续保留 |
| `queuedAt` | 本次尝试进入队列的时间 |
| `startedAt` | 开始执行的时间；执行前为 `null` |
| `finishedAt` | 成功或失败时间；进行中为 `null` |
| `lastError` | `null` 或 `{code,message}`；失败使用 `build_failed` |

处于 `queued` 时日志接口可以返回空文本。全部构建产物完成校验后，`files`、完整元数据和 `status=ready` 在同一次数据库提交中发布；准备中的文件不可下载。失败日志留在既有日志接口，`lastError.message` 保留能够解释失败的摘要。

`POST /v1/builds` 的响应明确为：新建尝试返回 `202 + Build`；重复请求已经存在的构建返回 `200 + Build`。`force:true` 在 `queued/building` 时仍复用当前尝试；在 `failed` 时创建下一次尝试；在 `ready` 且没有任何非 `closed` 房间引用时创建下一次尝试。`ready` 构建仍被这些房间引用时，返回 `409 invalid_request`，保持正在使用的 manifest 和文件稳定。创建新尝试时递增 `attempt`，重新填写 `queuedAt`，将 `minecraft/loaderVersion/java/builtAt/durationSec/startedAt/finishedAt/lastError` 全部设为 `null`，`files` 设为 `[]`，日志切换到新尝试。版本字段只在本次元数据读取成功后填写。

构建失败后，普通构建请求和房间 `start` 都不会隐式重建。用户通过 `force:true` 完成新的构建尝试后，再显式启动失败房间。异步构建结果按 `(buildId,attempt)` 写入，过期尝试不能覆盖当前记录。

这里需要确认的语义包括构建淘汰后的可查询范围，以及正在被房间使用的成功构建拒绝强制重建。

## 3. SSE 重放、同步与首次就绪 ACK

**新增响应字段：** `POST /v1/rooms/{id}/start` 和 `POST /v1/hop` 的响应增加 `eventCursor`：

```json
{
  "room": {"id":"dev-fabric","generation":3,"state":"starting"},
  "lease": {"id":"<leaseId>","generation":3,"expiresAt":"<timestamp>"},
  "eventCursor":"<opaque cursor>"
}
```

示例中的 Room 省略了其他既有字段；实际响应返回完整 Room。`generation` 的定义见第 4 项。`eventCursor` 表示接受请求并保存房间、租约之后的事件位置，与这些操作使用同一次数据库事务。返回响应前已经产生的 `ready`，以及返回响应后才产生的 `ready`，均由以下连接流程覆盖。

事件保存在持久化日志中，状态更新与对应事件使用同一次数据库事务。游标由持久化的事件流标识和递增序号组成，客户端将其视为不透明字符串。服务端至少保留最近 200 条全局事件。房间订阅沿用全局游标，过滤其他房间的事件后允许出现序号间隔。正常服务重启保留游标；数据库重新初始化会产生新的事件流标识。

**新增 SSE 事件类型：** `sync`。每次连接和重连均执行以下顺序：

1. 服务端原子取得当前事件位置 `H` 和该位置对应的状态，并保证后续大于 `H` 的事件进入本连接的待发送队列。
2. 有有效 `Last-Event-ID=L` 时，按顺序重放保留的 `L < id <= H` 事件。没有游标时跳过历史重放。
3. 发送 `event: sync`，其 SSE `id` 为 `H`，数据为 `{rooms: Room[], builds: Build[]}`。全局订阅返回当前全部 Room 及第 1 项定义的构建集合；房间订阅返回该 Room 和其引用的现存 Build。客户端使用这份状态替换订阅范围内的缓存。
4. 在统一有序事件通道中取得确认位置 `C`，先发送 `H < id <= C` 的事件，再检查 `C` 对应的当前状态。仅对仍为 `ready` 的房间发送现有类型 `ready` 的当前就绪确认，数据增加 `confirmation:true`。取得 `C`、检查状态和将确认加入发送队列使用同一个串行操作；大于 `C` 的事件排列在确认之后。该确认不设置 SSE `id`，不产生新的持久化状态事件。
5. 按顺序发送大于 `C` 的持久化事件，继续实时订阅。

当前就绪确认使用 `{id,generation,host,port,buildId,confirmation:true}`。持久化 `ready` 事件使用同一结构并令 `confirmation:false`。确认针对已经通过 Pod Ready 和路由 SLP 核验的当前 generation；连接本身不会将未通过核验的房间改为 `ready`。

持久化的 `room` 和 `build` 事件分别携带完整 Room、完整 Build，避免客户端通过多个不一致的 GET 拼接状态。`failed` 保持 `{id,error}` 并增加 `generation`。已有 Git 事件名称和语义保持原计划约定。每 15 秒的注释心跳不设置事件 ID。

ruri 的加入流程必须同时满足：

- 先收到 `start` 响应，记录目标 `(room.id,room.generation,room.buildId)`；使用响应的 `eventCursor` 订阅房间 SSE，并行准备 manifest 和模组。
- 每次建立 SSE 连接后，先完成该连接的 `sync`。历史重放中的 `ready` 只更新历史状态，不触发游戏启动。
- 在 `sync` 后收到目标 generation 的当前就绪确认或新的持久化 `ready`，同时本地模组准备完成，才启动游戏。`sync` 中的 Room 状态本身不充当 ACK。
- 每个用户加入操作最多启动一次游戏；房间变为 `failed/stopping/archiving/closed/lost`，或者 generation/buildId 已改变时，撤销旧 ACK。服务端当前 generation 已变化时结束本次加入，展示当前房间状态，由新的显式加入操作取得新目标。

这也覆盖已经 `ready` 的房间：`start` 返回新租约，订阅取得 `sync` 和当前就绪确认。连接恰好在 `sync` 后、确认前断开时，下一次连接重新执行完整流程。客户端按持久化事件 ID 处理重放去重；当前就绪确认按目标 generation 和加入操作去重，其处理不能依赖事件 ID 的唯一性。SSE 采用至少一次交付语义。

游标格式错误返回 `400 invalid_request`。游标属于其他事件流、已经超出保留范围或者位于服务端未来位置时，在开始 SSE 响应前返回 `409 invalid_request`。客户端清除该游标，不携带 `Last-Event-ID` 重新连接，取得新的 `sync` 和当前就绪确认。不能根据一次普通 Room GET 自行补造 ACK。

这里新增一个 SSE 事件类型、`eventCursor`、`confirmation`，并扩充现有事件载荷；两个 SSE 路径保持原有路径。

## 4. 固定 roomId 的新启动周期

**新增字段：** `Room.generation`。登记房间时为 `0`；接受一个新的启动周期时递增，永久保存在该 roomId 的记录中。`closed` 表示该 generation 已归档并清理 Kubernetes 对象，固定 roomId 可以继续承载之后的 generation。

| 当前情况 | `start` 的处理 |
| --- | --- |
| `stopped`，标签为 `testing` | 创建下一 generation，保留已有 PVC 世界 |
| `closed` 的 commit/asset，标签为 `testing` | 创建下一 generation 和新的 PVC，按下面的种子规则初始化 |
| `closed` 的 branch，标签已经改回 `testing` | 创建下一 generation，从最后一个成功快照恢复 |
| `failed` 且原 Pod 已退出、世界来源仍可访问 | 创建下一 generation；保留已有 PVC 世界，未初始化的世界沿用固定来源；构建失败仍须先显式重建 |
| `queued/building/starting/ready` | 复用当前 generation，发放新的加入租约 |
| `stopping/archiving` | `409 invalid_request`，等待当前停止归档完成 |
| `lost` | `409 node_lost`，不使用失联节点上的世界继续启动 |
| 标签为 `finished` | `409 room_finished` |

**需要明确扩展原状态定义：** commit/asset 经 `POST /v1/rooms` 创建后、首次启动前也使用 `stopped`。此时 `generation=0`，尚未分配 PVC。既有的 roomId 创建请求继续返回现有 Room，包括 `closed` Room，不修改原有种子、不启动新周期。重复创建请求携带与原记录不一致的不可变字段时，返回 `409 invalid_request`；`kind`、完整 `sha`、`loader`、asset ID 属于不可变字段。

closed commit 再次启动时，重新解析保存的 `seed` 表达式。**既有 start 路径新增可选请求字段** `seed`，允许 commit 在首次启动或者 `closed` 且旧 PVC 已清理时覆盖下一周期的种子。branch、asset 携带该字段返回 `400 invalid_request`。commit 已经接受启动或者保留已有世界时，重复请求的 `seed` 与保存表达式相同则复用已固定来源；不同则返回 `409 invalid_request`。这使首次请求响应丢失后的相同请求可以安全重试。同一固定 commit ID 的重复创建请求不能改变保存的 `seed`；修改新周期种子通过上述 start 字段表达。

closed asset 再次启动时固定该资产当时的 `latestRev`。运行中的 asset 房间保持原 revision；发布新 revision 不会替换当前世界。branch 的正常停止后启动和显式 restart 保留 PVC；finished branch 改回 testing 后启动，使用原计划要求的最后成功快照。

显式 `restart` 完成原周期的停止和归档后，才接受下一 generation；停止或归档失败不创建新周期。branch 在新周期接受时固定新的分支 head。失败房间需要先确认旧 Pod 不再写入世界，才能开始下一周期；旧 Pod 尚未退出时返回 `409 invalid_request`。

**关联扩展：**

- 租约增加 `generation`；旧 generation 的租约不保护新周期。
- Room SSE 和就绪 ACK 增加 `generation`；Snapshot 增加 `roomGeneration`。
- 房间 token 增加 generation 绑定；快照上传按匹配的 roomId/generation 认证，旧周期的 token 无法操作新周期。
- Kubernetes 房间对象和归档 Job 增加标签 `chorus.dev/generation`。异步操作结果只更新它所属的 generation。
- MOTD 的身份标记扩充为 `room=<roomId> build=<buildId> generation=<n>`；路由 SLP 核验三个值全部匹配后才发出 ACK。

## 5. `branch:<name>` 种子的世界一致性

`branch:<name>` 表示对应分支、对应目标 loader 的房间**最新一个已经完整保存并发布的快照**。在需要初始化新世界的 start 请求受理时解析一次，固定快照 ID；构建排队、容量等待和该世界初始化的重试继续使用这个来源。源房间当前 head 变化不会改变已固定的快照。

源房间正在运行、有在线玩家、正在停止或者正在归档时，可以使用其此前完成的最新快照。请求种子不会停止源房间，不会修改玩家状态，不会直接复制仍在写入的 PVC。正在生成但尚未完成上传和校验的快照不能被选中。

没有完整快照时返回 `404 not_found`；源分支或者对应 loader 房间不存在时同样返回 `404 not_found`。快照只存在于 tank 时返回 `409 archive_failed`，新增可选字段 `error.details.snapshotId` 标明需要恢复的快照；客户端使用快照恢复接口，等文件恢复完成后重新请求 start。新周期、租约和构建请求在来源验证通过后才提交。

不设置快照年龄上限。Room 提供固定来源，客户端通过 Snapshot 的 `capturedAt` 展示世界取样时间。用户需要源房间最新的世界状态时，显式停止源房间，等待快照完成，再发起目标启动；该操作会让源房间玩家退出，应由发起停止操作的界面正常说明。

**新增 `Room.seedResolved` 字段：**

```json
null
{"kind":"empty"}
{"kind":"snapshot","id":"<snapshotId>"}
{"kind":"asset","id":"tower","rev":"<revision>"}
```

`null` 表示尚未确定初始世界来源。`empty` 对应原计划的空世界；快照和资产值固定到不可变版本。已有 PVC 的 branch 启动及保留世界的失败重试继续使用该世界和已记录来源。初始化完成前保留固定来源的本地文件，防止缓存淘汰。快照的 Minecraft 版本必须与目标房间配置的 Minecraft 版本一致，不要求源快照的 buildId 与目标 buildId 相同；不兼容时返回 `400 invalid_request`。准备完成的 Build 也必须满足同一 Minecraft 版本约束。该检查不承诺不同模组版本对任意已有世界的兼容性。

## 6. 资产 revision 的世界下载

**新增路径：** `GET /v1/assets/{id}/revisions/{rev}/download`。

成功返回 `200`、`Content-Type: application/gzip` 和该不可变 revision 的完整 `world.tar.gz`，顶层为 `world/`。URL 同时接受用户 Bearer 和原计划的 `exp/sig` 签名授权；签名绑定完整路径、HTTP 方法与过期时间。Pod 使用 chorusd 为已固定 revision 签发的 URL。

`AssetRevision.world` 提供 `{sha256,size,url}`。`url` 使用上述路径；普通查询或发布响应返回路径，编排 Pod 时生成有期限的签名 URL。调用方校验完整 SHA-256 和大小后才将解包世界投入使用。归档采用实际 Minecraft 26.3 的世界结构验证，包括 `world/level.dat` 及存在的维度数据；有效性判断不要求旧版本固定的 `world/region/` 位置。

资产或 revision 不存在时返回 `404 not_found`；本地没有已经校验的完整文件时返回 `409 archive_failed`。已有文件仍可按原有下载授权读取，`archived` 状态禁止进入资产房间的规则由启动和 hop 接口执行。恢复最新 revision 使用第 10 项操作。历史 revision 的下载仅在文件已经本地可用时成功；本文没有增加历史资产 revision 的恢复接口。

## 7. 资产、资源包的发布对象和 revision

### 资产发布

已有 `POST /v1/assets/{id}/revisions` 使用 `multipart/form-data`，必须包含两个命名 part：

| part 名称 | 内容类型 | 内容 |
| --- | --- | --- |
| `world` | `application/gzip` | `world.tar.gz` 的原始字节 |
| `asset` | `application/json` | 原计划 §11 的完整 `asset.json` |

`asset.json.id` 必须与路径一致。`minecraft`、`requires`、`resourcePack`、`source` 按原计划提供；`resourcePack` 可以为 `null`，非空时必须指向已发布、可用的固定资源包 revision。上传方只能提交转换后的 MC 世界，不包含原始解包资源。归档路径、成员类型和 MC 存档结构必须通过服务端验证；内容不合格返回 `400 invalid_request`，文件没有进入可查询的 revision 集合。

校验成功返回 `201 + AssetRevision`。**新增对象结构：**

```json
{
  "id":"tower",
  "rev":"<server-generated UUID>",
  "title":"高塔 · 中庭样板",
  "minecraft":"26.3",
  "requires":{"loaders":["fabric"],"chorusMod":true},
  "resourcePack":{"id":"d2-tower","rev":"<pack revision>"},
  "source":{"repo":"d2-map-dev","commit":"<sha>","scale":2.0,"notes":""},
  "snapshotId":null,
  "world":{"sha256":"<sha256>","size":123456,"url":"/v1/assets/tower/revisions/<revision>/download"},
  "createdAt":"<timestamp>"
}
```

资产 revision 采用服务端生成的 UUID v4，不表达顺序；每次成功发布产生新 revision。revision 的元数据、世界文件和资源包引用一经发布保持不变。相同内容再次发布也会产生新 revision，因此客户端在响应不明确时查询资产当前记录，避免自动重复提交发布请求。

世界文件校验完成、固定资源包引用验证完成之后，revision 记录、`Asset.latestRev` 和该世界文件及 revision 元数据的 tank 同步任务在同一次事务中提交，同时确保固定资源包 revision 已有同步任务或校验记录。新资产的状态为 `active`；现有资产保持当前 `active/archived` 状态。并发发布按成功提交的顺序更新 `latestRev`。Asset 的 `title/minecraft/requires/resourcePack` 对应最新 revision 的元数据，正在运行的房间继续使用自己的 `seedResolved`。

已有 `POST /v1/assets/{id}/promote {"edit":"<snapshotId>"}` 返回同一 `201 + AssetRevision`，使用相同的发布事务和 tank 同步任务登记规则。`edit` 明确使用 Snapshot ID；该快照必须是此资产房间生成的编辑候选，文件已在本地通过校验。新 revision 沿用快照来源 revision 的 `title/minecraft/requires/resourcePack/source`，世界来自快照，并填写 `snapshotId`。资产来源不匹配返回 `400 invalid_request`；快照不存在返回 `404 not_found`；需要先恢复文件时返回 `409 archive_failed`。

### 资源包发布

已有 `POST /v1/packs/{id}/revisions` 使用 `Content-Type: application/zip`，body 为 `pack.zip` 的原始字节。ZIP 必须具有有效资源包结构；元数据由 ZIP 中的 `pack.mcmeta` 提供，不新增旁路元数据文件。

资源包 revision 使用 ZIP 全部字节的 SHA-256。首次发布返回 `201 + PackRevision`；同一 pack ID 和相同字节的重复发布返回 `200 +` 已有对象。**新增对象结构：**

```json
{
  "id":"d2-tower",
  "rev":"<sha256 of ZIP bytes>",
  "sha256":"<sha256 of ZIP bytes>",
  "sha1":"<sha1 of ZIP bytes>",
  "size":123456,
  "url":"/v1/packs/d2-tower/<revision>.zip",
  "createdAt":"<timestamp>"
}
```

`sha256` 用于文件完整性校验，`sha1` 用于 Minecraft 的资源包配置。两者均由服务端对收到的同一份 ZIP 计算。发布资源包 revision 时，同一事务登记 ZIP 和 revision 元数据的 tank 同步任务；相同 revision 的重复发布复用已有任务和校验结果。固定 revision 的 ZIP 不允许覆盖；文件下载沿用原计划的 pack 路径，并明确加入可签名下载的资源种类。发布和下载保持原计划的私有网络与敏感资源边界。

本项新增 AssetRevision、PackRevision 两种返回结构，明确 multipart part 名称、pack body 和 revision 算法；没有增加发布路径。

## 8. hop 目的地与加入租约

已有 `GET /v1/hop/destinations?fromRoom=<roomId>` 返回 `200 + HopDestination[]`。房间 token 必须与 `fromRoom` 及其当前 generation 匹配；调用来源已经退出当前周期时返回 `401 unauthorized`。目的地对象统一为：

```json
{
  "dest":"asset:tower",
  "kind":"asset",
  "id":"tower",
  "title":"高塔 · 中庭样板",
  "roomId":null,
  "buildId":"fabric-<sha12>",
  "state":"available"
}
```

**新增对象字段规则：** `kind` 为 `asset|room`；`id` 为对应资产 ID 或 roomId；`roomId` 在已找到匹配 `(asset,buildId)` 房间时填写，否则为 `null`；`buildId` 始终为源房间使用的固定构建。`state` 为实际 Room 状态，或者尚未存在可复用启动周期时的 `available`。`title` 使用资产标题或房间显示名。

资产条目只包含 active 资产。先确定 `(asset,buildId)` 的实际目标周期：复用已有周期或保留世界的失败周期时，按 `seedResolved` 的 revision 检查 Minecraft、loader 和依赖；初始化新世界时，按 `latestRev` 检查。只有实际目标 revision 与源房间兼容、初始化所需文件完整可用时才提供该条目；已有目标处于 `stopping/archiving/lost` 时暂不提供。普通房间条目只包含同一 buildId、标签为 testing、状态为 `stopped/queued/building/starting/ready` 的其他房间。对 stopped branch，按照其下次启动将固定的当前 head 计算目标 buildId，不能仅比较停止前的 Build。asset 房间提供同一 buildId 的 branch/commit 返回目的地；固定 commit/asset ID 已 closed 后的再次启动遵循第 4 项，资产目的地可以创建或复用其固定 roomId。列表不包含源房间本身，按照 `kind`、`id` 排序。

`dest` 字符串增加限定形式 `asset:<assetId>` 和 `room:<roomId>`，点击目的地时使用限定形式。原计划中的裸资产名称和 roomId 仍然接受；若同一个裸字符串同时匹配资产与房间，返回 `409 invalid_request`，信息提示使用限定形式。不存在的目标返回 `404 not_found`，跨构建的房间目标返回 `409 build_mismatch`。

已有 `POST /v1/hop {fromRoom,dest,player:{uuid,name}}` 返回第 3 项的完整 `{room,lease,eventCursor}`。来源必须是 token 绑定的当前 ready 周期；来源状态不满足时返回 `409 invalid_request`。资产目标在受理时确定同一 buildId 和实际 revision，并重新执行上述兼容性及可用性检查；已经存在运行周期时保持该周期的 revision。受理时再次解析并固定实际目标构建，若 stopped branch 的 head 已变化，导致构建与来源不同，则返回 `409 build_mismatch`，不启动目标房间。目标已 ready 时返回 `200`，其余已接受的启动或等待返回 `202`。容量不足保持 `queued` 并返回房间状态，继续原计划的自动等待，不把已接受的请求转为错误。

租约为 `{id,generation,expiresAt}`，服务端还记录调用者及玩家 UUID。有效期沿用默认三分钟；同一 generation 的重复 start/hop 发放新租约，不重新初始化世界。租约按各自到期时间失效，任何仍有效的租约都暂停空闲计时。构建和排队阶段不运行 ready 房间的空闲回收逻辑；已经过期的租约不会在 ready 时自动获得新的期限。

chorus-hop 继续按原计划每两秒查询 Room；当目标 generation 及 buildId 匹配、状态为 ready 时执行 transfer，五分钟仍未成功则报告失败并取消自己的租约。ruri 使用 SSE ACK 流程。租约删除在已经过期或已删除时仍返回 `204`；不得删除其他调用者的有效租约，越权请求返回 `401 unauthorized`。请求方取消租约不停止仍被其他请求或玩家使用的房间。

**房间 token 权限扩展：** 允许调用既有 `DELETE /v1/rooms/{targetRoom}/leases/{leaseId}`，仅能删除由该来源 `(roomId,generation)` 创建的目标租约。服务端验证签名、有效期和租约所有权；来源已经退出该周期的有效 token 仍可取消自己的旧租约。现有读取目标 Room、请求 hop 和上传所属快照的权限继续遵守各自的 generation 规则。

本项没有增加 HTTP 路径；新增 HopDestination 结构、限定 dest 语法，并统一 start/hop 的响应。`POST /rooms/{id}/start` 同样在已 ready 时返回 `200`，其余受理情况返回 `202`。

## 9. Snapshot、快照种子与异步恢复

**新增 seed 语法：** `snapshot:<snapshotId>`，精确引用该快照。恢复操作只将归档文件取回 encore；创建房间和启动房间继续使用现有接口。

**新增 Snapshot 对象：**

| 字段 | 契约 |
| --- | --- |
| `id` | 不透明、永久稳定的快照 ID |
| `roomId`、`roomGeneration` | 产生快照的房间和启动周期 |
| `buildId`、`minecraft` | 产生快照的构建及 Minecraft 版本 |
| `asset` | 编辑候选的来源 `{id,rev}`；普通快照为 `null` |
| `capturedAt` | 源 Pod 已退出、世界停止写入的时间 |
| `createdAt` | 完整上传、文件校验及元数据发布完成的时间 |
| `sha256`、`size` | 完整归档文件的摘要及字节数 |
| `localAvailable` | encore 是否保存完整且校验通过的文件 |
| `tankVerifiedAt` | tank 文件校验通过时间；尚未完成为 `null` |
| `restore` | 最新恢复尝试；没有发起过恢复时为 `null` |

只有完整上传并校验成功的 Snapshot 才进入查询集合；准备中的归档由 Room 的 `archiving` 状态表达。`Room.lastSnapshot` 保留时间字段，填写最新成功快照的 `capturedAt`。已有 `GET /v1/snapshots?room=<roomId>` 返回 `Snapshot[]`，按照 `capturedAt` 从新到旧排列，同一时间按照 `id` 排列。

**新增路径：** `GET /v1/snapshots/{id}`，返回 `200 + Snapshot`。恢复状态内嵌在 Snapshot 中：

```json
{
  "attempt":1,
  "status":"queued",
  "requestedAt":"<timestamp>",
  "startedAt":null,
  "finishedAt":null,
  "error":null
}
```

`status` 使用 `queued|running|succeeded|failed`。成功和失败均填写 `finishedAt`；失败的 `error` 为 `{code:"archive_failed",message:"…"}`。进行中的尝试持久化，controller 重启后继续处理。

已有 `POST /v1/snapshots/{id}/restore` 的行为为：本地完整文件已经可用时返回 `200 + Snapshot`；新建恢复尝试或正在恢复时返回 `202 + Snapshot`，进行中的重复请求保持同一 attempt。没有进行中尝试、并且确实需要从 tank 取回文件时创建下一 attempt，覆盖失败后重试和先前成功后本地文件再次被淘汰的情况。快照不存在返回 `404 not_found`；没有已经校验的 tank 来源返回 `409 archive_failed`。

恢复写入独立工作目录，完整摘要与大小校验通过后原子发布文件并设置 `localAvailable=true`、`restore.status=succeeded`。此前不能下载、解包或作为种子使用。既有下载接口在本地文件不可用时返回 `409 archive_failed`，保持既有签名 URL 授权。下载和世界初始化持有文件期间，缓存淘汰不得移走对应文件。

**内部上传接口扩展：** `POST /v1/snapshots?room=<roomId>&build=<buildId>&generation=<n>`，新增必需查询参数 `generation`，body 继续使用原始世界归档。服务端先验证 token 的签名、有效期及 `(roomId,generation,buildId)` 绑定。尚未发布快照的新上传，还必须匹配当前 generation 和当前归档任务。`capturedAt` 取 controller 已确认的 Pod 退出记录，归档任务使用专属 generation token。

同一 `(roomId,generation)` 的停止归档只产生一个 Snapshot。首次完整上传返回 `201 + Snapshot`；重试相同文件返回 `200 +` 原 Snapshot；同一 generation 提交不同内容返回 `409 invalid_request`。已经发布该快照后，有效旧 token 可以对原 tuple 和相同内容取得这个幂等响应，即使房间已经 closed 或进入下一 generation；这次请求只确认已有结果，不修改文件、归档状态或者当前房间。归档 Job 重试沿用同一份已封存文件，避免重新打包造成压缩文件字节变化。上传重试不得覆盖已经发布的快照。branch 已经停止且之后没有写入世界时，重复 stop 或设置 finished 复用该周期已有快照，按标签处理 PVC 保留或删除。节点失联后的强制删除不构成成功保存的证据，只能继续使用此前已经完成的快照。

## 10. 资产从 tank 恢复

**新增路径：** `POST /v1/assets/{id}/restore`。**新增字段：** `Asset.restore`，使用第 9 项的恢复对象并增加 `revision`，记录本次固定的目标 revision；没有发起过恢复时为 `null`。

资产仍然使用 `active|archived` 两种状态。受理恢复请求时固定 `latestRev`，恢复该 revision 的世界文件及它引用的固定资源包。恢复期间状态保持 `archived`；全部文件的大小和摘要校验通过后，在同一次事务中发布文件可用性、`restore.status=succeeded` 和 `state=active`。恢复失败保持 `archived`，错误使用 `archive_failed`。

本地已经具有全部必需文件时，恢复请求直接校验并返回 `200 + Asset`，状态成为 active；需要从 tank 取回文件或者同一尝试正在运行时，返回 `202 + Asset`。重复请求复用进行中的 attempt；没有进行中的尝试且确实需要取回文件时，创建下一 attempt，覆盖失败后重试和先前成功、再次归档及淘汰后的恢复。资产不存在返回 `404 not_found`；缺少已经校验的 tank 来源返回 `409 archive_failed`。现有 `GET /v1/assets` 返回的 Asset 包含恢复进展，客户端按资产 ID 观察状态。

已有 `PATCH /v1/assets/{id} {"state":"active"}` 调用同一个恢复流程，采用相同的 `200/202` 响应和文件校验条件。恢复完成不会自动创建房间或发放租约。asset 已经 active 且文件完整时，重复恢复返回 `200`，不改变正在运行的房间。

在 `restore.status=queued/running` 期间，对该资产 publish、promote、再次设置 archived 返回 `409 invalid_request`，以保证本次恢复的目标稳定。历史 revision 保持原存储位置，本操作只恢复进入最新资产房间需要的完整依赖集合。

archived 或正在恢复的资产禁止新建资产房间、启动该资产房间和 hop 进入，返回 `409 invalid_request`。允许进入的条件为 `state=active` 且最新 revision 及固定资源包文件全部校验通过。运行中的旧 revision 房间继续按照其已有生命周期工作；恢复不会替换其世界。

**需要一并确认的归档前提：** 资产存在已经接受启动、尚未 closed 的资产房间时，设置 archived 返回 `409 invalid_request`；调用者先显式停止这些房间。`generation=0` 的登记记录不阻止归档。设置 archived 前，最新世界、固定资源包和它们的 revision 元数据必须已经在 tank 校验通过。同步任务在上述 publish/promote 的成功事务中登记，由原计划的持久化同步队列按带宽限制异步执行，服务重启后继续处理。

`PATCH state=archived` 检查所需对象时，也会幂等登记缺失任务，并重新排队此前失败、目前本地文件完整的任务；进行中的任务继续复用。尚未全部校验通过时返回 `409 archive_failed` 并保留当前 active 状态。后台同步完成后，调用者再次 PATCH 才设置 archived，不会因先前失败的 PATCH 自动改变资产状态。归档成功返回 `200 + Asset`，文件按保留策略释放本地缓存；未获得 tank 校验记录的本地文件始终保留。

## 需要用户确认的接口增量

| 类别 | 本提议引入的内容 |
| --- | --- |
| 新增 HTTP 路径 | `GET /v1/builds`；`GET /v1/assets/{id}/revisions/{rev}/download`；`GET /v1/snapshots/{id}`；`POST /v1/assets/{id}/restore` |
| 现有请求扩展 | 构建列表过滤参数；start 可选 `seed`；hop 限定 dest；快照上传必需 `generation`；明确资产 multipart part 与资源包 ZIP body |
| Build 字段 | `attempt/queuedAt/startedAt/finishedAt/lastError`，以及既有字段在四种状态的可空性 |
| Room 与启动响应 | `generation/seedResolved`；租约 `generation`；start/hop `eventCursor` |
| SSE | 新增 `sync`；`ready.confirmation/generation`；`failed.generation`；`room/build` 完整对象载荷；连接重放与同步规则 |
| 发布返回对象 | AssetRevision、PackRevision；UUID 资产 revision；SHA-256 资源包 revision；promote 使用 Snapshot ID |
| hop 返回对象 | HopDestination；统一 `{room,lease,eventCursor}` |
| 快照与恢复 | Snapshot、内嵌恢复尝试；`snapshot:` seed；`Asset.restore` 及其 `revision` |
| 错误字段 | 可选 `error.details.snapshotId`，错误码继续使用原集合 |
| 周期身份传递 | token、Kubernetes 标签、归档任务、MOTD/SLP 加入 generation |
| 房间 token 权限 | 允许取消由其来源 roomId/generation 创建的目标租约 |
| 需要确认的行为 | commit/asset 首次 stopped；closed ID 可新建周期；已有 ready 构建受引用时拒绝 force；branch seed 固定已完成快照；资产发布保持 archived；归档前完成房间停止和 tank 校验 |

这些条目应由用户确认后再进入对应阶段的 `docs/api.md` 和实现。P1 开始前需要确定第 1–5 项；第 6–8 项在 P5 前确定；第 9–10 项在 P6 前确定。跨阶段使用的 generation、快照 ID、固定 revision 和恢复错误语义应在首次实现相关对象时保持一致。
