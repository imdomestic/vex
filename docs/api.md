# Chorus HTTP API v1

本文件是 chorusd、Ruri Chorus、chorusctl、chorus-hop 和资产发布工具共同使用的接口契约。依据 [2026-10-15 裁决](directives/2026-10-15-decisions.md)及原计划附录 A/B 编写，十项接口事项均已裁决。接口变更先更新本文件，再记录到 `docs/decisions.md`，随后修改实现。

P1 实现 Git、Build、Room 启动、SSE 和种子规则；P2 完成房间生命周期；P3/P4 接入 ruri；P5 实现资产、资源包和 hop；P6 实现 tank 同步及恢复。节点 drain 的自动下线集成属于 P7。本文中的阶段安排表示实现顺序，全部字段和行为以本文为准。

## 1. 通用约定

- API 前缀为 `/v1`。用户端使用 `http://encore.inner.imdomestic.com:8790`，Pod 使用 `http://10.1.2.137:8790`；下载相对路径以对应调用者的 base URL 解析。Minecraft 资源包 URL 使用用户端 base URL。
- JSON 使用 UTF-8，类型为 `application/json`。时间为 UTC RFC 3339 字符串；大小为非负字节整数；SHA-256 为 64 个小写十六进制字符；Git SHA 为完整的 40 个小写十六进制字符。
- 下文对象列出的字段均返回。可空字段使用 `null`；集合使用空数组或空对象。仅明确标为可选的请求字段、`error.details` 可以省略。客户端忽略未知响应字段。
- 路径中的标识符按单个 URL path segment 编码；文件名只允许文件名本身，不接受路径分隔符、`.`、`..` 或目录穿越。非法 JSON、类型、枚举、字段值、过滤参数返回 `400 invalid_request`。
- 当前 Minecraft 为 `26.3`、Java 为 `25`，启用的 loader 只有 `fabric`。当前分支房间为 `main-fabric`。客户端通过 `/v1/meta` 读取实际配置，不将 zone 固定在代码中。
- 房间主机名为 `<roomId>.<meta.zone>`，端口为 `25565`。目标 zone 为 `chorus.internal`；部署采用临时 zone 时，现存 Room 的 `host` 和路由 Service 注解随配置同步。

### 1.1 鉴权

除下面明确允许房间 token 或内部调用的接口外，所有接口，包括 health、meta、SSE，使用 `Authorization: Bearer <用户 token>`。用户 token 标识调用者。token 和签名密钥只通过相应 secret 文件或钥匙串提供，不出现在日志、错误信息或文档中。

房间 token 由 chorusd 签发，绑定 `roomId`、`generation`、`buildId` 和有效期。它只允许：

1. 调用 `/v1/hop` 和 `/v1/hop/destinations`，来源必须是该 token 绑定的当前周期。
2. `GET /v1/rooms/{id}`，用于观察 hop 目标。
3. 上传所属 `(roomId,generation,buildId)` 的快照，遵守 §9 的归档任务及重试规则。
4. 删除由该来源 `(roomId,generation)` 创建的目标加入租约。

下载类 GET 同时接受用户 Bearer 或签名 URL：`?exp=<Unix秒>&sig=<HMAC-SHA256>`。覆盖 build files、deps、mods、snapshot download、asset revision download、pack ZIP。签名字符串为 `GET\n<已编码的绝对路径>\n<exp>`，没有末尾换行；HMAC 输出为小写十六进制。路径不含 host 和 query；服务端使用实际请求的编码路径验证，`exp` 必须是规范的十进制整数，当前时间达到 `exp` 即失效。签名只授权该路径的 GET。客户端把签名 URL 当作服务端提供的完整 URL 使用。

mc-router webhook 是部署内部接口，只接受受信任的 mc-router 调用来源。它只触发补充 SLP 探测；玩家数量和房间就绪状态始终来自实际探测结果。

### 1.2 错误

业务错误响应为：

```json
{"error":{"code":"conflict","message":"房间正在归档"}}
```

`Error` 对象为 `{code:string,message:string,details?:{snapshotId:string}}`。`details.snapshotId` 只用于展示涉及的快照；快照种子的自动恢复由服务端处理。`Room.lastError`、`Build.lastError`、恢复任务的 `error` 和 SSE 的 `error` 使用同一结构。客户端根据 HTTP 状态和 `code` 处理流程，`message` 供人阅读。

| code | HTTP 与使用场景 |
| --- | --- |
| `unauthorized` | 401；缺少、无效、过期的凭据，或 token 无权执行该操作 |
| `not_found` | 404；请求的对象、远端提交、分支、文件不存在 |
| `invalid_request` | 400；请求格式、参数、版本约束或归档内容不合法 |
| `conflict` | 409；当前状态不允许请求、不可变字段不一致或歧义目标 |
| `room_finished` | 409；房间标签为 finished |
| `build_failed` | 409；请求依赖已失败的构建；异步构建失败也使用该 code |
| `build_mismatch` | 409；跨 build hop，或资产 Minecraft、loader、命名空间与目标构建不兼容 |
| `capacity_exhausted` | 排队提示；已受理的 start/hop 返回 202，Room 保持 queued |
| `start_timeout` | Pod 连续未就绪超过启动期限；保存日志并确认 Pod 退出后 Room 进入 failed |
| `node_lost` | 409；请求依赖失联节点上的房间；异步失联也写入 Room.lastError |
| `archive_failed` | 409；需要的已校验归档文件不可用或归档条件未满足；异步归档、恢复失败也使用该 code |

异步错误通过对象状态和 SSE 表达。客户端还必须处理传输失败、非 JSON 响应和 HTTP 5xx，并显示请求失败；这些情况不构成业务完成的证据。

## 2. 数据对象

### 2.1 Health、Meta、Branch、Commit、Node

| 对象 | 字段 |
| --- | --- |
| Health | `status:"ok"` |
| Meta | `minecraft:string`、`java:integer`、`zone:string`、`loaders:string[]`、`publicBaseURL:string`、`limits:Limits` |
| Limits | `buildConcurrency:integer`、`buildCacheSize:integer`、`maxRunning:{节点名:integer}`；当前为 1、20、`{encore:2,seraph:4}`，实际容量配置可按验收结果调整 |
| BuildSummary | `id:string`、`status:queued\|building\|ready\|failed` |
| Branch | `name:string`、`head:SHA`、`rooms:{loader:roomId}`、`builds:{loader:BuildSummary\|null}` |
| Commit | `sha:SHA`、`parents:SHA[]`、`subject:string`、`author:string`、`time:timestamp`、`refs:string[]`、`builds:{loader:BuildSummary\|null}` |
| Node | `name:string`、`ready:boolean`、`schedulable:boolean`、`maxRunning:integer`、`rooms:roomId[]` |

Branch/Commit 的 builds 表示对应 SHA 的当前构建；尚无构建或已被淘汰时，该 loader 的值为 `null`。`refs` 是当前指向该提交的 ref 显示名称，`parents` 保持 Git 父节点顺序。Commit 数组按拓扑顺序返回，同一提交只出现一次。Node 的 rooms 是当前分配到该节点、尚未 closed 的房间 ID。

### 2.2 Build

| 字段 | 类型与含义 |
| --- | --- |
| `id` | `<loader>-<sha前12位>` |
| `sha`、`loader` | 完整 SHA、启用的 loader |
| `status` | `queued\|building\|ready\|failed` |
| `attempt` | 正整数；每次实际尝试递增，计数在构建缓存淘汰后保留 |
| `queuedAt` | 本次进入队列的时间 |
| `startedAt`、`finishedAt` | 本次开始、结束时间；尚未发生时为 null |
| `minecraft`、`loaderVersion` | 字符串或 null；本次元数据读取成功后填写 |
| `java` | Java 主版本整数或 null |
| `builtAt` | 文件校验并发布的时间；只有 ready 时非 null |
| `durationSec` | 非负数或 null；本次实际执行耗时，运行结束后填写 |
| `modIds` | 字符串数组；从本次 manifest 中各 Fabric jar 的 `fabric.mod.json` 读取模组 ID，去重排序，ready 时完整发布 |
| `files` | BuildFile 数组；ready 时为完整集合，其余状态为 `[]` |
| `log` | `/v1/builds/{id}/log` |
| `lastError` | Error 或 null；失败使用 build_failed |

BuildFile 为 `{name:string,sha256:string,size:integer,side:"both"|"server"|"client",url:string}`。`url` 是本文定义的下载路径。`both` 同时安装到客户端和房间；`server` 只安装到房间；`client` 只安装到客户端。两端安装的同名文件使用同一 SHA-256。chorus、Fabric API 及资产需要的客户端依赖使用 `both`；chorus-hop 使用 `server`。

`queued` 时 `minecraft/loaderVersion/java/startedAt/finishedAt/builtAt/durationSec/lastError` 为 null，`files/modIds` 为 `[]`。building 可以填写已读出的版本，`finishedAt/builtAt/durationSec` 仍为 null。ready 必须具有完整版本、文件、modIds、时间和执行耗时。failed 保留本次已确认的版本；未执行就失败时 startedAt/durationSec 为 null。一次尝试的文件、完整元数据和 ready 状态在同一数据库提交中发布，准备中的文件不可下载。

### 2.3 Room、SeedResolved、Lease

| 字段 | 类型与含义 |
| --- | --- |
| `id` | 稳定 roomId，规则见 §4.1 |
| `kind` | `branch\|commit\|asset` |
| `branch` | 原始分支名；commit/asset 为 null |
| `sha`、`loader`、`buildId` | 当前登记或启动周期的完整 SHA、loader、Build ID；同一已接受周期保持固定 |
| `generation` | 非负整数；登记时为 0，每次接受新启动周期递增，永久保留 |
| `behindHead` | branch 当前 SHA 与远端 head 不同时为 true，其余 kind 为 false |
| `host`、`port` | 当前路由主机名、整数端口 |
| `state` | `stopped\|queued\|building\|starting\|ready\|stopping\|archiving\|closed\|failed\|lost` |
| `phase` | 当前阶段的说明字符串；客户端原样展示。`re-verify` 和 `restoring seed` 具有 §4 的明确含义 |
| `label` | `testing\|finished`；默认 testing，与 state 独立 |
| `node` | 已分配节点名或 null |
| `players` | `{name:string,uuid:string}[]`；实际 SLP sample 中可得的玩家资料 |
| `idleSince` | 当前零玩家且无有效租约的起始时间；尚未空闲为 null |
| `seed` | 保存的种子表达式；语法见 §5 |
| `seedResolved` | SeedResolved 或 null；尚未解析初始世界时为 null |
| `asset` | asset 房间的资产 ID 字符串；其他 kind 为 null |
| `lastSnapshot` | 最新成功快照的 capturedAt；尚无快照为 null |
| `lastError` | Error 或 null |
| `createdAt`、`updatedAt` | 登记时间、最后状态更新时间 |

房间登记时已经确定 SHA 和 buildId，但对应 Build 可以尚未进入构建队列。branch 运行期间远端 head 移动只改变 behindHead；下一启动周期才固定新 head。

SeedResolved 只有以下三种对象，引用的内容固定到不可变文件版本：

```json
{"kind":"empty"}
{"kind":"snapshot","id":"<snapshotId>"}
{"kind":"asset","id":"plaza","rev":"1"}
```

Lease 为 `{id:string,generation:integer,expiresAt:timestamp}`。服务端另外保存调用者、来源房间周期及适用的玩家 UUID；这些身份信息不返回到 Lease。start 和 hop 的成功响应统一为 `StartResult={room:Room,lease:Lease}`。

### 2.4 资产与资源包

资产 ID 匹配 `^[a-z0-9][a-z0-9-]{0,15}$`。AssetRevision.rev 是每个资产各自递增的正整数，以无前导零的字符串表示，例如 `"1"`、`"2"`；比较 revision 顺序时使用整数顺序。

| 对象 | 字段 |
| --- | --- |
| Requirements | `loaders:string[]`、`chorusMod:boolean`、`namespaces:string[]` |
| PackRef | `id:string`、`rev:SHA256` |
| Source | `repo:string`、`commit:SHA`、`scale:number`、`notes:string` |
| AssetMetadata | `id:string`、`title:string`、`minecraft:string`、`requires:Requirements`、`resourcePack:PackRef\|null`、`source:Source` |
| AssetRevision | AssetMetadata 的全部字段，以及 `rev:string`、`snapshotId:string\|null`、`world:{sha256:string,size:integer,url:string}`、`createdAt:timestamp` |
| Asset | `id:string`、`title:string`、`state:active\|archived`、`latestRev:string`、`minecraft:string`、`requires:Requirements`、`resourcePack:PackRef\|null`、`restore:AssetRestore\|null` |
| PackRevision | `id:string`、`rev:SHA256`、`sha256:SHA256`、`sha1:40位小写十六进制`、`size:integer`、`url:string`、`createdAt:timestamp` |

Asset 的 title、minecraft、requires、resourcePack 来自 latestRev。AssetRevision 的元数据、世界文件和资源包引用发布后不可变。普通资产发布的 snapshotId 为 null；promote 填写编辑候选的 Snapshot ID。`world.url` 为 `/v1/assets/{id}/revisions/{rev}/download`。

发布工具扫描真实世界的区块调色板和物品命名空间，将全部非 `minecraft` 的命名空间去重、排序后写入 `requires.namespaces`。兼容性检查要求实际目标 revision 的 `minecraft == Build.minecraft`、目标 loader 位于 `requires.loaders`、`namespaces ⊆ Build.modIds`。chorusMod 为 true 时使用包含 chorus 的构建。缺少命名空间时返回 `409 build_mismatch`，message 列出缺少的名称。资产房间和 hop 使用同一检查规则。

PackRevision.rev 等于上传 ZIP 全部字节的 SHA-256；sha1 从同一份 ZIP 计算，供 Minecraft 配置使用。pack 下载路径为 `/v1/packs/{id}/{rev}.zip`。资源包按敏感文件处理，通过私有网络和下载授权提供。

### 2.5 Snapshot 与 Restore

| Snapshot 字段 | 类型与含义 |
| --- | --- |
| `id` | 不透明、永久稳定的快照 ID |
| `roomId`、`roomGeneration` | 来源房间和启动周期 |
| `buildId`、`minecraft` | 来源构建及 Minecraft 版本 |
| `asset` | 编辑候选来源 `{id:string,rev:string}`；其他快照为 null |
| `capturedAt` | 源 Pod 已退出、世界停止写入的时间 |
| `createdAt` | 完整上传、文件校验和元数据发布完成的时间 |
| `sha256`、`size` | 归档文件摘要和字节数 |
| `localAvailable` | 本地是否存在完整且校验通过的文件 |
| `tankVerifiedAt` | tank 文件校验通过时间；尚未校验为 null |
| `restore` | Restore 或 null |

Restore 为 `{attempt:integer,status:"queued"|"running"|"succeeded"|"failed",requestedAt:timestamp,startedAt:timestamp|null,finishedAt:timestamp|null,error:Error|null}`。attempt 从 1 开始递增。成功、失败均填写 finishedAt；失败 code 为 archive_failed。AssetRestore 使用同一结构并增加 `revision:string`，固定本次恢复的资产 revision。

只有已经完整上传并校验成功的 Snapshot 才进入查询集合。快照生成阶段由 Room.archiving 表示。恢复任务持久化，服务重启后继续处理。

### 2.6 HopDestination

HopDestination 为 `{dest:string,kind:"asset"|"room",id:string,title:string,roomId:string|null,buildId:string,state:string}`。

dest 为 `asset:<assetId>` 或 `room:<roomId>`。id 是对应资产 ID 或 roomId；title 是资产标题或房间显示名；buildId 始终是来源房间的固定构建。roomId 在已找到目标 Room 时填写，否则为 null。state 使用目标 Room 状态；尚未存在可复用启动周期时为 `available`。

## 3. Git、构建与下载

### 3.1 Git 查询

| 方法与路径 | 请求 | 成功响应 |
| --- | --- | --- |
| `GET /v1/health` | 无 | 200 + Health |
| `GET /v1/meta` | 无 | 200 + Meta |
| `POST /v1/git/refresh` | 空 body | fetch/prune 完成后 204；同步登记分支并发出 Git 事件 |
| `GET /v1/branches` | 无 | 200 + Branch[]，按 name 排序 |
| `GET /v1/commits` | `branch` 默认配置分支 main；`limit` 默认 50，正整数；可选 `before=<完整SHA>` | 200 + Commit[] |

commits 的 before 是上一页最后一个 SHA；下一页从同一分支当前拓扑序中该提交之后开始，不重复该提交。没有更多提交时返回 `[]`。分支或游标提交不存在返回 404。刷新只读取配置远端已经存在的提交和 refs。远端新分支自动登记启用 loader 的 branch Room；删除分支将其 Room 标签改为 finished，按生命周期处理。

### 3.2 Build 操作

| 方法与路径 | 请求 | 成功响应 |
| --- | --- | --- |
| `GET /v1/builds` | 可选 `sha`、`loader`、`status` 精确过滤 | 200 + Build[]，按 queuedAt 降序，同时间按 id 升序 |
| `POST /v1/builds` | `{sha:SHA,loader:string,force?:boolean}`；force 默认 false | 新建尝试为 202 + Build；复用为 200 + Build |
| `GET /v1/builds/{buildId}` | 无 | 200 + 当前 Build，包括未完成或失败状态 |
| `GET /v1/builds/{buildId}/log` | 无 | 200、`text/plain; charset=utf-8`；queued 时可以为空 |

构建键为 `(完整SHA,loader)`。构建列表包含排队、进行中、失败及仍在缓存中的 ready 构建。相同构建的并发请求合并到同一次尝试，全局同一时刻最多运行一个 Gradle 构建。

failed 构建从 finishedAt 起保留七天，随后删除构建记录和各次尝试日志；单项 GET 返回 404，Branch/Commit 摘要变为 null。再次请求同一构建键会递增永久保留的 attempt 计数。

force 的处理：queued/building 复用当前尝试；failed 创建下一尝试；ready 且没有任何非 closed 房间引用时创建下一尝试；ready 正被引用时返回 `409 conflict`。开始下一尝试时递增 attempt，重置 queuedAt，将 minecraft、loaderVersion、java、builtAt、durationSec、startedAt、finishedAt、lastError 设为 null，files、modIds 设为 `[]`，日志切换到本次尝试。异步结果必须匹配 `(buildId,attempt)`。

普通 POST 和 Room.start 不隐式重试 failed 构建。用户显式 force 后，再启动失败房间。ready 构建按 LRU 保留配置数量，被任何非 closed 房间引用的构建不可淘汰。淘汰后 manifest、文件和日志一起退出查询集合，单项 GET 返回 404，Branch/Commit 摘要变为 null；再次请求该键会重新构建。完整 SHA 不同却形成相同短 Build ID 时返回 `409 conflict`，不能覆盖另一个构建。

### 3.3 文件下载

| 路径 | 内容类型与成功响应 |
| --- | --- |
| `GET /v1/builds/{buildId}/files/{name}` | 200、`application/java-archive`，manifest 中该文件的完整字节 |
| `GET /v1/deps/{sha256}` | 200、`application/java-archive`，内容寻址依赖 |
| `GET /v1/mods/chorus-hop/{mc}/{file}` | 200、`application/java-archive`；file 为 `<sha256>.jar`，固定 hop 内容 |
| `GET /v1/assets/{id}/revisions/{rev}/download` | 200、`application/gzip`，顶层为 world/ 的完整 tar.gz |
| `GET /v1/packs/{id}/{rev}.zip` | 200、`application/zip`，固定资源包字节 |
| `GET /v1/snapshots/{id}/download` | 200、`application/gzip`，固定快照字节 |

所有文件发布前验证大小及摘要，调用者下载后也验证。不存在的对象或文件返回 404；快照、资产或资源包仅在 tank、尚无本地完整文件时返回 `409 archive_failed`。文件被下载或用于初始化世界期间受保留保护。准备中的 build 文件不可下载。

chorus-hop 的发布文件按 SHA-256 独立保存，每个 Build attempt 同时保留对应 jar，`/v1/builds/{id}/files/{name}` 与 manifest 的 canonical URL 返回相同字节。更换配置中的 hop jar 只影响新 attempt，已经发布的下载路径保持原内容。

资产 archived 状态约束进入行为；已经本地可用的固定 revision 仍按下载授权提供。历史资产 revision 下载需要文件在本地可用。资产恢复操作针对 latestRev。

## 4. Room 与生命周期

### 4.1 ID、查询与登记

branch ID 为 `<slug(branch)>-<loader>`；commit ID 为 `c<sha前8位>-<loader>`；asset ID 为 `a-<assetId>-<sha前8位>-<loader>`。slug 转小写，将非 `[a-z0-9]` 字符替换成 `-`，合并连续 `-`，去除首尾 `-`。整个 roomId 最长 40 字符；branch 名过长或发生 slug 冲突时截断并附加 `-<原分支名SHA256前4位>`。空 slug 使用 `branch-<同一哈希前4位>`。无法唯一对应的短 ID 返回 `409 conflict`，不能复用不同身份的 Room。Kubernetes 对象名为 `room-<roomId>`。

| 方法与路径 | 请求 | 成功响应 |
| --- | --- | --- |
| `GET /v1/rooms` | 可选单值 `kind`、`state` | 200 + Room[]，按 id 排序 |
| `POST /v1/rooms` | `{kind:"commit"\|"asset",sha:SHA,loader:string,seed?:string,asset?:string}` | 新登记为 201 + Room；相同固定 ID 为 200 + 原 Room |
| `GET /v1/rooms/{id}` | 无 | 200 + Room |
| `POST /v1/rooms/{id}/start` | 空 body、`{}` 或 `{seed:string}` | 已验证 ready 为 200 + StartResult，其余已受理为 202 + StartResult |
| `POST /v1/rooms/{id}/stop` | 空 body | 202 + Room；已停止或 closed 为 200 + Room |
| `POST /v1/rooms/{id}/restart` | 空 body | 202 + Room，按停止、归档、新周期顺序执行 |
| `PATCH /v1/rooms/{id}` | `{label:"testing"\|"finished"}` | 同步更新为 200 + Room；引发停止归档为 202 + Room |
| `DELETE /v1/rooms/{id}/leases/{leaseId}` | 无 | 204 |
| `GET /v1/rooms/{id}/log` | 无 | 200、`text/plain; charset=utf-8`，最近服务端日志，最多 200 行 |

branch 由远端分支自动登记。commit 创建时 seed 默认配置 default_seed，当前为 empty；asset 创建必须携带 asset ID，种子由该资产决定，不能携带 seed。kind、完整 SHA、loader、asset ID 为固定身份字段；重复创建与原身份不一致返回 `409 conflict`。重复创建不改变保存的 seed、label 或运行状态。

新登记的 commit/asset 也使用 stopped，generation=0，尚未分配 PVC。asset 创建时要求资产 active、兼容且所需文件可用。固定 ID 可以在 closed 后再次启动。

### 4.2 start 和 generation

| 当前情况 | 处理 |
| --- | --- |
| label=finished | 409 room_finished |
| stopped，label=testing | 接受下一 generation；已有 PVC 的世界保留 |
| closed 的 commit/asset | 接受下一 generation，创建新的 PVC，重新解析下一周期来源 |
| closed 的 branch 已改回 testing | 接受下一 generation，从最后成功快照恢复；从未产生世界的登记记录使用初始 seed |
| failed，旧 Pod 已退出 | 接受下一 generation，保留已有世界或已固定但尚未初始化的来源；构建失败须先 force 重建 |
| queued/building/starting | 复用 generation，发放新租约 |
| ready | 复用 generation；响应前同步重新进行一次超时 3 秒的路由 SLP 核验，通过返回 200；失败改为 starting、phase=re-verify，返回 202 |
| stopping/archiving | 409 conflict |
| lost | 409 node_lost |

所有新周期在持久化事务中固定 generation、SHA、buildId、seedResolved，并登记租约。已有 Pod 仍可能写入世界时，失败重启返回 `409 conflict`。重复 start 不重复初始化世界。同一 generation 的 buildId 保持固定。

start 的 seed 只允许 commit 在首次启动或 closed 且旧 PVC 已清理时覆盖。branch/asset 携带 seed 返回 400。已受理或保留世界的 commit 重复请求携带相同 seed 时复用固定来源；携带不同 seed 返回 `409 conflict`。closed commit 再次启动重新解析保存的表达式；closed asset 固定当时的 latestRev。运行中的 asset 周期保持已有 revision。

就绪条件为 Pod Ready，且通过 mc-router、使用该房间主机名的 SLP 返回 MOTD，包含全部匹配的 `room=<id> build=<buildId> generation=<n>`。只有满足条件才写入 ready。MOTD 模板为 `§7chorus §f<id>\n§8room=<id> build=<buildId> generation=<n>`。节点、Pod、路由验证结果更新时必须检查 generation，旧周期不能更新新周期。

构建未完成时进入 building；等待容量时 queued；种子恢复使用 §5 的 queued；创建或扩容后进入 starting。Pod 连续未就绪超过配置的启动期限（默认 300 秒）时，lastError 使用 start_timeout。控制器先保存最后 200 行诊断日志，再缩容到零，确认 Pod 退出后进入 failed，保留 PVC；清理期间 phase=stopping failed server。failed 不占运行容量，日志接口继续提供该 generation 保存的诊断日志。Pod 已 Ready 而路由 SLP 失败时保持 starting、phase=route unavailable，持续重试，不因路由等待超时停止 Pod。Pod 恢复 Ready 后清除连续未就绪计时。容量等待自动继续，不返回拒绝已受理请求的容量错误。

每个房间独立调和。单个房间操作失败时记录该房间的 lastError，其他房间继续推进；后续调和继续重试。控制器重启后继续尚未完成的失败清理。

Kubernetes 对象、归档 Job 的 `chorus.dev/generation` 标签、房间 token、MOTD 和 Snapshot.roomGeneration 都绑定同一周期。controller 重启从持久化状态接管已有对象，不重复登记周期。

### 4.3 stop、restart、标签和失联

stop 执行保存、缩容、等待 Pod 退出、归档上传和校验。正常完成后：branch 保留 PVC 并 stopped；commit/asset 进入 closed，资产世界快照成为编辑候选。归档失败保持 archiving，lastError=archive_failed，保留 PVC。尚未分配世界的周期可以直接停止，不生成快照。

finished branch 先停止并完成归档，再 closed。持久化世界和对象的删除必须同时符合当次执行指令的数据保留权限。设置 testing 只恢复可启动标签，不自动启动。重复 stop 复用已有停止或归档任务；branch 已停止且世界未再写入时复用该周期已有快照。

restart 完成原周期停止和归档后才接受下一周期；停止或归档失败不创建新周期。branch 使用最新远端 head，并保留世界；其他 kind 使用其固定 SHA 和相应新周期种子规则。运行中的 branch head 移动只更新 behindHead，不替换正在使用的构建。

branch 固定调度 encore；commit/asset 优先在线的 seraph，否则 encore。节点 NotReady 持续超过 60 秒，相关房间变为 lost；节点持续不可用时保持 lost。节点恢复 Ready 后观察原 Pod：Pod 仍存在、Ready 且 generation/build 身份匹配时进入 starting，通过正常路由核验重新 ready；其他情况进入 stopping，执行保存、缩容和归档，branch 保留 PVC。失联前已经请求停止、完成标签或失败清理的房间继续相应清理。失联不会产生成功归档记录；对象清理遵守数据保留权限。

### 4.4 租约与空闲

每次受理 start/hop 发放有效期 180 秒的新租约，同一 generation 复用房间。租约不自动延长；旧 generation 的租约不保护新周期。任何有效租约都暂停空闲计时。构建、排队阶段不执行 ready 房间的空闲回收。

玩家数量以经 mc-router 的 SLP `players.online` 为准，每 15 秒探测；Room.players 是可得的 sample，空 sample 不等于零玩家。玩家数量为零且没有有效租约时记录 idleSince；连续空闲 600 秒执行 stop。branch、commit、asset 的空闲时间相同。

租约删除在已经到期或已删除时仍返回 204；只能删除自己的有效租约，越权返回 401。来源已退出旧周期的有效房间 token 仍可取消其创建的旧租约。取消租约不停止其他玩家或其他租约正在使用的房间。

## 5. 世界种子与自动恢复

seed 语法为 `empty`、`asset:<assetId>`、`branch:<完整分支名>`、`snapshot:<snapshotId>`。初始化新世界的 start 只解析一次，把结果写入 seedResolved；构建排队、容量等待和初始化重试保持该来源。已有 PVC 的 branch 启动及失败重试保留现有世界。

branch 种子查找同 loader 的源分支房间，依次选择：

1. 最新一个完整上传并发布的 Snapshot。
2. 尚无快照时，使用源房间已经固定的 seedResolved。
3. 来源仍为 null 时，使用 `{kind:"empty"}`。

源房间不存在返回 `404 not_found`。源房间运行、有在线玩家、正在停止或归档时仍按上述规则选择已发布来源。此操作不停止源房间，不复制正在写入的 PVC，不改变玩家状态。快照不设年龄上限，客户端可显示 capturedAt。需要最新世界时，用户显式停止源房间并等待快照完成。

asset 种子固定该资产当前 latestRev；snapshot 种子固定对应 Snapshot ID。快照的 Minecraft 版本必须与房间配置和最终 Build 一致；不要求源快照和目标使用相同 buildId。已知不兼容的种子请求返回 `400 invalid_request`；资产的 loader/命名空间兼容性还须满足 §2.4。

选中的 Snapshot 本地不可用、只在 tank 时，start 仍受理，返回 202；Room 为 queued、phase=`restoring seed`。chorusd 创建或复用该快照的持久化恢复任务，校验完成后自动继续启动。恢复失败则 Room.failed，lastError 使用 archive_failed，可带 details.snapshotId。该过程同样适用于显式 snapshot seed 和 branch 选出的快照。

自动恢复期间不占用已经运行的房间容量，不启动会写入世界的 Pod。初始化完成前保留固定来源的本地文件。下载和恢复使用完整文件摘要与大小校验，通过后才发布并解包；seedResolved 不因远端 head 或 latestRev 后续移动而改变。

## 6. SSE 与客户端进入

`GET /v1/events` 订阅全部房间与构建；`GET /v1/rooms/{id}/events` 订阅一个 Room 及其引用的 Build。不存在的单房间返回 404。响应为 `text/event-stream`，使用 UTF-8 JSON data。

每次连接的第一条事件为 `sync`。取得当前对象快照和接入内存订阅在同一个串行步骤完成，后续变化排在 sync 之后。服务端忽略 Last-Event-ID；每次重连重新同步当前状态。事件 id 使用进程内递增序号，仅用于调试，服务重启可以重新计数。

| event | data |
| --- | --- |
| `sync` | `{rooms:Room[],builds:Build[]}`；全局返回当前全部 Room 及 §3.2 的构建集合；单房间返回该 Room 和引用的现存 Build |
| `room` | 完整 Room |
| `build` | 完整 Build |
| `ready` | `{id:string,generation:integer,host:string,port:integer,buildId:string}`；表示该周期当前达到 ready |
| `failed` | `{id:string,generation:integer,error:Error}` |
| `branch_added` | 完整 Branch，仅全局订阅 |
| `branch_moved` | 完整 Branch，head 为新值，仅全局订阅 |
| `branch_deleted` | `{name:string}`，仅全局订阅 |

每 15 秒发送注释行 `: ping`，后接空行。客户端使用 sync 替换订阅范围内的状态，room/build 使用完整对象更新；Git 事件触发分支及所选提交列表刷新。客户端断线后退避重连；内存订阅不能继续交付时关闭该连接，使客户端重新取得 sync。

Ruri Chorus 的每次加入操作遵守：

1. 从 start 响应固定目标 `(id,generation,buildId)`，并开始观察房间及准备本地实例、模组。
2. start 响应、sync、room 事件都可以提供当前 Room；匹配目标的 ready 事件表示该目标 state=ready。最新观测同时满足相同 id、generation、buildId、state=ready，且本地模组准备完成，就启动游戏。
3. 每次加入操作最多启动一次；先准备好模组或先观察到 ready 均可。后续状态变化必须更新当前就绪判断，不能保留已经失效的 ready 状态。
4. 观察到目标房间 generation 改变，或 state 变为 failed、stopping、archiving、closed、lost，放弃本次加入并显示原因。
5. 等待期间每 120 秒重新 start 续租；检查返回的 generation/buildId，保存该操作新取得的租约。用户取消时删除该加入操作仍有效的租约。

ready 的进入许可来自已核验的 Room 当前状态。客户端按 §2.2 只安装 both/client 文件，全部 SHA-256 核验完成后使用 room.host/port 启动；只删除自己的旧托管模组。

## 7. 资产、资源包发布与归档

### 7.1 世界发布

`GET /v1/assets` 返回 200 + Asset[]，按 id 排序。

`POST /v1/assets/{id}/revisions` 使用 multipart/form-data，包含两个命名 part：world 为 `application/gzip` 的 world.tar.gz 原始字节；asset 为 `application/json` 的 AssetMetadata。asset.id 必须与路径一致，resourcePack 非空时必须指向已经发布且可用的固定 PackRevision。

归档顶层为 world/，必须包含有效 MC 26.3 的 level.dat，校验存在的维度及区块结构，包括 dimensions/minecraft/overworld/region。Chorus 在世界根目录生成的 chorus-checkpoint.json 随世界完整保存。拒绝目录穿越、链接、特殊文件和原始解包资源。发布工具拒绝 `/home/linwhite/d2-assets/sensitive` 及其解析后的子路径。命名空间来自实际扫描，按 §2.4 填写。

服务端计算世界文件 SHA-256，与该资产 latestRev 的世界摘要比较。相同时返回 `200 +` 已有 AssetRevision，保留原 revision 和元数据；不同时原子分配下一个整数 revision，返回 `201 + AssetRevision`。新资产从 `"1"` 开始。并发发布在同一资产的提交顺序中执行摘要判断和编号，不产生重复编号。

校验完成后，在同一事务写入 revision、latestRev、世界文件及 revision 元数据的 tank 同步任务，并确保固定资源包已有同步任务或校验记录。新资产为 active；现有资产保持当前 active/archived 状态。运行中的资产房间继续使用自身固定的 revision。

### 7.2 编辑候选转为 revision

`POST /v1/assets/{id}/promote` 请求为 `{edit:"<snapshotId>"}`。Snapshot 必须是该资产房间生成的编辑候选，文件已在本地校验通过。来源不匹配返回 400；不存在返回 404；文件需要恢复返回 `409 archive_failed`。

新 revision 的世界来自快照，snapshotId 填写该 Snapshot ID；requires.namespaces 根据待发布快照的实际内容重新扫描，其余元数据沿用快照来源 revision。编号、摘要幂等判断、Asset 状态保持和同步任务登记使用 §7.1 的规则：世界摘要等于 latestRev 时 200 + 已有 revision，否则 201 + 新 revision。

### 7.3 资源包发布

`POST /v1/packs/{id}/revisions` 使用 `application/zip`，body 为 pack.zip 原始字节。ZIP 必须是有效资源包，元数据来自 pack.mcmeta。服务端计算同一份 ZIP 的 SHA-256、SHA-1 和大小。首次发布返回 201 + PackRevision；同一 pack ID 和相同字节重复发布返回 200 + 原对象。固定 revision 不可覆盖。

pack ID 使用单个路径段，不能为空、`.` 或 `..`，不能包含 `/`、`\`、冒号或控制字符；违反要求返回 `400 invalid_request`。

发布资源包时，同一事务登记 ZIP 及 revision 元数据的 tank 同步任务。重复发布复用已有任务和校验记录。客户端游戏使用 publicBaseURL 下的签名 pack 下载 URL，服务器配置对应 SHA-1。

### 7.4 设置资产状态

`PATCH /v1/assets/{id}` 请求为 `{state:"active"|"archived"}`。设置 active 调用 §10 相同的恢复流程，返回 200/202 + Asset。

设置 archived 要求：所有已经接受启动的该资产房间均已 closed，且 latestRev 的世界、固定资源包及 revision 元数据均已在 tank 校验通过。generation=0 的登记记录不阻止归档。仍有运行周期返回 `409 conflict`，调用者先显式停止房间。

PATCH 会幂等登记缺失的同步任务，将此前失败且本地文件完整的任务重新排队，并复用正在运行的任务。尚未全部校验通过返回 `409 archive_failed`，保留 active；后台同步完成后须再次 PATCH，才设置 archived 并返回 200 + Asset。重复设置已经 archived 的资产返回 200 + Asset。文件依保留规则释放，未获 tank 校验的文件保留。

## 8. hop

`GET /v1/hop/destinations?fromRoom=<id>` 使用绑定来源当前 generation 的房间 token，返回 200 + HopDestination[]，按 kind、id 排序。来源周期无效返回 401。

资产条目仅包含 active、实际目标 revision 与来源 Build 兼容且初始化文件可用的资产。已有周期或保留世界的失败周期使用其 seedResolved；初始化新世界使用 latestRev。目标处于 stopping、archiving、lost 时不列入。固定资产 Room 已 closed 时可按新周期规则再次提供资产目的地。

普通房间条目为相同 buildId、label=testing、state 在 stopped/queued/building/starting/ready 中的其他房间。stopped branch 按下次将固定的当前 head 计算 buildId。来源房间不列入。资产房间可据此返回同构建的 branch/commit。

`POST /v1/hop` 请求为 `{fromRoom:string,dest:string,player:{uuid:string,name:string}}`。来源必须是 token 绑定的当前 ready 周期，否则状态冲突返回 409 conflict、身份错误返回 401。dest 优先使用 `asset:<id>`、`room:<id>`；裸资产名和 roomId 仍接受，同时匹配两者返回 `409 conflict` 并要求限定形式。不存在返回 404。

目标资产使用来源 buildId；受理时重新验证实际 revision 的 Minecraft、loader 和 namespaces。普通房间跨 build 返回 409 build_mismatch；stopped branch 的 head 在列表展示后变化，也须重新检查。相同 buildId 的目标保持相同客户端模组集合。

成功响应为 StartResult；目标已处于 ready 时也执行 §4.2 的同步 SLP 核验，通过返回 200，其他已受理状态返回 202。容量不足保持 queued。chorus-hop 每 2 秒 GET 目标 Room，generation、buildId 匹配且 state=ready 时执行 transfer；失败或等待超过 300 秒向玩家显示原因并取消本次租约。网络请求异步执行，transfer 在服务器主线程执行。

## 9. 快照上传、查询与恢复

| 方法与路径 | 请求 | 成功响应 |
| --- | --- | --- |
| `GET /v1/snapshots` | 可选 `room=<roomId>` | 200 + Snapshot[]，按 capturedAt 降序，同时间按 id 升序 |
| `GET /v1/snapshots/{id}` | 无 | 200 + Snapshot |
| `POST /v1/snapshots` | 必需 query：room、build、generation；`application/gzip` body | 首次成功 201 + Snapshot；相同内容重试 200 + 原 Snapshot |
| `POST /v1/snapshots/{id}/restore` | 空 body | 本地完整文件已可用为 200 + Snapshot；新建或复用恢复任务为 202 + Snapshot |

快照上传为内部房间 token 接口。服务端验证签名、有效期、roomId/generation/buildId；新上传还必须匹配当前周期的归档任务。capturedAt 使用 controller 已确认的 Pod 退出时间。归档任务封存一次完整文件，上传重试继续发送相同字节。

同一 `(roomId,generation)` 的停止归档只产生一个 Snapshot。相同 tuple 和内容的重试幂等；不同内容返回 `409 conflict`。已发布后，有效旧 token 可以确认原 tuple 的同一文件，即使房间进入下一周期；它不能改动快照、当前状态或新周期。首次上传必须完成大小、摘要及存档结构校验后才发布对象。

显式 restore 在没有进行中任务且确实需要从 tank 取回时创建下一 attempt。失败后重试、成功后本地文件再次淘汰都递增 attempt；queued/running 的重复请求复用当前 attempt。快照不存在返回 404；没有已经校验的 tank 来源返回 `409 archive_failed`。start 自动恢复按 §5 受理并报告异步结果。

恢复写入独立工作目录，完整校验后原子发布文件，设置 localAvailable=true、restore.status=succeeded。此前不能作为世界输入。恢复失败保持文件不可用，restore.status=failed、error.code=archive_failed。

## 10. 资产恢复与同步条件

`POST /v1/assets/{id}/restore` 与 `PATCH /v1/assets/{id} {state:"active"}` 使用同一流程。受理时固定 latestRev，恢复该 revision 的世界文件及其固定资源包，Asset.restore.revision 保存这一值。

本地全部文件已经完整时，直接校验、设置 active，返回 200 + Asset。需要从 tank 取回或同一尝试正在运行时返回 202 + Asset。恢复期间状态保持 archived；全部依赖校验完成后，同一事务发布文件可用性、restore.status=succeeded 和 state=active。失败保持 archived，使用 archive_failed。

重复恢复复用进行中的 attempt；无进行中任务且需要取回时递增 attempt，覆盖失败重试及再次归档、淘汰后的恢复。不存在返回 404；缺少已校验 tank 来源返回 409 archive_failed。active 且文件完整时重复请求返回 200，保持正在运行的房间。

restore 为 queued/running 时，publish、promote、再次设置 archived 返回 `409 conflict`。archived 或正在恢复的资产不能新建资产房间、启动或 hop，返回 `409 conflict`。恢复完成不自动创建房间、发放租约或替换已有世界。客户端通过 GET /v1/assets 的 Asset.restore 观察进度。

tank 同步使用持久化队列，完成远端 SHA-256 校验后才设置 tankVerifiedAt 或对应 revision 的校验记录。本地保留每房间最近 5 个快照及 14 天内全部快照；任何正在使用的文件和未校验文件继续保留。生产 tank 身份配置完成前，同步保持关闭，热缓存不淘汰文件；已登记任务继续持久保存。

## 11. 节点与内部通知

| 方法与路径 | 请求与响应 |
| --- | --- |
| `GET /v1/nodes` | 用户 Bearer；200 + Node[]，按 name 排序 |
| `POST /v1/nodes/{name}/drain` | 用户 Bearer、空 body；202 + Node，执行该节点房间的 stop 流程，全部安全停止后 cordon；节点不存在为 404 |
| `POST /v1/hooks/mc-router` | 部署内部调用；接收固定 mc-router 版本的 connect/disconnect JSON 通知，触发对应房间 SLP；204 |

固定 mc-router 1.47.2 的通知对象为 `{event:string,timestamp:timestamp,status:string,server:string,client?:{host:string,port:integer},player?:{name:string,uuid:string},backend?:string,error?:string}`。本部署启用 event=connect/disconnect；status 为 success、missing-backend 或 failed-backend-connection；server 为握手主机名，backend 为已确定的后端地址。client、player、backend、error 按上游实际可得信息省略。

接收端不依赖通知中一定存在玩家资料。通知通过容量为 1 的待办信号合并，由同一个后台循环执行调和；通知对应房间跳过本轮 15 秒 SLP 探测间隔。transfer 连接的玩家数量仍由 SLP 核验。房间未匹配的通知不创建房间或改写玩家数。drain 中归档失败保留房间和世界，客户端通过 Room 状态观察原因。
