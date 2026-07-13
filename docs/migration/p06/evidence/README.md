# P06 脱敏验证证据

`npm.cmd run migration:p06:verify` 为每次执行创建新的：

```text
docs/migration/p06/evidence/runs/<UTC-run-id>/
```

run 目录不可覆盖。仓库不预置成功证据；任何 `blocked`、`failed`、cleanup 失败或 P00 冻结红灯比较结果均应如实保留。

## 文件结构

- `summary.json`：状态、P05 gate 与 evidence 摘要、环境安全声明、冻结输入哈希、writer 时间线、实际命令和退出码、测试矩阵、OpenAPI 覆盖、剩余风险、git status 摘要及 cleanup 结果。
- `NN-<command>.stdout.log` / `stderr.log`：实际进程输出的脱敏副本。summary 记录其字节数与 SHA-256。
- `evidence-manifest.json`：除自身外所有已生成 artifact 的路径、字节数和 SHA-256，并声明 run 不可变。

`summary.status = "completed"` 不等于通过；只有 `summary.ok = true` 才代表 P05 前置、专项命令、P00 基线、时间线、哈希、脱敏和临时根清理全部满足。

## 必须审阅的结论

1. `p05-gate` 是首条命令。P05 失败时 status 必须为 `blocked`，且不能出现 Node/Go writer。
2. P06 source hash 起止一致；fixture、contract、稳定错误表、OpenAPI、owner guard 和附件存储 guard 的 SHA-256 可复算。
3. `node-golden-generator` 在所有 Go writer 前结束，writer 时间线无重叠，`simultaneousNodeGoWriter=false`。
4. 矩阵覆盖 CRUD、状态、关联、跨项目权限、幂等、并发、附件输入/下载、故障、恢复、审计/repair、MCP、renderer、OpenAPI 与 Node/Go golden。
5. P00 `check` 和 `test` 只按冻结签名接受既有红灯；新增、消失、重命名或未分类失败均为不通过。

## 脱敏规则

允许保存：仓库相对路径、受控占位符、稳定错误码、scenario ID、时间、退出码、信号、计数、字节数和 SHA-256。

禁止保存：

- SQLite bytes、数据库行、SQL 参数、附件 bytes、请求正文或用户内容；
- API key、token、cookie、Authorization、session 凭据或环境变量值；
- Electron `userData`、home/workspace、生产数据库、真实附件根或未授权绝对路径；
- `stored_path`、hash 原始值、`file://`、`autoplan-file://` 或可操作的本地 URL。

命中可复用凭据、真实本地路径、生产数据库或 local file URL 的输出会使该命令不被接受。脱敏不会覆盖原始退出码；它只在落盘前替换敏感片段。不要手工修改 evidence 来“修复”扫描结果。
