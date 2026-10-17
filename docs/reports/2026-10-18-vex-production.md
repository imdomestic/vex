# Vex 正式迁移执行记录

## 应用与配置

首次正式迁移使用提交 `b9c48aad76ab4b5ca42ae9fb42a76cf372db9afa`，通过 Go 构建、测试、vet、race、Gradle 测试和 Nix 整包测试。构建重试、CI 环境和默认世界种子的当前代码提交为 `7b42dd45dc3c99a96feabe2f16992b25da13892f`；应用路径为 `/nix/store/4l9zkw0drsxsv40y0jlaspanpkdv1090-vex-applications`，应用 profile 为第六代。初次完整历史扫描覆盖 18 个提交、483 个文件对象及 68 个归档成员；提交信息、工作目录和六种字符编码的可读内容均完成检查，真实凭据发现数量为零。证据为 `.work/vex-source-audit-20261017T185232Z/review.json`。

`feat/vex-p1` 的应用源码提交 `7b42dd45dc3c99a96feabe2f16992b25da13892f` 已推送至私有 `imdomestic/vex` 与 encore 的裸仓库，两端 `git ls-remote` 一致。nix-config 的模块输入仍锁定 `b9c48aa`，本次应用修复没有修改模块；应用 profile 与系统配置分别部署。登录验收见 [独立报告](2026-10-18-vex-login.md)。

当前代码的完整扫描覆盖19个提交、498个文件对象、68个归档成员、提交信息和六种编码的可读字符串，真实凭据和未分类候选均为零；结果为 `.work/vex-source-audit-20261017T195921Z/review.json`。报告提交前再次执行 `go vet ./...`、`go test -p 2 ./...` 和实际 Nix 构建，全部退出0；Nix 输出仍为 `1sr1yz4kzc7bq378f19swrqmdfn80jf6-vex-applications`。

## 系统候选与预演

| 主机 | 候选系统 | 预演差异 |
| --- | --- | --- |
| encore | `/nix/store/kdyrv2jm7pmcd3pqz8ky5f4i1sy7p2jp-nixos-system-encore-26.05.20260911.21a67dc` | 停用原控制器和 DNS；k3s 缓存名称改变；Vex 正式配置、用户和目录规则；nftables、polkit reload |
| seraph | `/nix/store/kg80nfw7gr4cmfisn98zws0f84cpfmyd-nixos-system-seraph-26.05.20260911.21a67dc` | k3s 的 TMPDIR 与 CacheDirectory 名称改变，引起 k3s 重启 |
| tank | `/nix/store/y0l2npk6z65a9xpkfqaih7maqgrc1h9l-nixos-system-tank-26.05.20260911.21a67dc` | 归档用户、公钥文件和目录规则改名；accounts-daemon/tmpfiles 重建，dbus reload，polkit/sshd restart |
| h610 | `/nix/store/lmbbl02mmk1nc57m7jiq9kbqlx38344j-nixos-system-h610-26.05.20260911.21a67dc` | Headscale split DNS 从 `chorus.internal` 改为 `vex.internal`，地址保持 `100.64.0.43`；Headscale 重启 |

tank 预演另外报告三个现存 gdm-greeter 用户的 UID 变化不会应用。所有候选均经过构建；seraph 与 h610 闭包没有软件版本变化。完整预演和闭包证据位于 `.work/logs/production-final-*.log`、`tank-production-dry.log` 和 `host-preflight-*.log`。

每台主机激活前记录 `/run/current-system`，安排 600 秒自动恢复任务，然后执行候选的 `switch-to-configuration test`。从 Mac 新建 SSH 逐项检查后，停止计时器、设置系统 profile 并执行 `switch`。encore 的 tailscaled、网卡和网络访问规则不进行测试性中断。

encore 的旧自动部署文件 `/var/lib/rancher/k3s/server/manifests/chorus-metrics-server.yaml` 在切换前移动至 `/home/linwhite/vex-work/.work/migration/chorus-metrics-server.yaml`，保留原文件。新配置安装 `vex-metrics-server.yaml`，继续管理同一个 metrics-server Deployment；切换后检查 Ready 和 AddOn 归属注解。旧 AddOn 记录保留，避免触发 Kubernetes 资源删除。

## 数据操作与执行门槛

生产迁移使用 `.work/vex-p0/prepare-production-migration.sh` 与 `apply-production-migration.sh`。前者停止原控制器和验证控制器，确认没有构建进程、两个构建锁可独占、SQLite WAL 已清空，保存旧数据库并生成新的完整 dry-run 计划。核对该计划后执行正式迁移，随后逐项验证文件摘要、generation 和记录计数；原始 main-g9 SHA256 必须保持 `cfd650e0fc26633ebaaf797d5bc877d7a16887fc5eadce59b8acf87d48fb1dcb`。

停止控制器后的最终计划已保存为 [完整迁移计划](2026-10-17-vex-migration-plan.json)，计划 SHA256 为 `3f68af5216fbbb5da938f8a561c56f0fa60a575a5bfcefc161ce3f674233c3fe`。保留 6 个房间、18 个快照、42 条传输、1 个世界和 3 个 revision；与此前审阅计划相比，仅分支房间的观察时间更新，源记录摘要、全部文件摘要与移动目录保持一致。

tank 使用用户已授权的 `.work/vex-p0/tank-migrate.sh`：归档用户保留 UID979/GID970；归档根改为 `/srv/archive/vex`，世界与快照进入 `projects/chorus/`；所有 legacy、it-* 和未完成的传输文件均保留。P0 剩余目录的完整 tar.gz 正在由已打开的文件描述符写入；目录重命名保持该文件的 inode 与传输进程，后续核验使用新路径 `/srv/archive/vex/legacy/vex-p0-20261018/remaining-via-mac.tar.gz`。完整文件核验成功前，encore 的 P0 原目录保持原状。

归档集成测试的目标为 `/srv/archive/vex/it-p1-r18b-{archive,queue,world,seed,retention}`。迁移后创建这五个独立空目录，权限 0700，属主 vex-archive；产生的文件全部保留。测试命令与 namespace 清单记录于 P1 报告。

系统自动恢复任务只恢复系统配置；文件迁移由持久迁移记录管理。若数据迁移中断，保持两端控制器停止，先核对记录，再使用 `vexd migrate chorus --resume /srv/vex/state/migration-chorus.json` 继续。不会通过切换系统配置逆向移动世界目录。

## 正式激活与迁移结果

2026-10-18 04:09 JST 前，四台主机均完成 test、新 SSH 核验与持久 switch，运行系统与系统 profile 均为上表路径，四个保护计时器均已停止。encore 的 vexd、vex-dns、k3s、mysql、postgresql、sshd、tailscaled 全部 active；seraph 为 Ready；tank 的专用 SSH 身份与七项 SSH 限制通过；h610 的 Headscale 健康检查返回 200。证据为 `.work/logs/production-<主机>-{test,verify,switch}.log`。

metrics-server 为 Ready，实际 `objectset.rio.cattle.io/owner-name` 已成为 `vex-metrics-server`。正式 mc-router 在 `vex-system` 提供 LoadBalancer 25565，原 router 已缩容到零、Service 改为 ClusterIP。Mac `dscacheutil -q host -a name main-fabric--chorus.vex.internal` 返回 `100.64.0.43`，该地址的 25565 可连接。encore 激活时额外启动 NetworkManager-dispatcher；h610 持久 switch 额外启动现有 ollama-model-loader，均已记录于激活日志。

迁移数据库可通过正式 API 读取 6 个旧房间、18 个快照、plaza 的 3 个 revision；main 保留 generation 9，种子为 `main-fabric-g9`。旧 runtime 与租约未迁入。新数据目录为 `/srv/vex`，SQLite 位于 `state/vexd.db`，原数据库一致副本位于 `/var/lib/vex/migration-backup/chorusd.db`。原 main PVC 的 UID 仍为 `998937db-c633-4431-bdd2-68331e8127be`。

42 条迁移传输记录全部保持 verified，3 个世界 revision 的 tankVerifiedAt 均非空。2026-10-18 再次完整读取两端对应的 42 个文件，合计 110,078,269 字节，大小与 SHA256 全部符合迁移记录。命令使用 `.work/vex-p0/production-data-audit.py` 的 database 与 files 操作；结果位于 `.work/logs/production-{local,tank}-file-verification.log`。tank 保留原 UID979/GID970、legacy 和全部 it-* 内容。

vex-build 实际不能读取签名密钥、归档私钥、kubeconfig 和数据库。管理 socket 为 vexd:vex-admin 0660，linwhite 可通过新 SSH 会话使用。正式 API 的自动验收会话由一次性邀请生成，只保存在忽略目录中的 0600 文件，收尾时吊销。

## 当前需要用户操作的验收

Mac 锁屏期间，当前 Ruri CLI 的 Microsoft 账号刷新返回 `AUTHENTICATION_REQUIRED`、Keychain `-25320`。游戏尚未启动。用户解锁并允许当前应用访问该账号后，继续正式 main、plaza 跳转和 example 两个提交的进房验收；GUI 邀请添加与界面检查同样等待解锁。

旧 `chorus-rooms` namespace、其 main PVC 和 `/srv/chorus` 的剩余内容保留，直到正式进房及新 tank 归档验收通过。功能分支在验收完成前保留，不合入 dev。

## 正式构建与房间验收

构建进程使用 `CI=true`，按 `work/<buildId>/attempt-<n>/source` 隔离源码目录。控制器在确认 systemd 单元退出后处理目录清理；worker 为自己生成的目录补齐同组写入权限。原有工作目录保留。配置默认世界种子为 `empty`；尚未启动、generation 为零且种子未设置的分支房间，在同步分支时获得默认值。Go 全包 test、vet 及 build/config/rooms/gitmirror 的 race 测试均通过，实际双 UID 文件权限和真实 Git 重试测试通过。

生产应用为 `4l9zkw0drsxsv40y0jlaspanpkdv1090-vex-applications`。补齐 Linux 集成测试文件后，完整源码的 Nix 构建得到 `1sr1yz4kzc7bq378f19swrqmdfn80jf6-vex-applications`；`vexd`、内部 `.vex-wrapped` 和 scanner 文件逐字节相同，CLI 包装脚本仅有自身 Nix store 路径差异。证据：`.work/logs/production-final-{go-tests,go-race,nix,integration-nix}.log` 与 `final-artifact-comparison.log`。

| 正式构建 | attempt | 耗时 | 结果 |
| --- | ---: | ---: | --- |
| chorus `fabric-f4555f8f0300` | 4 | 41.88 秒 | ready，三个文件 |
| example `fabric-3cf4f9d4009a` | 1 | 78.60 秒 | ready，三个文件 |
| example `fabric-932eab713246` | 2 | 11.82 秒 | ready，三个文件 |

全部九个文件经过真实鉴权下载，大小、SHA1、SHA256、SHA512 与 Build 一致；匿名下载拒绝，平台 agent 标记为 server。两个使用新代码的 worker 均已 inactive，MainPID 与 ControlPID 为零，对应源码目录已删除。证据：`.work/vex-p0/production-build-verification.json`、`.work/logs/production-worker-cleanup-verification.log`。

| 项目与房间 | 节点 | generation 与种子 | 实际检查与结束状态 |
| --- | --- | --- | --- |
| chorus `main-fabric` | encore | generation10；`main-fabric-g9` | 启动前7个 level.dat/region 摘要一致；Mac 经正式 DNS/router 核验 SLP；正常停止，g10 快照已在 tank 校验 |
| chorus `w-plaza-f4555f8f-fabric` | seraph | generation1；plaza revision4 | 正式 CLI 发布原始样板后使用新 revision 启动；SLP 通过；正常关闭并完成 tank 归档 |
| example `main-fabric` | encore | generation1；empty | SLP 通过，RCON 实际返回 `Vex Example v2`；正常停止，g1 快照已在 tank 校验 |
| example `c932eab71-fabric` | seraph | generation1；本项目 `main-fabric-g1` | SLP 通过，RCON 实际返回 `Vex Example v1`；正常关闭并完成 tank 归档 |

main 的新 PVC UID 为 `c583ce8c-07f8-4aa1-b054-8db34357eb6d`，独立于旧世界 PVC。g10 SHA256 为 `0e8d904917a2a189930d165d249a2548b65bf0f96c97e71214e68a04cd8c2343`，大小 10,961,196 字节。Chorus 项目包含新快照与 revision4 的全部48个归档文件，在本地与 tank 再次完整读取并通过摘要核验，合计121,972,436字节；example 的4个归档文件同样通过两端全量校验，合计407,058字节。房间证据为 `.work/vex-p0/production-room-*.json`，两端核验日志为 `.work/logs/production-final-{local,tank}-{chorus,example}-verification.log`。

正式 hank 邀请会话的 example 角色为 viewer；构建请求返回403 forbidden，Build 列表保持不变，测试会话随后吊销并验证401。main 的实际 room token 下载自己的 Build 返回200，请求 example Build 文件返回404。令牌仅在测试进程内使用；报告记录身份、返回码和证据路径：`.work/logs/production-viewer-verification.log`、`.work/vex-p0/production-room-main.json`。

这些检查使用实际 Minecraft 服务端、RCON、DNS、路由和归档，在线玩家数为零。真实客户端游戏启动次数仍为0/8；GUI 邀请输入、失效与重新登录显示、多服务器分组和断开提示也等待 Mac 解锁。

## Ruri 正式 prepare 与结束状态

2026-10-18 04:51–04:53 JST，`/Users/linwhite/ruri-vex-experimental/build/Ruri.app/Contents/Helpers/ruri-cli` 使用正式 8790 的邀请会话，在独立目录 `.work/vex-real-it` 完成三个 `vex prepare`。实际客户端模组集合严格等于 Build 的 `both`/`client` 文件，每个文件的 SHA1、SHA256、SHA512 均通过；`vex-agent` 保持服务端文件。实例标记中的服务器 URL、项目与房间逐项一致。

| 项目与目标 | generation | Build / attempt | 客户端实例 |
| --- | ---: | --- | --- |
| chorus / main-fabric | 11 | fabric-f4555f8f0300 / 4 | E32878E3-D0C2-4BF5-A0CF-6AA072BE9755 |
| example / main-fabric | 2 | fabric-3cf4f9d4009a / 1 | 0AD2109A-AFBA-417B-8D78-5C1771448E6F |
| example / c932eab71-fabric | 2 | fabric-932eab713246 / 2 | 0AD2109A-AFBA-417B-8D78-5C1771448E6F |

example 的同一实例在旧提交 prepare 时，将项目模组从 1.1.1 切换为 1.0.1；Chorus 使用独立实例。三个启动计划均为 Java25、Fabric KnotClient 和正式 `vex.internal` 地址。prepare 调用止于 `VexLaunchPlan.inspect`，没有调用游戏启动入口，也没有读取或刷新 Microsoft 凭据。Ruri 证据为 `.work/vex-production-prepare-*.json` 与 `.work/vex-production-verified-*.json`；报告提交 `61dc1df78cd961714f2034b751940495bea3f4e1` 已推送至 `feat/vex-experimental`，没有合入 dev。

三个房间随后通过正式 stop API 保存世界：chorus main 为 stopped/g11，example main 为 stopped/g2，example 旧提交为 closed/g2。全部新快照已完成 tank 校验。完整读取本地与 tank 后，chorus 的50个归档文件共132,936,445字节，example 的8个文件共814,126字节，大小与SHA256全部一致。证据为 `.work/logs/production-after-prepare-{local,tank}-{chorus,example}-verification.log`。自动化验收会话已吊销；Ruri 自身的登录会话保留。

最终新 SSH 核验使用 `.work/vex-p0/verify-final-state.sh`，退出状态为0。encore 与 seraph 均 Ready；sshd、tailscaled、k3s、mysql、postgresql、vexd、vex-dns 全部 active；正式 router 为 Ready；两个项目没有运行中的房间 Pod。新旧 main PVC 均为 Bound，UID保持一致；系统 profile 仅保留39和40。根分区可用63,493,206,016字节，MemAvailable为20,506,820,608字节。证据为 `.work/logs/production-final-state.log`。

实际 GUI 邀请输入、登录失效与重新登录、多服务器分组和独立断开提示，以及 chorus main/plaza 跳转和 example 两个提交的客户端进入，仍需解锁 Mac。旧 `chorus-rooms`、旧 main PVC、`/srv/chorus` 的删除及 dev 合并继续等待这些验收。独立验收应用的打开命令与逐项步骤位于下载目录的 `vex-progress-2026-10-18/RURI-R1-REPORT.md`。

## 临时环境收尾范围

用于登录与双服务器 SSE 的 `vex-login-it.service` 监听 `127.0.0.1:8792`。它实际运行完整 vexd handler、独立 SQLite 和会话逻辑；该回环地址无法被项目 Pod 访问，因此后台房间调和拒绝创建运行资源。本次测试覆盖真实登录、项目查询与 SSE，未在这台临时实例启动 Minecraft。日志和 `/srv/vex-it-login` 保留。

联测结束后已执行 `sudo -n systemctl stop vex-login-it.service`，并关闭本机专为 8792 建立的 SSH 转发。正式 8790、mc-router、数据库、集群和网络服务继续运行。

## Nix 配置提交

独立 checkout 为 `.work/vex-p0/nix-config`。按照仓库 `AGENTS.md`，提交前和推送前均执行 `git fetch origin main`，没有新增远端提交；所有推送均使用普通 fast-forward。四台系统均已完成正式构建和持久化。推送前通过新的 Mac SSH 连接确认 h610 的 `/run/current-system` 与持久 system profile 同为上述 `lmbbl02…` 系统，headscale、sshd、tailscaled 均 active，保护 timer 为 inactive。四台切换日志位于 `.work/logs/production-{encore,tank,seraph,h610}-switch.log`。

| main 提交 | 内容 | 验证 |
| --- | --- | --- |
| `807abe6abea0757701566e15a62e2cbc50a4ca58` | 固定 Vex 输入，提交已部署的 encore、tank、seraph、h610 配置 | 四台系统构建通过；从 GitHub 精确提交引用在 encore 独立求值得到相同四个系统路径。CI run `39061297641` 的 encore job `117479702889` 获取私有 Vex GitHub API tarball 时返回 HTTP 404 |
| `bc5ad8e4424c6aa18efca8fd491a1b07bc5ec98c` | 使用仓库既有的 Git SSH 私有输入方式 | 实际 `nix flake update vex` 成功；提交、NAR 摘要和四台系统 derivation 保持一致；CI run `39061705046` 的完整 flake 检查及四台受影响主机的构建预检、部署 schema 检查全部通过，确认现有 SSH 密钥可以读取私有 Vex 输入 |

最终 Vex 输入为 `git+ssh://git@github.com/imdomestic/vex?ref=feat/vex-p1`，锁定 `b9c48aad76ab4b5ca42ae9fb42a76cf372db9afa`，NAR 摘要为 `sha256-7qc8AUoDVlQNW0+A/IxtmR5dD3ikDNDoS/9WqHgT6Kg=`。没有读取 CI secret，也没有新增密钥或扩大权限。

最终远端 `main` 已通过 `git ls-remote` 确认为 `bc5ad8e4424c6aa18efca8fd491a1b07bc5ec98c`，本地 checkout 干净。直接使用 `github:imdomestic/nix-config/bc5ad8e4424c6aa18efca8fd491a1b07bc5ec98c#nixosConfigurations` 从远端提交独立求值，退出码 0，四台系统路径均与正式部署一致。此步骤没有使用本地路径输入替代 Vex。

使用真实 `nix eval --raw …#nixosConfigurations.<host>.config.system.build.toplevel.drvPath`，与各主机 `nix-store -q --deriver /run/current-system` 比较，结果如下。SSH 输入变更不需要再次切换系统。

| 主机 | 已部署系统与最终配置共同的 derivation |
| --- | --- |
| encore | `/nix/store/ymj2ya7925k1bill5rhmx0r8l43a5xqd-nixos-system-encore-26.05.20260911.21a67dc.drv` |
| tank | `/nix/store/cndvsw0ngxbfhk9l4dsb8v3asv5ylb1n-nixos-system-tank-26.05.20260911.21a67dc.drv` |
| seraph | `/nix/store/m3jcvpjl7bnz439zxs78v7d7xw5gip43-nixos-system-seraph-26.05.20260911.21a67dc.drv` |
| h610 | `/nix/store/32crydhmd5n6xzq7ymqxqaglrzn436zs-nixos-system-h610-26.05.20260911.21a67dc.drv` |

新增配置的 Alejandra 检查和提交前 `git diff --check` 均通过；原文件已有的格式差异保持原状。独立权限与来源审阅记录位于 `.work/nix-final-review.md`。求值使用项目 `.work/temp`，Linux IFD 使用 `/nix/var/nix/builds`，没有构建全部主机闭包。

CI 成功记录：`nix flake check` job `117480837471`、encore `117480911035`、tank `117480911042`、seraph `117480911047`、h610 `117480911022`。这五项均为 completed/success。记录时该 workflow 其余主机任务仍在运行或排队，没有失败项；本节的通过结论覆盖上述五项检查。

## 收尾删除前核对

本节保存 encore 清理前的只读清单。收尾脚本的 check 模式通过后，已执行下述精确删除与普通垃圾回收。系统 profile 的当前链接和 `/run/current-system` 均为第 40 代，保留第 40 代与上一代第 39 代。

| 系统代数 | 创建时间（encore 本地时间） | Nix store 目标 | 收尾动作 |
| --- | --- | --- | --- |
| 36 | 2026-10-17 17:01:32 | `/nix/store/n3q8mwlcsspj1d2ccxk0lhlyrs7x3knl-nixos-system-encore-26.05.20260911.21a67dc` | 已删除该 profile 代数 |
| 37 | 2026-10-17 23:11:23 | `/nix/store/3gsazgjmisf6v0ixxbabhinwm0rshjvb-nixos-system-encore-26.05.20260911.21a67dc` | 已删除该 profile 代数 |
| 38 | 2026-10-18 00:51:17 | `/nix/store/bwk6bm2kr2bfdv5bb3sczjhbm6c68bg8-nixos-system-encore-26.05.20260911.21a67dc` | 已删除该 profile 代数 |
| 39 | 2026-10-18 01:40:40 | `/nix/store/9jy1v1kbx6c3x0gcr219w5jfw6c4xah0-nixos-system-encore-26.05.20260911.21a67dc` | 保留，上一代 |
| 40 | 2026-10-18 04:04:52 | `/nix/store/kdyrv2jm7pmcd3pqz8ky5f4i1sy7p2jp-nixos-system-encore-26.05.20260911.21a67dc` | 保留，当前代 |

应用 profile 全部保留，仅列出清单：

| 应用 profile | 代数 | 创建时间（encore 本地时间） | Nix store 目标 |
| --- | --- | --- | --- |
| chorus | 4，当前 | 2026-10-16 03:57:54 | `/nix/store/d2pkb26zrjdraw6h64wdwzyvrz1z4x4x-chorus-applications` |
| vex | 1 | 2026-10-18 00:38:40 | `/nix/store/yfa1apib3kw88v7gn5m37ijsn9ij77ch-vex-applications` |
| vex | 2 | 2026-10-18 00:50:54 | `/nix/store/q19rji8kv5i4b5xsdwngyqval1w2c50y-vex-applications` |
| vex | 3 | 2026-10-18 00:57:05 | `/nix/store/7y6jpbwx1j5l4m4n262xp22z3x8215zb-vex-applications` |
| vex | 4 | 2026-10-18 03:18:19 | `/nix/store/fncmacj8lmff8xsjjsv2nbmkaylb36kp-vex-applications` |
| vex | 5 | 2026-10-18 04:33:00 | `/nix/store/cpxixy3gdh16119k2jna2v7kgq4q5kak-vex-applications` |
| vex | 6，当前 | 2026-10-18 04:41:53 | `/nix/store/4l9zkw0drsxsv40y0jlaspanpkdv1090-vex-applications` |

原 8791 验证服务的两个 namespace 为：

| Namespace | 实际 UID | 删除前状态 |
| --- | --- | --- |
| `vex-p-vex-it-stagea` | `3acb2828-6c38-46f3-81ea-4b50530b109f` | Active |
| `vex-p-vex-it-stageb` | `54757f4c-3853-4525-a087-eb0b4688d575` | Active |

通过实时 API discovery 枚举全部 35 种可列出的 namespaced 资源，再逐种读取名称和计数。两个 namespace 的 Pod、Job、StatefulSet、PVC 均为零；完整计数如下：

| API 资源类型 | stagea | stageb |
| --- | ---: | ---: |
| addons.k3s.cattle.io | 0 | 0 |
| configmaps | 1 | 1 |
| controllerrevisions.apps | 0 | 0 |
| cronjobs.batch | 0 | 0 |
| csistoragecapacities.storage.k8s.io | 0 | 0 |
| daemonsets.apps | 0 | 0 |
| deployments.apps | 0 | 0 |
| endpoints | 0 | 0 |
| endpointslices.discovery.k8s.io | 0 | 0 |
| events | 0 | 0 |
| events.events.k8s.io | 0 | 0 |
| helmchartconfigs.helm.cattle.io | 0 | 0 |
| helmcharts.helm.cattle.io | 0 | 0 |
| horizontalpodautoscalers.autoscaling | 0 | 0 |
| ingresses.networking.k8s.io | 0 | 0 |
| jobs.batch | 0 | 0 |
| leases.coordination.k8s.io | 0 | 0 |
| limitranges | 1 | 1 |
| networkpolicies.networking.k8s.io | 1 | 1 |
| persistentvolumeclaims | 0 | 0 |
| poddisruptionbudgets.policy | 0 | 0 |
| pods | 0 | 0 |
| pods.metrics.k8s.io | 0 | 0 |
| podtemplates | 0 | 0 |
| replicasets.apps | 0 | 0 |
| replicationcontrollers | 0 | 0 |
| resourceclaims.resource.k8s.io | 0 | 0 |
| resourceclaimtemplates.resource.k8s.io | 0 | 0 |
| resourcequotas | 1 | 1 |
| rolebindings.rbac.authorization.k8s.io | 0 | 0 |
| roles.rbac.authorization.k8s.io | 0 | 0 |
| secrets | 0 | 0 |
| serviceaccounts | 1 | 1 |
| services | 0 | 0 |
| statefulsets.apps | 0 | 0 |

每个 namespace 的五个基础对象分别为 ConfigMap `kube-root-ca.crt`、LimitRange `vex-project`、NetworkPolicy `vex-project`、ResourceQuota `vex-project`、ServiceAccount `default`。`vex-login-it.service` 实测为 LoadState=not-found、ActiveState=inactive、SubState=dead、MainPID=0；8791 没有 TCP 监听。只读调查证据为 `.work/logs/final-cleanup-inventory.log`。

脚本 `.work/vex-p0/final-cleanup.sh` 的 SHA256 为 `03e0d2d16eaa787e1a5ffcab51d8bf01c573d55a1db3ef8fdb1612ea02ae62fe`，本机与 encore 副本摘要一致，已通过 `bash -n`。脚本要求显式传入两个 UID 和当前系统完整路径，核对当前/上一代及所有应用 profile 链接；Vex 当前链接必须为第 6 代，且第 1–6 代目标路径均须与本节清单一致。脚本确认临时服务停止，再完整检查 namespace 资源。删除请求使用 Kubernetes `DeleteOptions.preconditions.uid`，随后等待实际 namespace 删除完成。任何前置变化或等待超时都会立即终止后续操作；等待超时需要再次查询实际状态。

保留资源使用以下完整 UID 进行检查。三个 namespace 必须为 Active 且没有 deletionTimestamp；两个 main PVC 必须为 Bound、没有 deletionTimestamp，且 `volumeName` 分别等于 `pvc-<完整 PVC UID>`。这些检查在初始前置、每个测试 namespace 删除前后、system 旧代数删除后和 Nix GC 完成后执行。

| 保留资源 | 完整 UID | 必须保持的状态 |
| --- | --- | --- |
| namespace `chorus-rooms` | `50154a39-bef0-4351-a4c1-0c4eb0cee989` | Active |
| namespace `vex-p-chorus` | `33d39031-50d4-403a-820e-07af5afdc37c` | Active |
| namespace `vex-p-example` | `14f44441-446a-49d8-b0c6-0a7be94d16c9` | Active |
| `chorus-rooms/data-room-main-fabric-0` PVC | `998937db-c633-4431-bdd2-68331e8127be` | Bound |
| `vex-p-chorus/data-room-main-fabric-0` PVC | `c583ce8c-07f8-4aa1-b054-8db34357eb6d` | Bound |

只有两个 namespace 删除完成、保留资源再次核验成功后，脚本才执行 `nix-env -p /nix/var/nix/profiles/system --delete-generations 36 37 38`，核对仅剩第 39、40 代，再执行普通 `nix-store --gc`。脚本不删除任何应用 profile 代数，保留世界数据和 UID 为 `50154a39-bef0-4351-a4c1-0c4eb0cee989` 的 `chorus-rooms`；没有连接 tank 或操作归档文件的命令。

上传及只读检查命令如下，已实际执行且 check 退出码为 0；日志保存于 `.work/logs/final-cleanup-check-profile6-pvc.log`。本次检查没有发送删除请求，也没有执行 Nix GC。

```sh
scp -p .work/vex-p0/final-cleanup.sh encore:/home/linwhite/vex-work/.work/final-cleanup.sh
ssh -o BatchMode=yes encore \
  'sudo -n bash /home/linwhite/vex-work/.work/final-cleanup.sh check 3acb2828-6c38-46f3-81ea-4b50530b109f 54757f4c-3853-4525-a087-eb0b4688d575 /nix/store/kdyrv2jm7pmcd3pqz8ky5f4i1sy7p2jp-nixos-system-encore-26.05.20260911.21a67dc'
```

服务端集成与正式房间核验完成后，已执行以下命令：

```sh
ssh -o BatchMode=yes encore \
  'sudo -n bash /home/linwhite/vex-work/.work/final-cleanup.sh execute 3acb2828-6c38-46f3-81ea-4b50530b109f 54757f4c-3853-4525-a087-eb0b4688d575 /nix/store/kdyrv2jm7pmcd3pqz8ky5f4i1sy7p2jp-nixos-system-encore-26.05.20260911.21a67dc'
```

执行完成，两个指定测试 namespace 已删除。旧、新 main PVC 均保持原 UID 和 Bound，正式两个项目 namespace 保持原 UID；全部应用 profile 保留。普通 GC 删除5393个 store path，报告释放1.4 GiB；根分区可用空间由60,935,819,264增加为63,526,789,120字节。执行日志为 `.work/logs/final-cleanup-execution.log`。随后新 SSH 检查确认 vexd、vex-dns、k3s、mysql、postgresql、tailscaled 全部 active，两节点 Ready，全集群没有 Minecraft Pod；两个分支房间为 stopped，临时房间为 closed。
