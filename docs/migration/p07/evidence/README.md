# P07 脱敏验证证据

`npm.cmd run migration:p07:verify` 每次执行都会创建新的 run 目录：

```text
docs/migration/p07/evidence/runs/<UTC-run-id>/
```

run 目录不可覆盖。仓库不预置成功证据；`blocked`、`failed`、cleanup 失败或 P00 冻结红灯比较结果都应如实保留。

## 文件结构

- `summary.json`：记录状态、P05/P06 gate、P05/P06 evidence 摘要、P07 source safety、环境安全声明、source hash 起止值、命令结果、writer 时间线、覆盖矩阵、OpenAPI 路由、fixture hash、git status 摘要、cleanup 和剩余风险。
- `NN-<command>.stdout.log` / `stderr.log`：实际进程输出的脱敏副本；summary 记录字节数和 SHA-256。
- `evidence-manifest.json`：列出本 run 生成 artifact 的相对路径、字节数和 SHA-256，并声明 run 不可变。

`summary.status = "completed"` 不等于通过。只有 `summary.ok = true` 才表示 P05/P06 前置、P07 专项命令、P00 基线、时间线、哈希、脱敏和临时根清理全部满足。

## 必须审阅的结论

1. `p05-gate`、`p06-gate`、`p07-safety-preflight` 是前三条命令；任一失败时后续 P07 writer 不得出现。
2. Node Plan golden command 结束时间早于所有 Go writer；`simultaneousNodeGoWriter=false`。
3. P07 fixture、稳定错误表、OpenAPI、capability catalog、owner guard、renderer transport 和 comparator SHA-256 可复算。
4. 覆盖矩阵包含状态/排序/删除/验收/redo、并发、事务、事件、HTTP/MCP、OpenAPI、renderer、disabled action 和安全证据。
5. P00 `check` 和 `test` 只按冻结签名接受既有红灯；新增、消失、重命名或未分类失败均为不通过。

## 脱敏规则

允许保存：相对路径、受控占位符、稳定错误码、scenario ID、capability ID、命令名、时间、退出码、信号、计数、字节数和 SHA-256。

禁止保存：

- SQLite bytes、数据库行、SQL 参数、请求正文、用户正文或真实计划内容；
- API key、token、cookie、Authorization、session 凭据、CLI secret 或环境变量值；
- Electron `userData`、home/workspace、生产数据库、真实附件根或未授权绝对路径；
- `stored_path`、`file://`、`autoplan-file://` 或可操作本地 URL；
- 长任务命令行、agent session ID 或未授权资源是否存在的细节。

脱敏不会改变原始退出码；只在落盘前替换敏感片段。不要手工修改 evidence 来“修复”扫描结果。
