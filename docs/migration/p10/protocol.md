# P10 Operation、事件与恢复协议

本文件冻结 P10 的 Operation、持久事件和恢复行为。它优先于旧的兼容性 audit event 与未迁移 runtime 约定；旧记录可以继续被读取，但不能作为 P10 API、SSE 或 outbox 的新写入格式。

## 通用约束

- 所有外部字段使用 `snake_case`。时间必须是 UTC RFC 3339（`Z` 后缀）；空值使用 JSON `null`，不能以空字符串代替。
- `request_id` 保持现有 DTO 的格式：`[A-Za-z0-9][A-Za-z0-9._:-]{0,63}`。它是关联标识，不是授权凭据。
- `operation_id` 与幂等键是受限 opaque identifier。`project_id` 始终为正整数，P10 Operation 不支持全局或无项目归属。
- `request_digest` 是规范化请求意图的 lowercase SHA-256。规范化必须稳定地排序对象键、以 LF 统一换行，并排除秘密、环境、路径、会话及任何不可安全回显的输入。原请求绝不写入 Operation、事件、响应、日志或 fixture。
- 所有更新都带 `version`。变更以 `WHERE version = expected_version`（或同等的事务前置条件）提交；成功更新使 version 增加一。重放 no-op 不增加 version、不分配 revision、不写 outbox。

## Operation 契约

Operation 的固定字段由 `backend/openapi/schemas/operation.schema.json` 定义：项目归属、类型、状态、`request_id`、幂等键、`request_digest`、version、取消请求时间、三类生命周期时间、受限 result 和稳定的错误摘要。`idempotency_key` 可为 null 以读取旧 DTO；所有会产生 P10 副作用的新命令必须提供非空键。

错误摘要只包含稳定的大写码和已脱敏的简短 `summary`。推荐使用 `IDEMPOTENCY_CONFLICT`、`OPERATION_VERSION_CONFLICT`、`INVALID_OPERATION_STATE`、`RECOVERY_INTERRUPTED`、`RECOVERY_EXPIRED`、`OUTPUT_TRUNCATED`、`OUTPUT_REDACTION_FAILED` 与 `RESYNC_REQUIRED`。错误码不会携带请求正文、命令、路径、环境变量、session 或 secret。

### 状态转换

| 当前状态 | 允许的新状态 | 幂等重放 | 禁止 |
| --- | --- | --- | --- |
| `queued` | `running`、`cancelled`、`interrupted` | 再次排队为 no-op | `succeeded`、`failed` |
| `running` | `succeeded`、`failed`、`cancelled`、`interrupted` | 再次开始为 no-op | 回到 `queued` |
| `succeeded` | 无 | 相同完成为 no-op | 任何不同状态 |
| `failed` | 无 | 相同失败完成为 no-op | 任何不同状态 |
| `cancelled` | 无 | 重复取消为 no-op | 任何不同状态 |
| `interrupted` | 无 | 重复中断为 no-op | 任何不同状态 |

开始命令只会把 `queued` 变为 `running`；对 `running` 的重复开始返回当前 Operation，不能取得第二个执行所有权。完成命令只接受 `running` 到其指定终态；同一终态的重放返回当前 Operation，不重复发布事件。取消 `queued` 时在同一提交中直接变为 `cancelled`；取消 `running` 时仅写入 `cancel_requested_at` 并触发执行上下文取消，确认取消才转换为 `cancelled`。

完成与取消竞争由同一事务的 version 前置条件裁决。首先成功提交终态的一方是唯一终态；另一方重读后仅返回该终态或得到 `OPERATION_VERSION_CONFLICT`，绝不覆盖、回退或生成第二个终态事件。取消请求已记录但完成先提交时，Operation 可以以 `succeeded` 或 `failed` 结束，并保留取消请求时间作为审计事实。

## 持久事件信封

`backend/openapi/schemas/event.schema.json` 的 `EventEnvelopeV1` 是 outbox、重放与 SSE 的唯一 P10 信封。所有字段始终存在；不适用的关联字段为 null。

- `event_id` 是全局严格递增的十进制字符串游标。客户端按整数比较，不能转为 JavaScript number。仅已提交的持久事件拥有该值。
- `project_revision` 是每个项目严格连续递增的正整数。每个持久业务或 Operation 变更在同一事务中取得一个 revision 和一个 outbox 事件；回滚不可见，重放不重新分配。
- `event_class=business` 用于 `project.snapshot`、`project.patch` 或 `business.*` 的持久业务事件。
- `event_class=operation` 用于六个 `operation.<status>` 事件，必须关联 `operation_id`。
- `event_class=control` 只能是 `heartbeat` 或 `resync_required`；二者的 `event_id` 与 `project_revision` 均为 null，因此不会占用持久游标。`heartbeat` 的 `operation_id` 和 `request_id` 也必须为 null。
- `payload` 只允许有界、已脱敏的对象。它不含原始 stdout/stderr、命令、命令环境、session、token、秘密、真实 userData 或未授权路径。`resync_required.payload.reason` 固定为 `last_event_id_invalid`、`last_event_id_future`、`history_expired`、`revision_gap`、`project_mismatch` 或 `slow_consumer` 之一。

Dispatcher 按 `event_id` 递增发布，并允许客户端用该值去重。若 Last-Event-ID 已清理、属于未来、无法证明项目归属，或发现 revision 缺口，服务端只发送 `resync_required`，不猜测或合成 patch。

## 启动恢复

启动扫描在事务中执行，且不自动恢复任何执行副作用：

1. 所有遗留 `running` Operation 一律转为 `interrupted`，使用 `RECOVERY_INTERRUPTED`，并原子追加对应 Operation 事件。
2. `queued` Operation 默认保持 queued，绝不由扫描器、dispatcher 或 transport 自动执行。
3. 只有已注册的业务恢复器可认领 queued Operation。认领必须在同一事务中核对项目归属、操作类型、原始 `request_digest`、当前 `queued` 状态与 expected version，再将其唯一地转为 `running`。
4. 恢复器缺失、类型或摘要不匹配、跨项目、版本漂移或策略到期时不得执行。到期（或恢复期限缺失/非法）时转为 `interrupted`，错误码为 `RECOVERY_EXPIRED`。

该策略保证重启后的执行者具有明确所有权，且不会重新启动未知或重复的副作用。

## 输出与脱敏

stdout/stderr 只在 runner 的短暂处理路径中读取。单次原始读取最多 16 KiB；先以 UTF-8 边界分块，再清洗，随后才允许计数或有限保存。任何无效 UTF-8、无法判定安全性或脱敏失败均丢弃该块，并以 `OUTPUT_REDACTION_FAILED` 记录稳定诊断。

- 清洗后每个流最多保留 64 KiB 和 1,024 行；超过任一上限时停止保留该流后续内容，并在内部元数据设置对应 `*_truncated=true` 与 `OUTPUT_TRUNCATED`。截断不得切断 UTF-8 rune。
- 删除整行而非部分掩盖的内容包括密钥、token、Authorization/Cookie、`KEY=value`/`export` 环境赋值、命令环境、真实 userData、绝对或未授权路径，以及终端会话数据。允许的占位符只有固定文本（例如 `<redacted>`、`<path>`），不得保留可逆值。
- Operation API 只暴露稳定错误码、脱敏摘要和安全 result；项目/Operation SSE、HTTP 响应、访问日志、fixture、diff 与证据不能包含终端原始输出或内部输出元数据。持久化绝不无界增长。

这些限制同时适用于正常、重试、并发写入、取消与恢复路径；无法安全归并时选择丢弃内容，而不是扩大保存或输出范围。
