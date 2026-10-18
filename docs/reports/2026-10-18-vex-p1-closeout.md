# Vex P1 收尾验收

执行依据：2026-10-18 P2 执行指令第 0 节。证据保存在本机 `.work/vex-p2/`；临时文件不提交。

| 项目 | 当前状态 | 证据 |
| --- | --- | --- |
| 生产用户权限 | linwhite 为 admin，hank 为非 admin | encore 以 root 执行 `p2-preflight.py`，仅读取 users 的 admin/identities 字段 |
| 生产服务 | vexd、vex-dns、k3s、mysql、postgresql、tailscaled 均 active；两节点 Ready | 新 SSH 检查 `.work/vex-p2/closeout-health.log` |
| Ruri 实际进房与 hop | chorus main→plaza→main、example main 与旧提交均通过；joined、SLP online=1 与 example v2/v1 已核验 | 独立验收目录 `.work/vex-real-it`，使用已验证的 P1 CLI |
| GUI 登录与多服务器 | loopback 邀请、分组和会话失效已验证；正式域名的 GUI 验收随 HTTPS 与 Ruri 模块改造继续进行 | `docs/ruri/reports/2026-10-18-ruri-vex-p1-game.md`；2026-10-18 用户明确允许服务端 P1/P2 独立推进 |
| 旧 namespace/PVC 与目录 | 六项前置条件通过，旧 namespace/PVC、旧世界目录及 /srv/chorus 已删除；新 main PVC 保持 Bound | `.work/vex-p2/legacy-namespace-delete.log`、`legacy-directory-delete.log`、`closeout-health.log` |
| P0 剩余归档 | 完整核验通过，原目录已按授权删除 | tank `legacy/vex-p0-20261018/remaining-via-mac.tar.gz`；`.work/vex-p2/p0-remaining-delete.log` |
| taint 调度 | Go build/vet/test 及真实 Kubernetes 验收通过 | `.work/vex-p2/taint-kubernetes.log`；原 Node UID 与完整 taints 已恢复 |
| dev 与部署版本 | Go、Nix 与完整源码扫描已通过，正在合并 P1 并锁定部署版本 | 系统预览与运行系统路径完全相同，dry-activate 无变更 |

当前 encore 系统为 `/nix/store/kdyrv2jm7pmcd3pqz8ky5f4i1sy7p2jp-nixos-system-encore-26.05.20260911.21a67dc`，应用 profile 为 `/nix/store/4l9zkw0drsxsv40y0jlaspanpkdv1090-vex-applications`。

nix-config 当前为远端 `0f3696b`。自 `bc5ad8e` 的新增提交涉及 Minecraft router、静态大厅、OpenCode 与 pi Home Manager 配置。使用 P1 源码预览 encore，系统输出路径与运行系统完全相同，闭包比较为空，dry-activate 没有激活动作，受保护服务的 InvocationID 保持一致。证据为 `.work/vex-p2/p1-system-preview.log`、`p1-system-closure-diff.log`、`p1-system-dry-activate.log`。

## 归档与旧资源清理

P0 剩余归档位于 tank `/srv/archive/vex/legacy/vex-p0-20261018/remaining-via-mac.tar.gz`，大小为 6,125,914,831 字节，SHA256 为 `1025930a2c11d6452f6f35d154d602a701084bbf4883baffb1a66feb62a2ccd5`。2026-10-18，`gzip -t` 完整校验通过；原始清单、归档逐成员解码清单、源目录再次完整读取清单完全一致：61,449 项、45,407 个文件、9,499,787,539 逻辑字节。比较覆盖文件 SHA256、大小、类型、权限、数字 UID/GID 与链接目标，`verify-remaining.py` 退出 0。

encore 源目录 `/home/linwhite/chorus-testbed-p0` 的 device/inode 为 `66309:2495511`，实际占用 9,650,450,432 字节。再次读取目录身份、检查全部进程 fd/cwd 与 `findmnt` 后，未发现进程引用或子挂载。按照执行指令第 0.3 项，执行以下目录删除；tank 全部归档文件保留：

```sh
sudo -n rm -rf --one-file-system --preserve-root=all -- /home/linwhite/chorus-testbed-p0
```

实际执行入口为 `.work/vex-p2/remove-retired-directory.py`，仅接受这次授权的两个固定路径，删除前再次验证传入的 device/inode、实际目录类型、符号链接、进程引用与挂载，删除后检查目标缺席并记录 `df -B1 /`。脚本通过 SSH 标准输入执行，使用 Python `-B`，不产生远端缓存。

P0 目录删除已完成，执行退出 0。根分区可用空间由 63,186,419,712 增加至 72,170,487,808 字节；`/home/linwhite/chorus-testbed-p0` 已不存在。完整归档及 tank 同一目录中的其他文件全部保留。日志为 `.work/vex-p2/p0-remaining-delete.log`。

客户端正常退出后，通过现有 `.work/vex-p0/production-api.py` 建立短期独立会话，已分别调用 chorus `main-fabric` generation12、`w-plaza-779645c4-fabric` generation1，以及 example `main-fabric` generation3、`c932eab71-fabric` generation3 的正常 `POST …/stop`。四个房间均已完成世界保存，分支房间为 stopped，临时房间为 closed。四个新快照均有 `tankVerifiedAt`；再次完整读取本地与 tank 文件后，大小与 SHA256 全部一致。验收会话已注销，本地临时会话文件已移除。

旧资源删除的六项前置条件已核验通过。正式 main g10 使用 g9 的启动前七项文件 SHA256 证据仍保存于 `.work/vex-p0/production-room-main.json`；本轮客户端完成 main→plaza→main、example main 与旧提交的真实进入。迁移完成记录、原数据库副本以及新旧快照均保存在删除范围之外。运行中的房间、runtime 和配置没有旧 namespace 或旧数据目录依赖；迁移审计记录保留原始来源路径。旧控制器 MainPID 为 0，旧构建单元缺席，旧目录无进程 fd/cwd 引用或子挂载。

| 新快照 | 字节数 | SHA256 |
| --- | ---: | --- |
| chorus/main-fabric-g12 | 11,001,888 | e8f7dea2e3e295255ae0ca8819b15542919d6c5b828ea191aac5e9882661a488 |
| chorus/w-plaza-779645c4-fabric-g1 | 881,710 | f2eff8bbc5de0586a3e6e537826d1839a90a998dd89cbabd20a863f4d2a9f741 |
| example/main-fabric-g3 | 5,881,479 | 1977a0847055266eaa3b5338489b92b788323abd14d8aa096c2a30b433817088 |
| example/c932eab71-fabric-g3 | 5,231,715 | e9f69a9597ffb7152f0c693702c8acd069739f2873cfd125518e2e34f85a1e0a |

证据为 `.work/vex-p2/closeout-archives.json`。原 g9 在两端的大小仍为 10,959,437 字节，完整 SHA256 均为 `cfd650e0fc26633ebaaf797d5bc877d7a16887fc5eadce59b8acf87d48fb1dcb`。准确删除范围为 UID `50154a39-bef0-4351-a4c1-0c4eb0cee989` 的 `chorus-rooms` namespace、其中原有资源及旧 main PVC，以及 `/srv/chorus`。新 main PVC `c583ce8c-07f8-4aa1-b054-8db34357eb6d` 必须持续 Bound。

### 精确删除清单与命令

通过 API discovery 枚举 35 种可列出的 namespaced 资源，得到以下 23 项。完整 UID JSON 清单为 `.work/vex-p2/legacy-namespace-inventory.json`；删除脚本再次获取同一清单，逐项比较资源类型、名称和完整 UID。Pod 与 Job 数量均为零，StatefulSet replicas 为 0。

| 资源 | 名称 | UID |
| --- | --- | --- |
| ConfigMap | kube-root-ca.crt | c83c86dd-9411-4c60-8ca6-49c3278d278f |
| Endpoints | room-main-fabric | c0364091-d60e-4bec-9ed4-06f6c75e8fbe |
| PVC | data-room-main-fabric-0 | 998937db-c633-4431-bdd2-68331e8127be |
| Secret | chorusd-api | 096e6b58-7cb9-458a-ac21-093585ee0459 |
| Secret | room-main-fabric-credentials | 30ff7380-aeaa-4b72-a13a-8a4cbb2184f9 |
| ServiceAccount | chorusd | 0bd34ab4-b0fc-4122-9a36-3871fdfc04dc |
| ServiceAccount | default | 665a55ba-e99f-401c-9919-9c40693d2c12 |
| Service | room-main-fabric | c5b9893e-2d03-4bdc-95e9-9b884076b2f1 |
| ControllerRevision | room-main-fabric-54847fd758 | 6423d5e5-69ec-4527-b7a8-2cace081d3a4 |
| ControllerRevision | room-main-fabric-54f6dbd768 | 119b7b11-2399-49e8-a3c3-cdca1f7d690e |
| ControllerRevision | room-main-fabric-5b4b6c84ff | 732447ea-ec08-4921-b981-d1d455511856 |
| ControllerRevision | room-main-fabric-66bd797fc6 | 5747b2c8-07b7-42c6-aa05-8233da069a94 |
| ControllerRevision | room-main-fabric-6755fb7884 | 7470de41-3679-44ae-ae40-48f3ed4dfc17 |
| ControllerRevision | room-main-fabric-68fd4ccf9f | 21d1614b-e288-4ebf-b50d-f8fa9b5333c8 |
| ControllerRevision | room-main-fabric-74bd79954b | 228ddb7e-b9ad-484a-a5a0-78712a08d61e |
| ControllerRevision | room-main-fabric-789d6cd886 | 1ff78008-6824-4bb4-9a84-39b09e66df5a |
| ControllerRevision | room-main-fabric-7b9b6d774c | 9aeb8eea-3099-41c5-9ce8-7dca62dd812c |
| StatefulSet | room-main-fabric | a0096299-10f4-47d8-a08a-2ad0fd382841 |
| EndpointSlice | room-main-fabric-lwcwd | f907b520-433a-4289-9cc7-716044da473d |
| RoleBinding | chorusd | bb350802-8551-4240-9790-e49e99cad4d8 |
| RoleBinding | mc-router | 3651716c-4665-4ee7-bae9-e84ce6a82242 |
| Role | chorusd | 166c3b16-2a8e-4e45-827f-ad5f7f2c010e |
| Role | mc-router | af2fac4d-e268-4cbf-805b-2122faddbd04 |

namespace 删除使用以下 API 请求，由 `retire-legacy-namespace.py --execute` 调用 `k3s kubectl delete --raw /api/v1/namespaces/chorus-rooms -f -`，标准输入为：

```json
{"apiVersion":"v1","kind":"DeleteOptions","preconditions":{"uid":"50154a39-bef0-4351-a4c1-0c4eb0cee989"}}
```

旧 PV 名称为 `pvc-998937db-c633-4431-bdd2-68331e8127be`，UID 为 `65760b89-8153-44ad-bdfb-13f6783c32f9`，回收策略为 Delete；namespace 删除已触发旧 PVC 与其目录的正常回收。旧 PVC 宿主目录删除前实际占用 230,240,256 字节。脚本等待 namespace 与该 PV 均不存在，再检查新 main PVC 的原 UID 与 Bound 状态，全部通过。

`/srv/chorus` 的 device/inode 为 `66309:2127952`，实际占用 32,092,160 字节；其子目录为 git 14,331,904、.work 4,096、work 45,056、worktrees 4,096、state 143,360、deps 2,625,536、builds 14,884,864、mods 49,152 字节。旧 namespace 完成删除后，使用同一目录保护脚本执行：

```sh
sudo -n rm -rf --one-file-system --preserve-root=all -- /srv/chorus
```

上述删除已完成，两个执行脚本均退出 0。2026-10-18 09:20:05 UTC，全新 Mac→encore SSH 检查确认：旧 namespace、旧 PV、旧 PVC 宿主目录、`/srv/chorus` 和 P0 源目录均不存在；新 main PVC 保持 UID `c583ce8c-07f8-4aa1-b054-8db34357eb6d` 与 Bound。encore/seraph 均为 Ready，版本均为 `v1.35.7+k3s1`；vexd、vex-dns、k3s、mysql、postgresql、tailscaled 均 active。迁移数据库副本、完成记录与 g9 快照保持可读，g9 两端完整摘要继续一致。

旧 namespace 与 `/srv/chorus` 删除后的根分区可用空间为 72,432,640,000 字节；随后健康检查记录 72,432,594,944 字节。证据为 `.work/vex-p2/legacy-namespace-delete.log`、`legacy-directory-delete.log`、`closeout-health.log`、`closeout-tank-retained.log`。全部 tank 文件保持原状。

### 构建与部署门槛

P1 taint 修改通过完整 Go build、vet、test；真实 seraph NoSchedule 测试耗时 1.2 秒，实际 Capacity 与房间节点选择在添加 taint 后排除该节点，移除后恢复，Node UID 与完整 taints 均恢复。独立只读审阅未发现实质问题。

完整 Nix 应用构建退出 0，候选为 `/nix/store/v59rg1ww9nsw5x4jcahan9j21agpzf9f-vex-applications`，日志为根工作区 `.work/vex-p2/p1-nix-build.log`。完整源码扫描覆盖历史、文件对象、提交信息、压缩包成员及六种编码的可读字符串，没有发现实际凭据。用户于 2026-10-18 明确允许 vex P1 合入 dev 与 P2 服务端独立推进；Ruri 的 HTTPS GUI 验收按新模块指令执行。P1 合并后将 nix-config 输入切换到 dev 并锁定同一提交，应用 profile 使用对应源码构建。
