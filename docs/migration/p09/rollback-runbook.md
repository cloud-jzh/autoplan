# P09 回滚与恢复 Runbook

回滚始终从维护状态开始：UI mutation、新任务和所有 writer 保持关闭。不得将失败视为可安全重试，也不得以重启旧 Node/sql.js writer 作为恢复手段。

## 硬边界

### 首笔正式 Go 写入前

允许的恢复路径是：停止 Go → 复核演练副本仍受控 → 释放 Go owner → 由 immutable manifest 恢复到**新的、尚不存在的独立副本**。恢复 API 需要 `independent_copy` 模式；目标已有文件、符号链接、真实 userData、活动 sidecar 或未映射附件/Plan artifact 都必须返回阻断错误。

恢复完成后复核：

1. SQLite hash、schema/version、行数、关系与 snapshot。
2. `.bak`、`.mirror` 与每个声明 artifact 的 manifest hash。
3. 附件目录与 Plan 文件内容。
4. 原演练源、manifest 和 backup 没有被修改。

### 首笔正式 Go 写入后

默认路径仅为 Go 二进制回退或前向修复。禁止自动恢复旧备份、自动数据截断、环境变量确认和 Node writer 回退。

只有人工明确接受数据截断时，才可调用 `truncating_replace`：请求必须同时包含稳定的截断点、`confirmed=true` 和受影响 mutation 的稳定标识列表。恢复结果只记录截断点、数量及标识摘要哈希；不记录内容、路径或秘密。若 manifest 含附件/Plan artifact 而请求不能完整、显式地恢复它们，工具必须阻断并保持 maintenance，改走前向修复。

## 故障处置

冻结失败、进程拒停/超时、Node persist 失败、备份中断/哈希错误、preflight/审计/migration 写点中断、锁冲突、readiness/readyz 失败、读写 smoke 失败、UI 打开失败或进程树清理失败时：

1. 维持 maintenance，禁止新任务与 UI mutation。
2. 保守停止或隔离所有 writer；不创建第二个 Go owner。
3. 保留副本、immutable backup 和脱敏 evidence，不覆盖原始 backup 或故障现场。
4. 记录稳定失败码、operation ID、阶段、退出码和哈希；不记录 token、env_vars、绝对路径或正文。

恢复或前向修复成功后也不得自动打开 UI；必须重新执行 schema/关系/snapshot/附件/Plan 核验与读写 smoke，并由后续授权流程决定是否恢复正式写入。
