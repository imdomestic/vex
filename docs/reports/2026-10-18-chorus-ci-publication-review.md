# Chorus CI 发布核验

## 本地配置

- 独立工作目录：`.work/vex-p2/chorus`，分支 `feat/vex-ci-preview`，已正常快进至真实 main 提交 `f63f618e599b0fc05737e76dcf754939bb1cdbc5`。
- CI 提交 `d1315b62f733eb53a32dc2429aa9fd833fbbf247` 仅添加 `vex.toml` 和 `.github/workflows/vex.yml`，固定 Go Action 提交 `8ff4dbed05ef06d4341ed51c6b53c99f88bcb158`。模组代码保持原样。
- 完整发布扫描已经通过，CI 分支已提交 [PR #1](https://github.com/imdomestic/chorus/pull/1)，由维护者审阅。[实际 Actions run 39124898574](https://github.com/imdomestic/chorus/actions/runs/39124898574) 已成功完成 Fabric 构建、Go Action 打包和 artifact 上传。生产 chorus 保持 `build.source="host"`，需要维护者合并 CI PR 后再切换。
- 真实 artifact 为 `vex-build`，ID `12136345143`，大小 5,913,609 字节，SHA256 `e384b889d02092d286bcd5c6a47ab24dd611c99bab48eadb8330f5dc3df6fa08`。
- 单独配置 patch 为 Downloads 审阅目录中的 `CHORUS-CI-CONFIG.patch`，只包含上述两个新增文件。

## 历史归档完整性

【实测】全历史读取范围包含 373 个提交、6,389 个 blob、3,204 个工作目录文件，原始对象共 537,591,822 字节。证据目录：`.work/vex-p2/chorus/.work/vex-source-audit-20261018T111930Z`。

【实测】既有对象 `7af96986bb1770062aa2551c2c23f85bcface9f7` 对应 `data/compendium/assets/2026-10-10/sheets/1662574278.html.gz`。Python gzip 的完整性检查返回 CRC 不符；GNU gzip 1.14 的 `gzip -lv` 同时返回 `invalid compressed data--crc error` 和 `invalid compressed data--length error`，退出码 1。

【代码】已读取 GNU gzip 1.14 官方源代码，下载包 SHA256 为 `01a7b881bd220bfdf615f97b8718f80bdfd3f6add385b993dcf6efd14e8c0ac6`。`unzip.c:140` 至 `:147` 要求解码成功后才继续读取 footer；`:192` 至 `:200` 比较 CRC 与长度。`inflate.c:972` 至 `:980` 处理至 DEFLATE 最后一个区块，`:991` 写出全部剩余输出后返回成功。`util.c:206` 至 `:220` 检查实际写入错误。源码保留在 `.work/vex-p2/gzip-1.14/`。

【实测】另使用 zlib 1.2.12 的成熟 `inflate` / `inflateValidate` API 独立核对解码覆盖，保留原始 footer 错误：返回 `Z_STREAM_END`，消费 150,294 字节，与原文件大小相同；剩余输入为零，输出 757,172 字节。GNU gzip 的实际完整输出与其逐字一致，SHA256 为 `9b71f89773f54a2427e649829f4085e734bfca72f5022ebaefd2375c06e427d8`。因此当前成员输出和全部输入均已覆盖，没有未处理的拼接成员。没有编写 gzip 或 DEFLATE parser，既有 Git 对象与历史保持原字节。

【实测】完整补充扫描覆盖原始对象、126 个完整解压成员，以及 9,719 个来源各六种编码的可读字符串；随后检查远端 main 新增四个提交和最终 CI 提交。合计覆盖 378 个提交、6,432 个 blob，实际凭据为零、未分类候选为零。原始 gzip 的 CRC/长度错误仍然存在，完整解压输出已经纳入扫描。

证据：`.work/vex-p2/gzip-coverage.json`、`gzip-gnu-output.stderr`、`chorus/.work/complete-audit-20261018T114653Z/review.json`、`chorus/.work/final-delta-audit-20261018T114955Z/review.json`。

## token 候选分类

【实测】历史、blob 和工作目录扫描各报告一次同源候选，来源为既有对象 `296941cbfd40017440bcb4b36d34ad81d7a95592`、路径 `data/d2-research/2026-10-11/control-bungie-9-7-0.html.gz`。首次相关历史提交为 `b8a8714ddd0ab4a1daf3c0fbfae4353c039ab30e`。

【实测】该 gzip 通过实际解压。Python 标准库 HTMLParser 和 JSON 解析器确认候选位于 `script` 的 `data-cf-beacon` 属性，配置字段为 `spa`、`token`、`version`；脚本来源为 `static.cloudflareinsights.com` 的 `beacon.min.js`。token 为 32 个十六进制字符，检查没有输出其内容。

【分类】该字段属于公开页面交付给浏览器的 Cloudflare Web Analytics site token。Cloudflare 官方 FAQ 明确展示同一 `data-cf-beacon` 形式及其网页嵌入用途；该页面已完整读取。本次候选按公开网页分析配置分类，未将其视为 Vex、Microsoft、GitHub 或基础设施登录凭据，也没有调用该 token 验证其当前运行状态。[Cloudflare Web Analytics FAQ](https://developers.cloudflare.com/web-analytics/faq/)

分类脚本：`.work/vex-p2/review-chorus-token.py`。脚本还逐字核对扫描器报告的实际列位置。历史、原始对象、归档成员及两种字符串编码中的六次同源发现均已完成分类。
