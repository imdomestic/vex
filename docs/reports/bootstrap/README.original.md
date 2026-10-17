# Bootstrap 资料

`nix/bootstrap` 已由 `imdomestic/nix-config` 的 encore 配置取代，保留为首次环境搭建的历史资料。生产系统更新使用 nix-config；应用更新使用 chorus-testbed 根目录的 Nix flake。操作步骤见 [部署说明](../../../deploy/README.md)。

nix-config 管理 k3s、metrics-server、`chorus-dns`、防火墙、旧 Minecraft 单元的 masked 状态与 GC roots、chorusd 用户、目录规则、polkit、构建模板、服务单元和 JDK。应用 profile `/nix/var/nix/profiles/chorus` 提供 chorusd、chorusctl 和 chorus-world-scanner。

生产数据位于 `/srv/chorus`，配置、kubeconfig 和 secrets 位于 `/var/lib/chorusd`。原 Minecraft 文件、归档及 GC roots 保留。系统切换执行 600 秒自动恢复保护，通过全新 SSH 核对服务之后持久化。

首次搭建的检查记录保留在 encore `/home/linwhite/chorus-testbed-p0/.work/bootstrap-retired`；后续部署证据位于 `/home/linwhite/chorus-testbed/.work/activation`。实际运行系统与持久系统路径分别通过 `readlink -f /run/current-system` 和 `readlink -f /nix/var/nix/profiles/system` 查询。
