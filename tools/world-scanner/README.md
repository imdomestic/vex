# 世界扫描

`scan.py` 使用 `anvil-parser2` 读取区域与区块，使用 `nbtlib` 读取独立 NBT 文件。
扫描覆盖方块调色板和物品 ID，兼容 Minecraft 26.3 的字符串调色板。

```sh
vex-world-scanner --world /path/to/world --minecraft 26.3 --work-dir /path/to/project/.work
vex-world-scanner --archive /path/to/world.tar.gz --minecraft 26.3 --work-dir /path/to/project/.work
```

输出为单个 JSON 对象：`minecraft`、`namespaces`、`regions`、`chunks`、`nbtFiles`。
命名空间按名称排序，省略 `minecraft`。服务端在发布世界时调用该命令。
归档扫描只在明确指定的工作目录解包，拒绝链接、目录穿越及敏感资源路径。
