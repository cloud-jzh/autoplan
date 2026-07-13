# P02 版本化契约

本文冻结 P02 Go sidecar 的公开传输契约。机器可读定义位于 `backend/openapi/openapi.yaml` 与 `backend/openapi/schemas/`，Go 中立 DTO 与严格解码规则位于 `backend/internal/domain/contracts/`。三者表达同一边界；后续实现不得在 handler 内另造一套 DTO。

## 当前可用性边界

- `/healthz` 只表示进程存活；`/readyz` 表示配置、单实例、迁移状态、核心 application service 与生命周期门禁均已就绪。
- `/api/v1/skeleton/rest`、`/api/v1/skeleton/sse` 和 `/api/v1/skeleton/websocket` 只是受保护的握手与依赖边界。它们当前稳定返回 `not_implemented`，不代表生产读取、业务写入、事件订阅或终端会话已经可用。
- 本阶段没有 Operation 执行器、持久化、重放、取消裁决或生产事件流。契约先于这些能力冻结，不能据此推断相关业务已经实现。

## 通用 JSON 规则

REST DTO、Operation 和事件信封使用 `snake_case`。`AppSnapshot` 的 19 个顶层字段为兼容现有 renderer 而保留 `camelCase`；这是显式兼容例外，不是新 API 的命名先例。

所有时间是带 `Z` 的 UTC RFC3339，可包含小数秒。带时区偏移、无时区、无效日期均被拒绝。严格解码还拒绝重复键、未知字段、缺少必填字段、尾随 JSON 值和超深嵌套。可选字段“缺失”和必填 nullable 字段的 `null` 不可互换。

公开对象不得携带本机 workspace 绝对路径、环境变量集合、API 密钥、认证令牌、通用 token、会话凭据、口令、Cookie 或其它原始秘密。兼容状态中确需表达敏感配置时，只允许布尔 `has_*`/`hasXxx` 或字符串 `*_masked`/`xxxMasked`；路径类兼容值只能是相对路径。该禁令递归应用于 Snapshot、错误 details、Operation result/error 以及 SSE/WS data。

## Project 与 AppSnapshot

`Project` 的必填稳定字段是 `id`、`name`、`description`、`created_at`、`updated_at`。其余字段保持 P00 renderer 类型中的可选配置形状，但有意排除本地 workspace 定位信息、环境变量和原始认证材料。Claude 配置只暴露 `has` 标记和非秘密的配置引用。

`AppSnapshot` 冻结以下完整顶层键集合：

`activeProjectId`, `activeProject`, `projects`, `mcp`, `state`, `requirements`, `feedback`, `attachments`, `plans`, `tasks`, `events`, `scans`, `scanSummary`, `scripts`, `executors`, `terminals`, `activeOperation`, `activeOperations`, `lastOperation`。

所有键必须出现。`activeProjectId`、`activeProject`、`state`、`activeOperation`、`lastOperation` 可为 `null`；集合必须是数组而不能用 `null` 代替。存在活动项目时，ID、对象和 projects 集合必须一致。尚未迁移成专用 Go DTO 的嵌套兼容对象仍受递归脱敏和路径限制，不能借 Snapshot 绕过公开边界。

## 稳定 Error

错误体固定为：

- `code`：稳定的小写 `snake_case` 机器码；
- `message`：有界、非敏感、可展示的固定消息；
- `request_id`：与响应头 `X-Request-ID` 一致的关联 ID；
- `retryable`：调用方能否在不改变请求语义的前提下重试；
- `details`：可选且递归脱敏的结构化元数据。

当前 HTTP 错误码目录为 `not_found`、`method_not_allowed`、`invalid_json`、`body_too_large`、`unsupported_media_type`、`invalid_idempotency_key`、`unauthorized`、`origin_forbidden`、`not_implemented`、`service_unavailable`、`shutting_down` 和 `internal_error`。解析器原文、内部 error、堆栈、路径或凭据不得进入 message/details。

## Operation 与幂等语义

异步提交的冻结响应是 `OperationAccepted`：`operation_id`、固定初态 `queued`、`request_id` 和 `accepted_at`。完整 `Operation` 必须显式给出 `operation_id`、`type`、`status`、`request_id`、nullable `idempotency_key`、创建/更新时间、nullable 开始/完成时间、nullable result/error。

六态约束如下：

| 状态 | `started_at` | `finished_at` | `error` |
| --- | --- | --- | --- |
| `queued` | null | null | null |
| `running` | UTC 时间 | null | null |
| `succeeded` | UTC 时间 | UTC 时间 | null |
| `failed` | UTC 时间 | UTC 时间 | Error |
| `cancelled` | 可为 null | UTC 时间 | 可为 null |
| `interrupted` | 可为 null | UTC 时间 | 可为 null |

时间必须满足 `created_at <= started_at <= finished_at <= updated_at`（不存在的时间跳过相应比较）。失败 Error 的 `request_id` 必须与 Operation 相同。`succeeded`、`failed`、`cancelled`、`interrupted` 是终态，后续实现不得从终态回迁。

未来 mutation 可接受单个 `Idempotency-Key` 请求头，字符集与长度由 OpenAPI 固定。相同作用域、相同键和等价请求必须指向同一 Operation；不同请求复用同一键应返回稳定冲突错误。未提供键不产生隐含的跨请求去重保证。P02 skeleton 只校验键的形状，不存储键、不创建 Operation，也不执行 mutation。

## SSE 与 WebSocket 信封

SSE 与 WebSocket 都使用版本 1 JSON 信封，并共同包含 `schema_version: 1`、`event_id`、`type`、`request_id`、nullable `operation_id`、UTC `occurred_at` 和脱敏 `data`。SSE 可附加 `project_id`、`sequence`；WebSocket 增加 `direction`，并可附加 `terminal_session_id`。

- 未知 `schema_version`、缺失的必填字段、未知字段、非法 ID/事件类型、非 UTC 时间或含敏感键的 data 必须在进入 application service 前被拒绝。
- SSE 的 HTTP `Last-Event-ID`、重放、心跳和 live 拼接语义尚未实现；`event_id` 在本阶段只是信封身份契约。
- WebSocket 的 `direction` 只能是 `client_to_server` 或 `server_to_client`。当前端点只验证握手，不升级连接，也不传输终端内容。
- Operation 终态事件的数据若携带 Operation，必须继续满足完整六态约束；信封不能放宽 Operation 的终态规则。

## 三种传输与共享服务

REST/JSON 负责有限请求/响应及未来 mutation 的 202 接收语义；SSE 负责未来的服务端事件订阅；WebSocket 只保留未来确需双向、低延迟交互的通道。选择传输不能改变业务授权、Project 隔离、幂等裁决或状态机结果。

三种传输必须复用同一组 application services 和 repository/runtime 端口。HTTP handler 只完成协议解析、安全门禁、DTO 校验和响应映射；不得直接写数据库、文件、进程状态或复制 MCP/UI 业务逻辑。会话、精确 Origin、Host、request_id 和脱敏日志策略同样由共享安全中间件执行。

## 版本演进

OpenAPI `/api/v1` 和事件 `schema_version: 1` 分别版本化请求边界与流式信封。v1 内只允许向后兼容演进：新增可选、非敏感字段，保持既有字段含义、类型、nullability、枚举和错误码。删除/重命名字段、改变必填性、复用枚举值、放宽秘密或路径策略都需要新 API/信封版本及明确迁移窗口。

消费者必须拒绝未知信封版本，不能按“尽量解析”降级。服务端可在部署期同时提供多个明确版本，但同一消息只能符合一个版本，且版本转换应在 application service 外的适配层完成。
