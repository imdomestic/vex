`flake.nix` 固定引用 `imdomestic/nix-config` 的 `cc004e5814bb2dbef08186901b71f65c83bae064`，通过 `extendModules` 添加 encore 的 k3s server。`flake.lock` 固定全部输入。`running-system.nix` 保留运行 generation 的 `services.sunshine.capSysAdmin = true`。

flake 在添加 k3s 模块之前检查完整系统路径必须等于：

```text
/nix/store/9s5vlyqfk6l59lmbn27gd7mlyzyv78mv-nixos-system-encore-26.05.20260911.21a67dc
```

2026-10-14 09:28 UTC 的 P0 只读验收确认旧 Minecraft 服务停用配置为以下 generation：

```text
/nix/store/n4r586zyshxnl4jhpsbbf9gisn4zwf9h-nixos-system-encore-26.05.20260911.21a67dc
```

2026-10-14 的生产部署已运行 `nixosConfigurations.encore-chorus`。该配置在 P0 配置上增加 `chorusd`、受限构建模板、世界扫描工具和 DNS 服务。最新一轮部署的运行系统与持久系统 profile 均已指向：

```text
/nix/store/i9d513024yxxr8dbd8m1fi87d918nzqg-nixos-system-encore-26.05.20260911.21a67dc
```

后续源码更新会产生新的系统路径；实际路径通过 `readlink -f /run/current-system` 与 `readlink -f /nix/var/nix/profiles/system` 核对，部署证据保存在 `/home/linwhite/chorus-testbed/.work/activation/`。

| 配置 | 取值 |
| --- | --- |
| k3s | `1.35.7+k3s1`，来自固定 nixpkgs |
| containerd | `2.2.5-k3s2` |
| 节点与角色 | `encore`，server，内置 agent |
| 数据库 | 默认 SQLite |
| API 绑定地址与通告地址 | `10.1.2.137` |
| Flannel | 默认 VXLAN，接口 `wlo1` |
| 存储 | 默认 `local-path`，`WaitForFirstConsumer` |
| ServiceLB | 保持启用，encore 带 `enablelb=true` 标签 |
| Traefik | 通过 k3s 原生选项禁用 |
| NodePort 地址 | `10.1.2.137/32` |
| kubeconfig 权限 | `0600` |
| k3s 运行临时目录 | `/var/cache/chorus-k3s`，权限 `0700` |
| metrics-server 证书与临时目录 | `/var/cache/metrics-server`，emptyDir 挂载，已完成进程与文件检查 |

防火墙通过 `wlo1` 接受来自 `10.1.2.0/24` 的 TCP 6443、TCP 10250、UDP 8472，通过 `cni0` 和 `flannel.1` 接受 Pod CIDR `10.42.0.0/16` 的入站流量。API 使用 IPv4 LAN 地址。该模块添加 `br_netfilter`、`overlay` 和 `net.ipv4.ip_forward = 1`。

官方版本的 [local-storage.yaml](https://raw.githubusercontent.com/k3s-io/k3s/v1.35.7%2Bk3s1/manifests/local-storage.yaml) 声明 `local-path` 为默认 StorageClass、绑定模式为 `WaitForFirstConsumer`、回收策略为 `Delete`。[K3s 网络服务文档](https://docs.k3s.io/networking/networking-services) 说明 ServiceLB 通过节点标签选择节点，并根据 LoadBalancer Service 的端口创建使用 hostPort 的 Pod。

`metrics-server.nix` 在 k3s 启动前为七份 bundled metrics-server 清单建立 `.skip` 文件，并安装 `manifests/chorus-metrics-server.yaml`。K3s 自动部署该清单。清单保留固定版本官方文件中的九个完整资源、镜像 `rancher/mirrored-metrics-server:v0.9.0`、权限、探针、调度与资源请求；证书目录、挂载路径和 `TMPDIR` 使用 `/var/cache/metrics-server`。[官方组件管理文档](https://docs.k3s.io/installation/packaged-components) 定义了自动部署目录与 `.skip` 行为。模块断言 k3s 版本必须为 `1.35.7+k3s1`，升级时需要同步检查该版本的完整清单。

默认 `nixosConfigurations.encore` 组合 k3s、metrics-server 和 `retire-legacy-minecraft.nix`，将 `GTL`、`bedrock-proxy`、`bingo`、`lobby`、`proxy`、`speedrun` 六个服务及其六个同名 socket 设置为 masked，并清空 `wantedBy`。该配置保留 Minecraft 用户、目录规则、服务定义和数据库服务；`system.extraDependencies` 引用包含全部原 Minecraft 资源的内部系统闭包，使保留文件所引用的 store 路径继续受到 Nix GC 保护。原服务归档、完整恢复及 tank 校验通过后已经应用；切换前后原目录的 8,937 个 Minecraft 项目和四个 secret 目录项目均通过清单核验。

flake 输出分别提供 P0 基础系统和生产系统：

| flake 输出 | 系统 |
| --- | --- |
| `nixosConfigurations.encore`、`nixosConfigurations.encore-retired` | P0 基础系统 `n4r586…` |
| `packages.x86_64-linux.default`、`packages.x86_64-linux.retired` | P0 基础系统 |
| `checks.x86_64-linux.encore`、`checks.x86_64-linux.encore-retired` | P0 基础系统 |
| `nixosConfigurations.encore-chorus` | 生产系统，包含 controller、构建模板和 DNS |
| `packages.x86_64-linux.chorus-system`、`checks.x86_64-linux.encore-chorus` | 生产系统 |
| `packages.x86_64-linux.chorusd`、`checks.x86_64-linux.chorusd` | 包含 `chorusd` 与 `chorusctl` 的 Go package |

导出的 `nixosModules.encore-k3s` 包含 `k3s-server.nix` 和 `metrics-server.nix`，供其他系统复用这两个组件。完整 encore 配置另外组合旧服务停用模块和原资源闭包保留设置。内部保留六个旧服务定义的系统仅用作 `system.extraDependencies` 的依赖。常规 `--flake .#encore`、默认 package 及显式 retired 别名都保持已确定的停用状态。

该系统已通过 `nix eval --json`、`nix flake check --no-build`、系统构建、`nix-diff`、`nix store diff-closures`、生成文件比较及 `dry-activate`。与切换前系统的生成文件比较确认：十二个旧单元指向 `/dev/null`，相关 `wantedBy` 链接全部移除；其他服务的主文件仅 `k3s.service` 变化，其他 drop-in、内核、initrd、内核模块集合、系统软件包路径和 `00-nixos.conf` 全部保持一致。包含原 Minecraft 资源的闭包共 1696 个 store 路径，均保留在当前系统闭包中。

P0 基础系统的运行验收结果为：

| 检查 | 实际结果 |
| --- | --- |
| encore 节点 | `Ready=True`；`containerd://2.2.5-k3s2` |
| CoreDNS、local-path-provisioner、metrics-server | 三个 Deployment 均 rollout 完成，期望、更新和可用副本数均为 1 |
| metrics API | `Available=True`，实际节点指标请求返回 CPU 和内存用量 |
| metrics-server 参数与挂载 | `--cert-dir=/var/cache/metrics-server`，同路径的 `emptyDir` 挂载 |
| metrics-server 实际进程环境 | `TMPDIR=/var/cache/metrics-server` |
| metrics-server 实际证书文件 | `apiserver.crt` 为 2299 字节、权限 `0644`；`apiserver.key` 为 1679 字节、权限 `0600` |
| 六个旧 service 及六个同名 socket | 全部 `LoadState=masked`、`UnitFileState=masked`、`ActiveState=inactive`、`SubState=dead` |
| mysql、postgresql、k3s | 全部 `active/running` |
| TCP/UDP 25565 | 无套接字占用 |
| local-path | 默认 StorageClass，`WaitForFirstConsumer`、`Delete` |

生成的 kubeconfig 使用 `https://10.1.2.137:6443`。ServiceLB 的 encore 节点标签已生效。PVC 供应和 Minecraft 真实容器验收记录在 P0 运行报告。

P0 远端复现副本位于 `/home/linwhite/chorus-testbed-p0/.work/k3s-candidate-metrics`，验证证据位于 `/home/linwhite/chorus-testbed-p0/.work/bootstrap-retired`，包括 `preservation.json`、`current-closure-diff.txt`、`dry-activate.txt`、`flake-check.log`、`export-verification.json` 和 `actual-runtime/`。`export-verification.json` 验证默认输出与 retired 别名均指向 P0 基础系统，并单独验证可复用 k3s 模块的系统结果。构建客户端使用 `.work/temp` 作为 `TMPDIR`，Nix builder 显式使用 `/nix/var/nix/builds`，sandbox 内使用 `/build`。

以下命令在 encore 复现 P0 基础系统的检查和构建：

```sh
export TMPDIR=/home/linwhite/chorus-testbed-p0/.work/temp
bootstrap_flake=path:/home/linwhite/chorus-testbed-p0/.work/k3s-candidate-metrics
nix flake check --no-build --no-write-lock-file "$bootstrap_flake"
nix eval --no-write-lock-file --json "$bootstrap_flake#bootstrapReport"
nix build --no-write-lock-file --no-link --print-out-paths \
  --option build-dir /nix/var/nix/builds \
  "$bootstrap_flake#nixosConfigurations.encore.config.system.build.toplevel"
```

生产源码位于 `/home/linwhite/chorus-testbed`，仅通过 encore 裸仓库 `/home/linwhite/git/chorus-testbed.git` 同步。生产构建使用 `deploy/build-system.sh`：它读取固定 flake 输入，同时从完整工作目录加载 `encore-chorus` 及相邻源码，完成构建、独立 GC root 和 `dry-activate`。完整部署顺序与实际检查项见 [deploy/README.md](../../deploy/README.md)。

其他 encore 配置可直接导入 `nix/module.nix`，并提供已经固定依赖的 package：

```nix
{ pkgs, ... }:
let
  project = /home/linwhite/chorus-testbed;
in {
  imports = [ (project + "/nix/module.nix") ];
  services.chorusd = {
    enable = true;
    package = pkgs.callPackage (project + "/nix/package.nix") {
      vendorHash = pkgs.lib.removeSuffix "\n"
        (builtins.readFile (project + "/nix/vendor-hash"));
    };
    configFile = "/var/lib/chorusd/config.toml";
    dataDir = "/srv/chorus";
    jdk = pkgs.jdk25;
  };
}
```

模块要求主机名为 `encore`，创建 UID/GID 980 的 `chorusd` 用户，安装固定 `chorus-build@.service` 模板，并仅通过限定 polkit 规则允许该用户管理 `chorus-build@fabric-<12位SHA>.service`。每次构建在整个服务进程组上应用 MemoryHigh 5 GiB、MemoryMax 6 GiB、CPUQuota 200%，并使用 `/srv/chorus-build.lock` 共享锁。

生产数据和 SQLite 位于 `/srv/chorus`；配置、受限 kubeconfig、用户 token 与签名密钥位于 `/var/lib/chorusd`。真实秘密文件保留在服务器本地，Git 仅保存配置示例。`dnsmasq` 在 `100.64.0.43` 与 `10.1.2.137` 的 TCP/UDP 53 监听，将 `*.chorus.internal` 解析到 `100.64.0.43`。mc-router 的 LoadBalancer 使用 TCP 25565，并仅观察 `chorus-rooms`。
