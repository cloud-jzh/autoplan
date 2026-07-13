# P06 验证、恢复与切换运行手册

本手册只适用于 P06 的脱敏 fixture、系统临时目录或调用方显式授权的独立副本。它不授权读取、修复或迁移真实 Electron profile、生产数据库、真实附件根或 P06 以外领域。

## 1. 前置条件

在执行 P06 门禁前，必须同时满足以下条件：

- P05 `migration:p05:verify` 已真实通过，并存在最新的不可覆盖、哈希关联的 `completed` / `ok: true` evidence。
- P06 intake contract、Node golden、manifest、稳定错误表、schema/checksum 和 OpenAPI 均保持冻结哈希。
- 数据库是调用方显式授权的脱敏副本；Go writer 持有有效的 P05 `DatabaseOwnerProof`，并以 `AuthorizedCopy` 打开。
- 附件根位于本次受控临时范围；Files policy 在上传、打开、rename、下载、quarantine 和 unlink 前均能重新验证根与 realpath。
- Node/sql.js 已完全关闭同一副本。不得通过启动旧 Node writer、IPC 写入口或热回退来“恢复”。

统一执行：

```text
npm.cmd run migration:p06:verify
```

前置不满足时保留 `blocked` evidence；不要手改 summary、伪造退出码、更新 golden 或继续运行后续命令。

## 2. 上传与删除状态机

上传采用以下可恢复顺序：

1. `staged`：在附件根的 `staged/` 内独占创建、流式限额、哈希、文件同步和目录同步。
2. 数据库事务写入附件元数据和 operation；提交失败时只清理 staged bytes，清理失败则持久化失败 operation。
3. `ready`：同根原子 promote 到 `ready/`，再校验 size/hash/MIME。无法确认时保持 `failed`，不伪造成功。

删除采用以下可恢复顺序：

1. 数据库事务创建 `deleting` operation 并删除可见元数据。
2. 将 `ready/` bytes 移入同卷 `quarantine/`，状态转为 `quarantined`。
3. 删除 quarantine bytes 后进入 `complete`；权限、断连、文件不存在或崩溃造成的不确定状态转为可报告的 `failed` 或待恢复状态。

`ready`、`complete`、`failed` 的含义不同：`ready` 表示上传 bytes 已校验可读，`complete` 表示删除工作流完成，`failed` 表示必须由恢复/审计确认，不能作为开放 readiness 的依据。

## 3. 崩溃与恢复顺序

应用启动或受控运行时必须先调用附件 `Recover`；未完成恢复时 readiness 保持关闭，禁止新上传、下载成功响应或静默忽略 operation。

| 发现状态 | 受控恢复动作 | 允许的终态 |
| --- | --- | --- |
| `staged` 上传 | promote 后重验 size/hash/MIME | `ready`，或 `failed` |
| `ready` 上传 | 重验持久 bytes | `ready`，或 `failed` |
| `deleting` | 移入 quarantine，再删除 | `complete`，或 `failed` |
| `quarantined` | 仅删除对应 quarantine bytes | `complete`，或 `failed` |
| `failed` | 依据持久 operation、限定 key 和审计结果重放 | `ready` / `complete`，或继续报告 |

恢复不得扫描或删除任意路径。只允许 operation 引用的、通过 storage key 与 Files policy 重新验证的 `staged/`、`ready/`、`quarantine/` 对象。

## 4. 审计、repair 与孤儿处置

先执行只读审计，记录 DB 缺文件、存储孤儿、过期 staged/quarantine、size/hash/MIME 不符、unsafe key、owner 缺失和跨项目 owner。审计报告只记录 attachment ID、operation ID、错误码和相对占位位置。

仅在以下条件同时满足时才可请求显式 repair：

- runtime 已提供 owner-locked 的授权副本；`autoplan-audit` 不会自行发现数据库或附件根。
- operator 已阅读只读报告，且 repair 目标是无 operation 引用、超过保留期的 `staged/` 或 `quarantine/` 对象。
- repair 前后分别生成哈希与动作摘要；`ready/` orphan、DB 缺文件、hash/MIME 不符、unsafe key 与 owner 不一致只报告，不自动删除。

repair 必须可重复执行：第二次执行不得新增删除动作。任何无法安全归因的项目保持报告状态，交由独立数据处置流程处理。

## 5. 回滚与范围边界

- 数据库事务未提交：不 promote 最终 bytes；保留或清理 staged bytes，并在无法清理时写入 recovery anchor。
- 数据库已提交但文件未完成：不回滚已提交 Intake/附件业务结果，不启动 Node writer；保留 operation，执行恢复或显式审计。
- transport 失败、会话失效、Origin/Host 拒绝、跨项目访问或 owner 不存在：在应用服务读取、文件打开和 bytes 返回之前拒绝。
- 不通过热回退重启 Node/sql.js writer，不重写 Intake UI，不扩展到 Projects、Config、loop、scripts、executors 或其他后续领域。

## 6. 证据收集与审阅

每次 `migration:p06:verify` 创建一个新的 `docs/migration/p06/evidence/runs/<UTC-run-id>/`。审阅顺序：

1. 确认 P05 gate 是首条命令；若失败，后续 P06 writer 不得出现。
2. 重新计算 evidence manifest 的文件大小与 SHA-256。
3. 检查 Node golden 结束时间早于所有 Go writer；命令区间不得重叠。
4. 检查 CRUD、状态、关联、权限、幂等、并发、故障、恢复、审计/repair、MCP、renderer、OpenAPI 与 golden 矩阵。
5. 检查环境与证据均不包含真实路径、附件内容、数据库内容、`stored_path`、local file URL、凭据或 token。

失败、blocked 和 cleanup 失败的 evidence 必须保留。只能以新的 run 重试，不能覆盖、编辑或删除既有 run。
