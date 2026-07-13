# P15 证据与运行手册

本目录只保存 P15 的脱敏证据契约、采集结果索引和人工执行手册。它不授权删除旧模块、不切换真实 userData，也不发布安装包。

当前状态：`blocked`。已知阻塞包括旧业务 IPC 仍注册、唯一 writer 的运行时证据缺失、Electron+Go E2E 未执行、三平台安装/签名/恢复证据缺失，以及发布工作流写入环境受限。任何工具不得将这些缺失解释为通过。

## 使用边界

- 仅可使用 `fixtures/migration/p15` 中标记为 synthetic/authorized 的 fixture，或人工明确创建的临时副本。
- 采集器拒绝真实 Electron userData、活动 SQLite/WAL/锁文件、符号链接、绝对路径、凭据、会话材料、原始命令输出和无界输入。
- `collect-evidence.js` 只归档外部已执行命令的脱敏摘要；它不会启动 Electron、Go、数据库、CLI、MCP、终端、安装器或发布流程。
- `validate-evidence.js` 仅验证记录、哈希、平台覆盖和门禁状态。缺少证据以退出码 `2` 表示 `blocked`，不是通过。

## 目录约定

| 位置 | 用途 |
| --- | --- |
| `evidence/manifest.json` | 当前证据索引与完成定义状态 |
| `evidence/runs/<run-id>/` | 由采集器写入的不可变脱敏摘要与文件清单 |
| `evidence-matrix.md` | 第 15、19 节逐项映射 |
| `release-runbook.md` | 三平台构建、签名、验收与人工发布前检查 |
| `degrade-runbook.md` | 发现风险后的停止和降级边界 |
| `recovery-runbook.md` | 备份、兼容 Go 版本和恢复演练边界 |

只有 `validate-evidence.js` 返回完成，且 P005/P006 门禁、平台证据和人工发布批准均已独立满足时，后续任务才可改变迁移状态。
