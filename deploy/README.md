生产系统由 `imdomestic/nix-config` 管理；应用由 chorus-testbed 构建并安装到 `/nix/var/nix/profiles/chorus`。encore 的应用源码目录为 `/home/linwhite/chorus-testbed`，同步远端为 `/home/linwhite/git/chorus-testbed.git`。临时资料使用项目 `.work/`，Nix builder 使用 `/nix/var/nix/builds`。

## 应用更新

在 encore 同步已通过测试的源码，执行：

```sh
bash deploy/build-application.sh
bash deploy/install-application.sh deploy "$(cat .work/application/candidate)"
```

构建产物包含 chorusd、chorusctl 和 chorus-world-scanner。构建执行 Go 格式检查与全部普通测试；脚本检查磁盘至少剩余 15 GiB，取得全局构建锁，更新 Nix profile，重启 chorusd，并验证真实鉴权健康接口。Nix profile 保留历史 generation。初次系统迁移使用 `prepare` 参数预先安装 profile。

## 系统更新

nix-config 管理 k3s、metrics-server、`chorus-dns`、防火墙、旧 Minecraft 单元屏蔽、GC roots、用户与目录规则、polkit、构建模板、chorusd 服务单元和 JDK。chorusd 与构建模板的 ExecStart 固定指向应用 profile。secrets 继续通过 `/var/lib/chorusd/secrets/` 中的文件提供。

在 encore 使用准备部署的 nix-config checkout 构建，并审阅 dry-activate、nix-diff 与闭包差异：

```sh
bash deploy/build-system.sh /home/linwhite/chorus-testbed/.work/nix-config-round2
```

然后在 Mac 使用已审阅的系统路径执行：

```sh
bash deploy/activate-from-mac.sh /home/linwhite/chorus-testbed/.work/nix-config-round2 /nix/store/<已审阅的系统路径>
```

脚本重新构建并确认路径一致；核对 k3s、数据库、SSH、Tailscale、防火墙服务内容及十二个旧 Minecraft 单元；取得构建锁；设置 600 秒自动恢复任务后执行 `test`。全新的 SSH 连接检查 k3s、mysql、postgresql、chorusd、chorus-dns、真实 API、DNS 和旧服务 masked/inactive 状态。全部通过后取消 timer，再持久化并执行 `switch`。检查失败时保留恢复 timer。

系统配置验证后提交并推送 nix-config；使用刚推送远端的全新克隆执行 `nixos-rebuild build --flake .#encore`，核对独立复现结果。应用仓库与旧系统内容均不作为 nix-config 的私有输入。

## 构建与集成检查

`deploy/build-controller.sh` 执行 Go 格式检查、测试与二进制构建；`deploy/test-kube.sh` 在 `chorus-it-*` 隔离 namespace 检查真实 Kubernetes 组件；其他集成测试使用 `integration` build tag。禁止替换依赖的测试实现。生产房间验收使用真实 Minecraft、RCON、mc-router SLP 和签名后的 Ruri Chorus CLI。

Fabric 构建通过 `chorus-build@<buildId>.service` 执行 `build-worker`，systemd 限制整个构建进程组，`/srv/chorus-build.lock` 保证同一时刻只有一个 Gradle 构建。生产数据为 `/srv/chorus`，配置为 `/var/lib/chorusd/config.toml`，kubeconfig 为 `/var/lib/chorusd/kubeconfig`。

`nix/bootstrap` 与 `prepare-production.sh` 保留首次环境搭建资料。生产更新使用上述系统、应用两层流程。
