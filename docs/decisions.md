# 实现决策

## 2026-10-14：Chorus 来源

用户确定目标仓库为 `imdomestic/chorus`，迁移完成前使用 `HCHogan/chorus` 的 `main` 分支作为占位来源。后续更换要求见根目录 `REPOSITORY-MIGRATION.md`。

## 2026-10-14：Fabric 产物构建任务

房间所需的 Fabric jar 使用 `:fabric:assemble` 生成。该任务执行 Minecraft 编译依赖准备、common 与 Fabric 编译、资源处理及 jar 打包。

当前上游 `:fabric:build` 同时启动 GameTest，其中包含 `makeMockServerPlayerInLevel()`。用户禁止使用 mock，因此项目不采用这些测试作为验收证据。实际服务端启动、模组加载与客户端进入仍按规划在真实运行环境中验证。

任务依赖已通过 `:fabric:assemble --dry-run` 检查，包含完整产物构建步骤。生产配置的 `gradle_task` 使用这一明确任务。

## 2026-10-14：encore 构建资源

构建使用 Nix OpenJDK 25，并设置 `JAVA_TOOL_OPTIONS` 中的 `-XX:ActiveProcessorCount=2`、Gradle `--max-workers=2` 和 Gradle 最大堆内存 `1536m`。临时验收服务对整个构建进程组设置 `MemoryHigh=5G`、`MemoryMax=6G`、`CPUQuota=200%`。

实际 NeoFormRuntime `2.0.31` 依据可见处理器数量计算 Vineflower 最大堆内存，至少为 4096 MiB；Vineflower `1.12.0` 同时依据该数量选择默认并行线程。此设置控制构建资源，所有编译步骤保持启用。资源峰值以 systemd cgroup 统计为准。

## 2026-10-14：环境搭建范围

用户已要求在 encore 搭建 k3s，并允许在构建阶段归档现有 Minecraft 服务的数据。六个旧服务在归档恢复校验通过后停用，原始文件保持保留。归档包含世界、完整配置、数据库和恢复所需的 Nix 运行依赖。

`imdomestic/nix-config` 保持只读。k3s 配置在本项目中单独提供，系统切换前核验原有配置与服务影响。

## 2026-10-14：ruri 的本机构建方式

Xcode 26.6 下，ruri 的 debug 构建、测试和产物目录查询统一显式传入 `--build-system swiftbuild`，构建目录使用 `.work/ruri-swiftbuild`。该方式负责将 String Catalog 编译为运行时需要的 `.lproj`、`.strings` 和 `.stringsdict`；本机 SwiftPM 默认 native 模式的规则仅复制 `.xcstrings`。

本机帮助标注 swiftbuild 为 preview。当前上游 SHA 已通过真实 debug 构建和 28 项测试，包括全部英文消息和复数规则检查。源码、测试断言和依赖锁定文件保持原样。测试范围遵循用户禁止 mock 的要求，详细证据见 `p0-ruri.md`。
