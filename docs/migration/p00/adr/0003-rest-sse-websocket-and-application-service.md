# ADR-0003：REST/JSON、SSE、WebSocket 与共享 application service

- 状态：Accepted
- 日期：2026-07-11
- 决策范围：renderer、MCP 与 Go sidecar 的业务入口和事件传输
- 关联基线：[能力矩阵](../capability-matrix.md)、[契约基线](../contract-baseline.md)、[ADR-0001](./0001-local-go-sidecar.md)、[ADR-0004](./0004-contract-files-flags-and-rollback.md)

## 背景

现有 preload/IPC 同时承载查询、mutation、完整 snapshot、增量 patch、Chat 流和 Terminal 双向数据。后端分离需要按交互语义选择 transport，同时确保 UI、MCP 和未来 HTTP 调用不会复制业务规则、授权或文件策略。

## 决策

本机 sidecar 采用以下唯一分工：

| transport | 负责 | 不负责 |
| --- | --- | --- |
| REST/JSON | 查询、CRUD、业务命令提交、Terminal 会话 create/list/close 等控制面 | 长连接增量、PTY 字节流 |
| SSE | AppSnapshot/patch、Operation、Chat、配置变更等服务端单向事件 | Terminal 输入、resize 或无界 PTY 输出 |
| WebSocket | Terminal 的双向 input/data/resize、状态、exit/closed 生命周期 | 普通 CRUD、Chat 或全局 snapshot 广播 |

接口统一置于版本化本机路径（初始为 `/api/v1`）；版本表示 transport/DTO 兼容边界，不授权在同一版本内重命名现有字段。

## 共享 application service

REST handler、SSE publisher、WebSocket handler、MCP tools、后台调度和 Electron 兼容 adapter 必须调用同一组 application service。它们不得直接拼 SQL、直接改文件或各自实现状态转换。

一次调用固定经过：

```text
session/origin → decode/size limit → project authorization → file policy
→ application service → domain transaction/side effects
→ DTO mapper → response/event
```

- 项目隔离、状态机、批量预校验、幂等/重复提交策略和错误 code 在 application service 中实现一次。
- 数据库 repository、工作区文件、Agent CLI、网络和 PTY adapter 只由 application service 编排。
- MCP 与 UI 的同名操作得到相同业务结果和副作用；MCP 不能因是本机 stdio/HTTP 调用而绕过项目、路径或 secret 策略。
- HTTP adapter 不成为“公共管理后门”。未在能力矩阵登记的内部方法默认不暴露。

## REST/JSON 规则

1. GET 只做无副作用查询；创建、更新、删除和 start/stop/accept 等命令使用相应 mutation 方法，不能用 GET 触发写入或进程副作用。
2. 输入和响应继续使用契约基线中的字段名、nullability、默认值、排序、整数 ID、UTC 时间和兼容 mutation `AppSnapshot` 返回。transport 层不得把既有 `{accepted:false,error}` 或 Terminal `{ok:false,code,...}` 擅自改成另一业务语义。
3. HTTP status 表示 transport/认证/资源边界，响应体仍携带稳定业务 code 和兼容 message。可重试标记不允许自动重放非幂等 create 或包含外部副作用的命令。
4. mutation 接受受控 request ID/operation ID 用于追踪和显式去重；它不能改变当前 create 非幂等契约。只有 application service 已登记结果的相同请求才能返回原结果。
5. request body、响应和错误执行大小/数量上限。秘密、环境值和未经授权的绝对路径在 DTO mapper 之前剔除。

## SSE 规则

SSE 承载当前 `loop:update`、`loop:patch`、Operation、Chat chunk/done/queue/title/config changed 和脱敏配置事件。服务端事件信封提供事件类型、项目/会话作用域、单调序号和兼容 payload；renderer adapter 再映射为当前 preload 回调形态，不改变 payload DTO。

- 每个项目/对话的序号和发送顺序由同一 publisher 串行化。snapshot 到达时清除更早 patch；同一帧的后续 patch 仍按现有字段存在性合并。
- Task 行写入先于 task 事件；Chat 保持当前队列释放/pump/done 顺序；配置事件只含 masked/has-secret 形态。
- client 使用最后确认的事件 ID 恢复。服务端无法覆盖缺口、进程重启或检测到序号跳跃时，不猜测 patch，改为发送/要求重新读取完整 snapshot。
- 心跳只证明连接存活，不是业务事件，不推进业务序号。慢消费者超过有界缓冲时断开并要求 snapshot 重同步，不能无限占用内存。
- 不把 Terminal 数据放入 SSE，避免单向文本事件承担输入、resize、字节序和背压。

## WebSocket Terminal 规则

Terminal 先经 REST 创建或确认会话，再用短期、单会话授权连接 WebSocket。握手校验本机 session、Origin、project、terminal session 和 workspace/cwd 文件策略；URL query 不携带长期 token。

WebSocket 帧类型覆盖 input、data、resize、status、exit、closed 和受控 replay。实现必须保持同一 session 的 PTY chunk 顺序，先持久/更新 session 状态，再发送 exit/status，显式 close 的 closed 最后到达。输入和输出采用有界帧、有界 scrollback 与背压；断线不自动创建第二个 PTY。重连通过 list/replay 恢复现有会话，不能把 session ID 当作授权凭据。

Terminal 的 create/list/kill 等控制命令仍走同一 Terminal application service；WebSocket handler 不直接操作 `node-pty`/Go PTY adapter。

## 会话与连接失败

- Electron 启动每个 sidecar 实例时建立高熵本机会话；REST、SSE 和 WebSocket 使用同一认证来源并分别执行用途校验。
- sidecar 重启使旧连接和会话失效。renderer 通过 Electron 完成新握手，再读取完整 snapshot、恢复 Chat/Terminal 可恢复视图。
- SSE 或 WebSocket 断开不等于业务命令失败；客户端依据 operation/message/session 的权威状态查询，不能盲目重发 mutation。
- sidecar 不可用时返回明确 unavailable。首笔 Go 正式写入后不得绕过 transport 直接让旧 Node 打开数据库。

## 不采纳的方案

- **全部使用 REST polling**：无法保持 patch、Chat 和 Operation 的低延迟顺序，且增加重复查询。
- **所有事件都用 WebSocket**：把普通查询、命令和单向事件混在一个状态协议中，难以恢复和审计。
- **Terminal 使用 SSE + REST input**：双向顺序、背压和 session 生命周期复杂且容易乱序。
- **UI 与 MCP 各自实现 service**：会造成授权、文件边界、状态机和副作用漂移。

## 结果

该分工让普通业务保持可检查的请求/响应，流事件具备恢复语义，Terminal 获得真正双向通道。代价是必须实现三个 adapter、连接恢复、序号/背压以及统一会话验证。新增 transport 或绕开 application service 的入口必须先更新能力矩阵并用新 ADR 取代本决策。
