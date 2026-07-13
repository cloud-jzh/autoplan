# P13A / P13B 冻结契约

状态：**frozen / default-off**。本文件只定义 P13 的兼容边界，不表示 Chat 或 MCP 门禁已经通过，也不授权启动新路由、provider、listener 或 writer。

机器可读权威定义是 backend/openapi/openapi.yaml、backend/openapi/schemas/chat.schema.json 与 backend/openapi/schemas/mcp.schema.json。P13 是 runtime/transport 迁移，不是 renderer、DTO、snapshot 或业务规则重写。未明确写出的行为不得推断；对应门禁保持关闭并标记 blocked。

## 通用线

- REST 成功与错误信封都包含 request_id；持久化 SSE 事件也包含 request_id。
- 公开时间一律是 UTC RFC 3339（Z 后缀），不得发送本地时区或无时区字符串。
- HTTP DTO 的 snake_case 是 wire authority；现有 renderer 依赖的 camelCase 兼容字段必须同时出现且值相等。
- null、字段省略和空字符串具有不同语义，按 schema 保留。不得用单一命名或空字符串替换旧兼容行为。
- API、SSE、MCP、错误和审计投影不得含 API key、MCP/Claude token、env_vars、Authorization/Cookie/Session、原始 command/args/cwd、真实 userData、未授权绝对路径、完整私密 tool data 或无界 provider/process 输出。
- P13 只接受由同一 application service 建立的 project/resource 归属、权限、幂等、Files policy、Operation/process runner 和审计上下文。transport 不是授权依据。

## P13A Chat

### Conversation 与消息投影

Conversation 固定为 id、project_id/projectId、title、ai_config_id/aiConfigId、pinned_at/pinnedAt、pinned、created_at/createdAt、updated_at/updatedAt。双命名字段必须同值；ai_config 与 pinned 时间的可空性不可改变。

完整 ChatMessage 固定为：

| 字段 | 兼容语义 |
| --- | --- |
| id | 稳定整数消息 ID。 |
| project_id/projectId、conversation_id/conversationId | 必须同时存在并同值，先做 project/conversation 归属校验。 |
| role | user、assistant、tool、system。 |
| content | 已脱敏、受上限约束的正文；迁移不得改变历史排序。 |
| tool_calls/toolCalls、tool_result/toolResult | 保留 JSON 字符串与 renderer 解析后的兼容投影；公开内容必须脱敏并受限。 |
| status | renderer 可见值固定为 streaming、queued、done、aborted、error、max_rounds、interrupted。streaming 是部分 assistant 消息的公共表示。 |
| created_at/createdAt | UTC RFC 3339，两个字段同值。 |

ChatMessageMetadata 仍是旧静态元数据投影：只含 id、snake_case scope/role/status/time 和 has_* 位，不含正文及 tool data。完整 history 路由返回 ChatMessage，旧 metadata 定义不得删除。

同时保留精确 legacy renderer 投影：LegacyChatMessage 的必填字段仍是 id、projectId、role、content、toolCalls、toolResult、status、createdAt；project_id、tool_calls、tool_result、created_at 继续是可省略兼容字段。LegacyChatQueue 仍只有 conversationId、items、count；LegacyChatDone 仍只有必填 status 及可省略 error、conversationId、title。P13 SSE/HTTP adapter 可在外层增加 project、turn、cursor 元数据，但不能改变这些对象的字段存在性、nullability 或状态语义。

Conversation 列表仍按既有 pinned、updated_at、id 的稳定排序；历史仍按 created_at、id 升序。删除、更新、默认会话、标题生成与 AI config 绑定保持已有业务语义。

### 发送、队列、停止与清理

发送路由接收 message，并由 Idempotency-Key（或 body 中的同名兼容键）固定一次 admission。成功返回 accepted=true、project_id、conversation_id、message_id、turn_id、operation_id；相同键和摘要复用原 admission，不同摘要冲突失败，断线或超时不自动生成第二个 turn。

每个 conversation 的 queue 是持久化 FIFO：

1. queued 条目按持久化 message ID 升序。
2. 只有队首可进入旧兼容态 processing。
3. cancel、edit、clear 只作用 queued 项，不取消或重写 processing/running turn。
4. ChatQueue 是完整替换快照，不是 delta；字段固定为 project/conversation 双命名、items、count。
5. 重启只从权威持久化队列恢复，不重新执行已经产生 provider 副作用的 turn。

stop 只请求创建该 turn 的 runtime 取消。清理 history 也不接管在途 turn。

### chat_chunk、chat_queue、chat_done SSE

Chat SSE 使用 ChatEventEnvelope：

- schema_version=1、event_id、project_id、project_revision、request_id、occurred_at、type、data 必须齐全。
- type 只允许 chat_chunk、chat_queue、chat_done。
- event_id 是 P10 durable cursor，写入 SSE id；Last-Event-ID 只重放已提交事件，绝不重跑 provider。
- chat_chunk.data.sequence 对 project_id/conversation_id/turn_id 从 1 严格递增。
- 所有 chat_chunk 先于同一 turn_id 唯一的 chat_done；done status 只能是 done、aborted、error、max_rounds、interrupted。
- chat_queue 总是完整权威 FIFO 快照；renderer 只可按 project/conversation/event_id/sequence 合并和去重。
- 历史水位、revision gap、无效 cursor 或慢消费者必须发 resync_required，随后读取 history/queue；不得通过重新发送消息恢复。

Chat REST 路径冻结为 Conversation CRUD、history/send/clear、queue read/edit/cancel/clear、stop 和 conversation SSE。每个路径使用相同 session、Origin、project/conversation scope、body/输出上限和审计检查。

### P13A 稳定错误码

| 代码 | 含义 |
| --- | --- |
| chat_disabled | Chat flag 关闭或没有通过 Chat 专属门禁。 |
| chat_prerequisite_failed | P10/P11/P12、单 writer、Files policy、secret mapper、Operation/runner 或审计前置缺失、过期或不一致。 |
| chat_runtime_unavailable | 已选择的 Chat runtime/依赖不可用；不得退到另一 runtime 重放。 |
| invalid_conversation、conversation_not_found | conversation 输入或 project 归属无效，且不得枚举其他项目。 |
| chat_queue_item_not_found、chat_turn_not_found | 同一 conversation 中不存在可操作条目或 turn。 |
| chat_turn_state_conflict | 对不允许的状态 edit/cancel/stop，或终态竞争失去。 |
| chat_idempotency_conflict | 同一幂等键对应不同请求摘要。 |
| request_in_progress、request_timeout、service_unavailable | 共享 admission/依赖不可用；不能伪装 accepted。 |

HTTP 继续使用统一错误信封、request_id 和既有 HTTP status 映射；这些代码不替代现有 invalid_*、*_not_found 或权限错误。

## P13B MCP

### Transport 与状态

P13B 迁移现有 MCP catalog，不另建工具服务：

- Streamable HTTP 默认只绑定 loopback，并验证 token/session/Origin、method/path、body 上限、超时和连接上限。
- stdio stdout 只输出 MCP 协议帧；受限、脱敏诊断只写 stderr。
- HTTP 与 stdio 注册同一不可变工具目录、同一 adapter factory 和同一 application service 实例。任一启动失败不能自动切到旧 Node MCP 或双启 listener。
- AppSnapshot.mcp 保持 camelCase McpStatus：enabled、running、status、transport、nullable host/port/path/url、hasAuthToken、authTokenMasked、authHeader、localOnly、tools、toolDocs、connectionExample、note、nullable lastEvent/lastError/startedAt。
- REST config 保持 enabled、transport、host、port、path、port_explicit/portExplicit、has_auth_token/hasAuthToken、auth_token_masked/authTokenMasked。不会返回明文 token。
- REST 的 mcp/status、mcp/tools、mcp:start、mcp:stop 只使用上述 status/catalog 投影并带 request_id；status/tools 没有启动 side effect，start/stop 只受 P13B gate 控制。

### 工具目录、输入与结果

mcp.schema.json 的 tool_call 是 28 个现有工具的完整机器可校验目录。输入字段保持 camelCase，HTTP 与 stdio 不得为同一工具提供不同 schema：

| 域 | 工具 |
| --- | --- |
| 项目 | list_projects、get_project、create_project |
| Requirement | list_requirements、create_requirement、get_requirement、update_requirement、delete_requirement、list_requirement_plan_links、replace_requirement_plan_links、upload_requirement_attachment |
| Feedback | list_feedback、create_feedback、get_feedback、update_feedback、delete_feedback、list_feedback_plan_links、replace_feedback_plan_links、upload_feedback_attachment |
| Attachment | delete_attachment |
| Plan / Task | list_plans、get_plan、list_tasks |
| Executor / Loop | list_executors、run_executor、stop_executor、start_loop、stop_loop |

结果使用原 MCP CallToolResult：成功是一条 text content 加同值业务投影 structuredContent；失败是 isError=true、一条 bounded text 及稳定 structuredContent.error/code/errorCode。结果业务顶层字段保持既有名称：项目为 projects/project/snapshot，Intake 为 requirements/feedback/requirement/feedback/openable/snapshot，Plan/Task 为 plans/plan/tasks，Executor 为 executors/executor/executorId/status/exitCode/durationMs/logTail/dependencyResults/snapshot，Loop 为已有 snapshot/operation 摘要。P13 可脱敏、截断或以安全相对标识替换敏感值，但不得复制业务规则、改变成功/失败类别或返回第二套 DTO。

projectId、resource ID、label、workspacePath、manual、command/args/cwd/env/PID、operation_id、localhost 来源和 token 都不是授权证明。handler 仅可解码、构造 caller context、调用共享 application service 并编码结果；禁止直接 SQL、repository、文件系统、进程实现或领域状态机写接口。

### P13B 稳定错误码

mcp_disabled、mcp_prerequisite_failed、mcp_transport_invalid、mcp_auth_failed、mcp_tool_not_found、mcp_tool_invalid、mcp_tool_forbidden、mcp_tool_conflict、mcp_tool_unavailable、mcp_tool_timeout、mcp_tool_internal 是 P13B transport 稳定代码。共享服务错误继续映射既有 invalid_intake、invalid_attachment、attachment_path_denied、attachment_recovery_required、not_found、duplicate_intake、precondition_failed、relation_conflict、idempotency_key_reused、request_in_progress、unsupported_media_type、request_timeout、service_unavailable、internal_error。

错误不得带 internal repository、SQL、path、token、env、tool 原始参数或堆栈。DUPLICATE_INTAKE 保留 intakeType 和既有 ID 摘要兼容字段，但仅在调用者已经获得该 project 授权时返回。
