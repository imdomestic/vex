# 首次环境搭建资料

本目录保存 2026-10-14 首次 k3s 环境搭建时的配置快照及[原部署说明](README.original.md)，供历史核查。历史 flake 的相对应用输入属于当时的目录结构，本目录不作为部署入口。

系统配置由 `imdomestic/nix-config` 管理。Vex 应用使用仓库根 flake 的 `packages.x86_64-linux.vex`，系统通过 `nixosModules.vexd` 引入 [Nix 模块](../../../nix/module.nix)。当前操作步骤见 [部署说明](../../../deploy/README.md)。

历史服务文件、数据归档和 GC roots 的保留要求仍然有效；具体状态通过当前系统配置和运行检查确定。
