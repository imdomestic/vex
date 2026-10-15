# Chorus 仓库迁移记录

迁移完成日期：2026-10-16（JST）。

生产 chorusd 使用公开仓库 `imdomestic/chorus`，通过匿名 HTTPS 读取。访问检查禁用了 credential helper、askpass、额外授权请求头和交互输入。

| 配置项目 | 实际值 |
| --- | --- |
| `git.remote` | `https://github.com/imdomestic/chorus.git` |
| `git.default_branch` | `main` |
| 已核对 head | `f4555f8f03001b53d2104ad0eff1f41a71e000eb` |
| 生产镜像 | `/srv/chorus/git/chorus.git` |

新旧仓库均有 `main` 分支，head 一致；336 个提交的 SHA 和父提交关系全部一致。生产数据库的 6 个房间、5 个构建、16 个快照所引用的来源提交全部存在于新镜像，缺失数量为 0。快照通过其 `buildId` 核对来源提交。

全新镜像在生产数据所在文件系统内以 chorusd 属主建立。确认构建进程全部退出后，停止 chorusd，通过 GNU `mv --exchange --no-copy` 原子交换镜像，保留旧镜像，再安装配置并启动服务。分支登记返回 `main`，房间与构建关联保持正确。

head 对应的真实构建 `fabric-f4555f8f0300` 为 ready，实际构建身份与文件摘要匹配。CLI 真实进入 `main-fabric` generation 9，服务端记录 `linWhite joined the game`；经 `10.1.2.137:25565` 的 Minecraft SLP 返回 `online=1`，房间、generation、build 身份全部匹配。

切换前后所有房间的种子字段保持一致，main 世界 PVC `998937db-c633-4431-bdd2-68331e8127be` 保持 Bound。正常停止后生成 `main-fabric-g9` 快照，10,959,437 bytes，已通过 tank 摘要与大小校验。main 最终保持 stopped。

旧镜像备份在验收完成后按授权删除。核对结果和采集证据保存在被 Git 忽略的 `.work/round3/`。

本项目自身的主线仍为 `dev`，功能开发使用 `feat/<名字>` 分支。
