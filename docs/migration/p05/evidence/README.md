# P05 evidence 说明

`npm.cmd run migration:p05:verify` 在 `runs/<UTC-run-id>/` 创建不可覆盖的审计证据。仓库不预置成功 run；每次执行使用新目录，完成后不得编辑、补写或删除其中单个文件。

每个 run 包含：

- `summary.json`：状态、blocked 原因、P04 evidence digest、冻结源起止哈希、owner/copy 声明、串行命令结果、mutation golden 摘要、安全结论、git 状态、清理结果与剩余风险；
- `NN-<command>.stdout.log` / `stderr.log`：真实进程输出的脱敏副本；summary 记录其字节数和 SHA-256；
- `evidence-manifest.json`：生成 manifest 前所有 artifact 的路径、大小和 SHA-256，并声明 run 目录不可变。

manifest 不递归包含自身。`summary.status=completed` 不等于通过，只有 `summary.ok=true` 才表示阶段门禁全部满足。

## 审阅顺序

1. 重新计算 manifest 所列 artifact 的大小和 SHA-256。
2. 检查 P04 是否为第一条命令；若 P04 失败，确认后续没有任何 P05 writer 记录，状态为 `blocked`。
3. 校验最新 P04 summary/manifest digest、`p04OwnerGateAccepted`、`authorizedCopiesOnly`、`sourceFilesComplete` 与 `sourceHashesStable`。
4. 检查所有命令的真实起止时间、退出码、日志哈希和 evaluation；区间不得重叠。
5. 检查 writer timeline：唯一 Node writer 结束后，repository/application/http/files/golden compare 五个 Go 链路验证才开始，`simultaneousNodeGoWriter=false`。
6. 检查 mutation golden 的 scenario、版本轨迹、Node/Go 数据库 before/after SHA-256 与 handoff 顺序；SHA-256 只用于证明副本变化，不保存数据库字节或行。
7. 检查 OpenAPI/renderer/file policy 安全扫描、临时根清理、test-control 扫描，以及 P00 `check`/`test` 精确基线比较。

## 状态规则

- `blocked`：P04 真门禁或最新 P04 evidence 校验失败；P05 writer 未启动。
- `failed`：P04 已通过，但某个 P05 专项、安全或路径契约失败；验证器保留该命令的真实结果并停止剩余跨阶段步骤。
- `completed` 且 `ok: true`：P05 专项全部零退出，P00 基线精确接受，深比较和安全扫描通过，源码稳定且 owned 临时根已清理。
- `completed` 且 `ok: false`：至少一项结果不满足；失败命令、退出码和日志必须保留。

## 脱敏与保留

允许记录仓库相对路径、占位符、稳定错误码、版本、scenario ID、计数、时间、退出码和 SHA-256。禁止记录：

- SQLite 文件字节、表行内容、SQL 参数、用户输入、聊天/日志正文；
- API key、token、cookie、Authorization、会话或环境变量名称和值；
- 真实 home、workspace、Electron `userData`、产品数据库或其它未授权绝对路径；
- 能恢复凭据、数据库内容或个人数据的编码值。

验证环境只记录被剔除的敏感环境变量数量。原始输出先扫描可复用凭据形态；命中即令命令失败，再将路径和凭据替换为占位符后落盘。

验证器只删除系统临时目录下、由本次创建且名称匹配 `autoplan-p05-verify-*` 的根。异常中断后的清理由运维人员先核对同一所有权边界；不得根据日志中的占位符猜测路径，也不得递归清理共享 temp、仓库或用户目录。失败与 blocked evidence 按审计策略整体保留。
