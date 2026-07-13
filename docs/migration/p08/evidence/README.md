# P08 脱敏验证证据

`npm.cmd run migration:p08:verify` 每次运行创建新的目录：

```text
docs/migration/p08/evidence/runs/<UTC-run-id>/
```

目录不可覆盖。仓库不预置成功 evidence；`blocked`、`failed`、cleanup 失败、
P07 gate 失败和 P00 红灯签名偏移都必须如实保留。

## 文件结构

- `summary.json`：状态、P07 gate/evidence、P00 签名、环境安全声明、source
  hash 起止值、命令真实退出码、writer 时间线、覆盖矩阵、fixture/schema/
  golden 哈希、git status 摘要、临时 HOME/Go cache 隔离、cleanup 和剩余风险。
- `NN-<command>.stdout.log` / `stderr.log`：实际进程输出的脱敏副本。summary
  记录字节数与 SHA-256。
- `evidence-manifest.json`：列出该 run 的 artifact、字节数和 SHA-256，并
  声明 run 不可变。

`summary.status = "completed"` 不等于通过。只有 `summary.ok = true` 才表示
P07/P00 前置、P08 专项命令、时间线、source hash、临时根清理和敏感扫描均
通过。

## 必须审阅的结论

1. `p07-gate` 是第一条、`p08-safety-preflight` 是第二条，随后必须是 P00
   `check`、`test`。任一前置失败时不能出现 `node-static-golden` 或任何
   `go-static-*` 命令。
2. `node-static-golden` 在全部 Go writer 前结束，
   `simultaneousNodeGoWriter=false` 且 `nodeClosedBeforeGo=true`。
3. manifest、Node golden、schema、分页、稳定错误、owner guard、secret
   provider 隔离与静态 runtime closure 哈希可复算。
4. 覆盖矩阵包含 golden、项目归属、分页/关联、事务/并发、Secrets、迁移
   恢复、HTTP/MCP/UI、运行能力非成功响应和敏感输出。
5. P00 `check` 只能按冻结签名接受既有失败；`test` 及所有 P08 专项命令必须
   为零退出码。

## 脱敏规则

允许保存：相对路径、受控占位符、稳定错误码、scenario/capability ID、命令名、
时间、退出码、信号、计数、字节数和 SHA-256。

禁止保存：

- SQLite bytes、数据库行、SQL 参数、请求正文、消息正文或 tool data；
- API key、token、cookie、Authorization、session、环境变量值、secret ref、
  provider locator 或 key-store locator；
- Electron `userData`、home/workspace、生产数据库、备份/secret-store 实际
  路径或未授权绝对路径；
- `stored_path`、`file://`、`autoplan-file://` 或可操作本地 URL。

脱敏不会改变原始退出码，只在 evidence 落盘前替换敏感片段。不要手工修改
evidence 来掩盖失败或扫描命中。
