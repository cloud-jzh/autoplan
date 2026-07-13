# P06 附件恢复与审计操作说明

本文描述附件 bytes 与 SQLite 事务无法跨资源原子提交时的受控恢复边界。所有示例均是逻辑操作，不包含真实文件路径、数据库路径或附件内容。

## 操作记录

每个上传或删除意图都有一个持久化 operation，至少关联项目、附件 ID（若已分配）、受控 storage key、大小、SHA-256、MIME、状态与更新时间。公开接口、事件、日志和 evidence 仅可输出安全摘要：attachment ID、operation ID、状态、错误码及相对类别（`staged`、`ready`、`quarantine`）。

| 操作 | 初始状态 | 中间状态 | 成功状态 | 不确定状态 |
| --- | --- | --- | --- | --- |
| upload | `staged` | promote / revalidate | `ready` | `failed` |
| delete | `deleting` | `quarantined` | `complete` | `failed` |

读取或下载只接受已验证的 `ready/` key。`staged/`、`quarantine/`、非法 key、缺失 bytes、hash 不符和未完成 recovery 均不得返回任何附件 bytes。

## 故障点与预期处置

| 故障点 | 安全结果 | 后续动作 |
| --- | --- | --- |
| stream 读取、大小限制、NUL/危险名称、MIME/内容嗅探失败 | 无可见元数据；staged bytes 被清理 | 拒绝请求，保留稳定错误码 |
| 临时创建、短写、fsync、目录同步、磁盘满或权限失败 | 无最终 key；无成功响应 | 记录失败，不进入 ready |
| metadata 事务或 commit 失败 | 不 promote；尽力清理 staged | 清理失败时保留失败 operation 供恢复/审计 |
| rename/promote 后进程崩溃 | operation 与 bytes 可能暂不一致 | Recover 依据 operation 重放并校验 hash/size |
| delete 已提交、quarantine 未完成 | 元数据不可见，bytes 可能仍在 ready/quarantine | Recover 先 quarantine，再删除；不可确认时 failed |
| unlink 被拒绝、外部删除、取消或断连 | 不声称 delete 完成 | 保留可报告 operation，直到恢复结论 |

任何未知 key、跨根 key、reparse/symlink/junction 替换、外部哨兵变化或 owner/project 不一致都必须安全失败，不得据此扩大搜索或删除范围。

## 审计与 repair 白名单

只读审计按稳定顺序报告：

- `database_file_missing`
- `storage_orphan`
- `staged_expired` / `quarantine_expired`
- `size_mismatch`、`hash_mismatch`、`mime_mismatch`
- `unsafe_storage_key`
- `owner_missing` / `owner_project_mismatch`
- `operation_pending`

repair 的白名单仅包括：未被 operation 引用、超过保留期、且仍位于受控 `staged/` 或 `quarantine/` 的对象。repair 不处理 `ready/` orphan，也不自行修复 DB 行、hash、MIME、owner 或跨项目问题。repair 输出前后哈希、计数和错误码；不输出路径、附件内容或 SQL 参数。

## 运行约束

`backend/cmd/autoplan-audit` 在没有 owner-locked runtime 时故意返回 `owner_locked_runtime_required`。这不是需要绕过的错误；应先由 bootstrap 提供 P05 授权副本、owner proof 和受控附件根，再执行只读审计或显式 repair。

恢复与 repair 不启动旧 Node writer，不访问 Electron `userData`，不使用环境变量猜测数据库位置，也不以 UI 热回退替代持久化 operation。完成后应通过 P06 evidence 记录状态、哈希、命令和退出码，并保留未解决项作为剩余风险。
