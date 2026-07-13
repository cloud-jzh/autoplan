# P13 独立门禁与回滚边界

状态：**default-off / blocked until evidenced**。本 runbook 定义 P13A Chat 与 P13B MCP 的开关、证据和回滚规则。P001 没有执行任何验证，也没有声称任一门禁已通过。

## 不可替代的前置

P13A 与 P13B 都必须独立核验以下条件。任一项缺失、过期、hash 不一致、证据无法脱敏或结论不确定时，相关 gate 的结果是 failed-closed 和 blocked：

1. P10、P11、P12 的 passed evidence 与 P00 冻结红灯签名可复核，且没有新增、隐藏、重命名或跳过失败。
2. Go 是活动业务库的唯一数据库 owner；Node/sql.js 不会与 Go 双写，且 Node 回退只经 GoDataClient。
3. Files policy 对 project、附件、Plan、workspace 与 realpath 规则有效并且默认拒绝。
4. secret mapper 已覆盖 Chat provider、Claude/Codex、MCP auth；API key、token、env_vars 和正文不会进入 DTO、SSE、日志、审计、fixture 或证据。
5. Operation、统一 process runner、取消/超时、输出边界、审计和持久化 outbox 能力可复核。
6. 验证只使用脱敏 fixture、系统临时库或调用方明确副本；不自动发现、读取或写入真实 Electron userData。

满足共同前置不等于任何 P13 gate 自动通过。Chat 与 MCP 各自还需专属证据。

## Flag 与状态机

| 门禁 | 唯一 feature flag | 环境变量 | 默认 | 仅控制 |
| --- | --- | --- | --- | --- |
| P13A | go_chat_api | AUTOPLAN_GO_CHAT_API | false | 新 Chat REST/SSE 与 Chat HttpAutoplanClient 路由。 |
| P13B | go_mcp_api | AUTOPLAN_GO_MCP_API | false | 新 MCP HTTP/stdio transport、catalog adapter 与生命周期。 |

features.go 仅接受严格 true 或 false，重复、非法或缺失依赖均不放宽为 true。两个 flag 没有蕴含关系：P13A 通过不能开启 P13B，P13B 通过也不能开启 P13A。

每条门禁的状态为：

| 状态 | 允许行为 |
| --- | --- |
| off | 使用当前允许的兼容 adapter；不启动对应 Go 新路径。 |
| blocked | 维持 off，记录稳定失败码和脱敏原因；不得由另一门禁代偿。 |
| shadow | 只对安全副本做无副作用对比；不接受生产 mutation、provider turn 或 MCP listener。 |
| limited | 仅在该门禁的证据清单完整后，按本门禁独立观测。 |
| on | 只影响新 admission；在途工作保持原 runtime owner。 |
| rollback | 停止新 admission，保全证据与唯一 Go writer，不恢复 Node/sql.js writer。 |

## P13A Chat gate

进入 shadow 或 limited 前，除共同前置外必须有：

- Conversation、ChatMessage、ChatQueue、ChatDone、历史排序、nullability、双命名、snapshot 投影与稳定错误码的契约比较；
- Conversation/Message 持久化、每会话 FIFO、send/stop/clear、queued/processing 语义、部分消息、唯一终态、崩溃恢复和幂等场景的专属证据；
- provider 与 Operation/process runner 的关联、取消/超时/进程树、脱敏、输出上限和审计证据；
- chat_chunk、chat_queue、chat_done 的 event_id、project_revision、turn sequence、Last-Event-ID、去重、gap、水位、resync、慢消费者及项目/会话隔离证据；
- Chat REST、SSE、UI HttpAutoplanClient 切换清单，以及独立的 P13A manifest、命令、真实退出码、输入副本 hash、输出脱敏记录和剩余风险。

P13A 的失败触发器包括：任一 turn 出现重复/丢失、chunk 在 done 之后、done 非唯一、FIFO 被破坏、重连重跑 provider、跨项目正文、泄露 secret/path/tool data、跨 runtime stop/接管、双写、无法复原的 queue 或前置证据漂移。

P13A 失败只禁止 go_chat_api 新路由。它不停止、降级或回滚 MCP transport，也不改变 go_mcp_api。

## P13B MCP gate

进入 shadow 或 limited 前，除共同前置外必须有：

- HTTP 与 stdio 的 loopback、token/session/Origin、method/path、body/连接/超时、半帧、断连、重复 initialize/call、shutdown 和 stderr/stdout 边界证据；
- 28 个冻结工具名、输入 schema、成功结果投影、稳定错误映射和全部 project/resource scope 的逐工具对比；
- HTTP、stdio、REST 与 UI adapter 同时指向相同 application service、repository、Files policy、Operation 和 audit 实例的架构证据；
- 成功、幂等重复/冲突、并发、无权限、跨项目、resource enumeration、Files realpath 逃逸、非法状态、限流和依赖不可用的一致性矩阵；
- P13B 专属 manifest、命令、真实退出码、脱敏输出、catalog hash、transport capability、listener 生命周期、回滚点和剩余风险。

P13B 的失败触发器包括：双 listener、HTTP/stdio catalog 或输入差异、直接 SQL/repository/文件/进程/状态机依赖、绕过权限/Files policy/幂等/审计、重复 mutation、token/路径/env/tool 私密数据泄露、stdio stdout 混入诊断、无法关闭 listener 或前置证据漂移。

P13B 失败只禁止 go_mcp_api 新 transport。它不改变 Chat REST/SSE、队列、provider 或 go_chat_api。

## 切换与回滚

### 切换前

1. 为目标门禁单独复核前置和专属 evidence；未通过即 blocked。
2. 固定当前 feature flag、sidecar 版本、契约/schema/catalog hash、数据库 owner、listener/Operation 清单和安全副本标识。
3. 确认另一门禁的状态原样保留；不要把两个 flag 放入同一原子“迁移成功”判断。
4. 打开目标 flag 前，仅让新 admission 使用目标 runtime。任何已接受 Chat turn、Operation、MCP mutation 或连接保持其创建 runtime。

### P13A 回滚点

在 Chat 新 admission 之前可关闭 go_chat_api 并继续使用允许的旧 adapter。关闭后：

- 已开始的 Chat turn 与关联 Operation 留在创建它的 runtime，直到 done、error、aborted 或 interrupted；
- 不跨 runtime stop、restart、replay 或重新归并 chunk；
- 未完成状态以权威持久化历史和 queue 复核；需要恢复时只重放事件或读取状态，不重放 provider 请求；
- Go 已写入的数据不交给 Node/sql.js writer。UI 兼容 adapter 如需回退，仍经 GoDataClient/Go application service。

### P13B 回滚点

在 MCP 新 transport 接受 call 前可关闭 go_mcp_api 并关闭该 Go listener。关闭后：

- 已接受的 MCP mutation、Operation 和 connection 由创建它的 runtime 完结；另一个 runtime 不接管、停止或重放非幂等 call；
- 停止目标 listener 不启动第二个 Node MCP listener，不把 token、会话或 call context 转交给旧 server；
- 任何已由 Go 持久化的状态保持 Go 唯一 writer；需要兼容 UI/REST 时继续经共享 Go service；
- stdio 仅结束自己拥有的 transport，不能将半帧或未完成请求转投 HTTP/Node。

### 首笔 Go 写入后的硬边界

关闭 flag 不是数据库 owner 回滚。首笔 Go 正式写入后，唯一允许的恢复方向是兼容 Go sidecar 版本回退、冻结 mutation、完整状态重读、Go 前向修复或已批准的 Go 数据恢复流程。不得热启 Node/sql.js writer、自动截断数据库、自动还原真实 userData、用另一 runtime 接手 process 或 listener。

## 旧路径删除门槛

P13A 旧 Chat IPC/controller/queue 路径只能在 P13A 自身连续稳定证据、零回退使用量、在途 turn 清零、独立回滚演练和零引用检查均成立后删除。P13B 状态对此无影响。

P13B 旧 MCP IPC/HTTP/stdio/tool 实现只能在 P13B 自身连续稳定证据、零回退使用量、listener/call 清零、独立回滚演练和零引用检查均成立后删除。P13A 状态对此无影响。

任一条件不能证明时保留兼容 adapter，但它不得成为第二个数据库 writer、第二个 listener、第二套 repository 或第二套业务状态机。

## P010 legacy path disposition

The removal policy is recorded in `legacy-removal-manifest.json`. Its initial
state is `blocked_until_independent_evidence`: this repository contains no
authorization to delete a P13A or P13B compatibility path merely because the
other gate is enabled.

When `go_chat_api` is enabled, preload Chat turn/history/queue and conversation
callables remain only as rejected compatibility placeholders and main-process
Chat IPC rejects before controller or queue creation. Existing Chat work remains
with its creator; no Node controller is constructed for a new Go Chat admission.
Chat configuration routes remain outside this removal set until their own shared
service contract exists. This does not change MCP state.

When `go_mcp_api` is enabled, the historical Node MCP server and tool catalog
must not start, advertise tools, or accept calls. A failed Go MCP listener
blocks only new MCP admission. It does not start a Node fallback listener,
transfer an accepted call or connection, or alter Chat state.

Actual deletion requires the named gate's own stable evidence, zero legacy
usage, zero in-flight work, a rollback exercise, and a zero-reference inventory.
After the first Go write, rollback remains a compatible Go version or frozen
mutation/state recovery; it never re-enables Node/sql.js as a writer.
