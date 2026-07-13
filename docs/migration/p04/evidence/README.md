# P04 evidence 说明

`npm.cmd run migration:p04:verify` 在 `runs/<UTC-run-id>/` 创建一次性、不可覆盖的证据目录。仓库不预置或手工补写成功 run；每次重新验证都必须使用新的 run ID。

每个 run 包含：

- `summary.json`：阶段状态、blocked 原因、前序 evidence 摘要、命令、起止时间、原始退出码、结构化迁移结果、覆盖矩阵、git 状态、清理结果和剩余风险；
- `fixture-matrix.json`：每个脱敏夹具的 from/to version、预期结果/错误码、migration checksum、源/结果 SHA-256、迁前/迁后逐表行数、分类差异、第二次 no-op，以及备份/恢复应匹配的源 SHA-256；
- `NN-<command>.stdout.log` / `stderr.log`：实际进程输出的脱敏副本及其字节数、SHA-256；
- `evidence-manifest.json`：生成 manifest 前存在的 summary、fixture matrix 和日志哈希，并声明 run 目录不可变。

manifest 不递归包含自身。run 创建后不得编辑日志、删除失败记录、替换 summary 或把 blocked 改写成 completed。

## 审阅顺序

1. 校验 `evidence-manifest.json` 中所有 artifact 的大小和 SHA-256。
2. 检查 `summary.status`、`ok`、P02/P03 evidence digest、`sourceHashesStable` 和 `sourceFilesComplete`。
3. 检查每条命令的 expected outcome、实际退出码、结构化 code、failure signature 和 evaluation。预期故障必须是匹配稳定 code 的真实非零。
4. 检查 fixture matrix 的 migration checksum、ledger/no-op、逐表 delta 分类和源哈希稳定性。
5. 检查 `restoreCoverage` 中每个成功夹具的 backup/restored SHA-256 均等于源 SHA-256，且真实 restore 测试已接受。
6. 检查 Owner 锁、readiness、旧 Node 拒绝、完整 Go gate、P00 check/test 基线比较和临时根清理。

## 状态规则

- `blocked`：P02/P03 实际门禁或最新完成 evidence 校验失败。P04 fixture、数据库副本和高风险命令不会启动。
- `failed`：门禁通过，但夹具生成、matrix 或安全前置失败。
- `completed` 且 `ok: true`：所有专项成功、预期故障精确匹配、成功夹具 restore 覆盖完整、源码哈希稳定、临时根清理成功，且 check/test 没有新增红灯。
- `completed` 且 `ok: false`：至少一项真实结果不满足；失败信息和原始退出码保留。

P04 专项必须零退出。只有最终项目级 `check`/`test` 使用 P00 的精确冻结规则；新失败、签名漂移、P04 相关失败或未经规则接受的“旧失败消失”都不能通过。任何 skip/only/force 控制都会令验证失败。

## 脱敏与保留

证据允许表/列名、计数、稳定错误码、脱敏记录标识、版本、checksum、SHA-256、仓库相对路径和授权根占位符。禁止数据库字节或行内容、SQL 参数、聊天/日志正文、API key、认证值、token、`env_vars` 值、环境变量值、可复用会话、真实 home/workspace/userData 路径及未授权绝对路径。

验证环境会剔除名称疑似凭据、会话或数据库路径的变量，只记录剔除数量，不记录名称和值。命令输出先进行凭据形态扫描；发现可用凭据形态会失败，然后才脱敏落盘。

验证器无论成功、失败或 blocked，只删除位于系统临时目录且名称匹配 `autoplan-p04-verify-*` 的本次拥有根。它不清理 evidence。进程被外部强制终止时，只能在核实相同所有权边界后清理临时根；失败 evidence 应按审计保留策略整体保存，不能局部删除。
