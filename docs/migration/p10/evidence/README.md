# P10 证据目录

`runs/` 是 P10 唯一有效的运行证据位置。每次验证都创建新的不可覆盖目录；已有目录、符号链接、缺少 `summary.json` 或哈希不匹配的目录都不构成完成证据。

运行目录包含：

- `summary.json`：gate 结论、fixture 授权状态、命令顺序、真实退出码、开始/结束时间、source hash、清理结果和剩余风险；
- `evidence-manifest.json`：除 manifest 自身外的每个文件的字节数和 SHA-256，且 `immutable_run_directory=true`；
- 每条已执行命令单独的 stdout/stderr 脱敏日志，即使 gate 或后续命令失败也保留真实结果。

证据采集拒绝 secret、token、cookie、session、Authorization、真实 userData、生产数据库路径、未授权绝对路径、file URL、符号链接和超过 8 MiB 的单个证据文件。失败或 blocked 运行绝不补写成功结论，也不会执行尚未获授权的后续步骤。

根目录 [manifest.json](manifest.json) 仅冻结证据格式和风险，不能替代一次实际的 `runs/<run-id>` 成功记录。
