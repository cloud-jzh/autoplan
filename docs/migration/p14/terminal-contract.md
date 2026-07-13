# P14 Terminal 冻结契约

状态：**frozen / default-off / blocked until evidenced**。本文件冻结 Terminal 的 REST 控制面、独立 WebSocket 数据面、PTY 生命周期与兼容边界；不表示 Go PTY、HTTP handler 或 WebSocket 已实现，更不授权启用或删除旧 IPC。

机器可读权威定义为 `backend/openapi/openapi.yaml` 与 `backend/openapi/schemas/terminal.schema.json`。Node 兼容面权威定义为 `src/terminal/terminalTypes.js`、`terminalService.js`、`terminalIpc.js`、`src/preload.js` 及 renderer 类型。任何未列出的行为均为拒绝，不能从 localhost、session ID、PID、cwd 或 renderer 声明推断授权。

## 兼容 DTO 与默认值

现有 Node IPC 保留 camelCase `TerminalSession`、`TerminalProfile`、`TerminalErrorResult` 与所有 `create/list/write/resize/kill/close/rename/replay/clear` callable。Go REST 使用 snake_case 同义 wire DTO；P006 负责转换，不能改变 renderer 可见语义。

| 对象 | 冻结字段与语义 |
| --- | --- |
| `TerminalSession` | `id` 为字符串（`term_...`），`projectId`/`project_id`、`title`、`cwd`、`shell`、`status`、`createdAt`/`created_at`、`endedAt`/`ended_at`、`exitCode`/`exit_code`、`cols`、`rows`、`profile`、`closed`、`runtime`。时间都是 UTC RFC 3339 `Z`；`ended_at`、`exit_code` 可为 `null`。不含 PTY、PID、句柄、输入或输出。 |
| `TerminalProfile` | `id`、`name`、`kind`（`default`/`custom`）、`shellPath`/`shell_path`、`args`、`env`。现有 IPC 继续保留 profile 投影。Go REST 读模型保留 `env: {}` 字段形状，但环境值只可作为受控 write-only 创建意图。 |
| `TerminalErrorResult` | IPC 始终是 `{ ok:false, code, message, details? }`；`details` 是稳定、脱敏的短原因，绝不包含 OS 原文、真实 cwd、完整命令、env 或认证材料。REST 使用既有错误 envelope 并保留等价稳定 terminal code。 |
| snapshot 终端元数据 | 只允许以上安全会话元数据和 `runtime`；不得放入 scrollback、output seq、input、env、PTY/PID 或连接状态。 |

默认值固定为 `cols=80`、`rows=24`、`title="Terminal"`、`retain_on_exit=true`、`scrollback_limit=10000`。尺寸范围是 `cols 2..500`、`rows 1..200`；标题最多 80 字符、cwd/shell 最多 2048、单次 input 最多 65536 UTF-8 字节、profile 参数最多 32 项且每项最多 512 字符。`null`、缺失字段和空字符串不得相互替换。

`runtime` 是创建时写入且不可变的 owner：旧 Node 会话为 `node`，P14 Go 会话为 `go`。相同字符串 ID 不允许成为跨 runtime 接管、输入复制或输出合并依据。

## REST 控制面

所有路由复用 loopback session、精确 Origin、Host、项目/会话归属、body 限制、deadline 与速率限制。控制面不传输原始输出；成功 response 为 `{data, request_id}`，错误带稳定 code 和 `request_id`。

| 方法与路径 | 冻结功能 |
| --- | --- |
| `POST /api/v1/projects/{project_id}/terminals` | 创建；body 为 `cwd`、`profile_id`/`profile`、`title`、`cols`、`rows`、`retain_on_exit`、write-only `env`。同一幂等键必须复用同一 admission，冲突失败。 |
| `GET /api/v1/projects/{project_id}/terminals` | 仅列出调用方获授权项目的元数据，最多 32 个。 |
| `DELETE /api/v1/terminals/{id}` | 兼容 close；幂等地关闭创建 runtime 的 PTY/进程树。 |
| `POST /api/v1/terminals/{id}/actions/write` | 受限 UTF-8 input。 |
| `POST /api/v1/terminals/{id}/actions/resize` | 固定整数尺寸。 |
| `POST /api/v1/terminals/{id}/actions/kill` | 终止完整进程树，不删除其他 runtime 会话。 |
| `POST /api/v1/terminals/{id}/actions/close` | 与 DELETE 等价的显式兼容动作。 |
| `POST /api/v1/terminals/{id}/actions/rename` | 非空 title。 |
| `GET /api/v1/terminals/{id}/replay?last_seq=N` | 读取有限、仅内存的 output 回放。 |
| `POST /api/v1/terminals/{id}/actions/clear` | 只清理内存回放，绝不重置 output sequence。 |

REST code 固定为：`terminal_feature_disabled`、`terminal_platform_blocked`、`terminal_pty_unavailable`、`terminal_invalid_payload`、`terminal_invalid_session`、`terminal_session_not_found`、`terminal_project_not_found`、`terminal_forbidden`、`terminal_cwd_outside_workspace`、`terminal_write_failed`、`terminal_resize_failed`、`terminal_kill_failed`、`terminal_replay_gap`、`terminal_cursor_too_old`、`terminal_session_limit`、`terminal_connection_limit`、`terminal_rate_limited`、`terminal_slow_consumer`、`terminal_protocol_error`。缺 gate、application service、Files policy、PTY 能力或安全依赖时一律失败关闭，单次请求不得隐式回退 Node。

## 独立 WebSocket 数据面

唯一数据面为 `GET /api/v1/terminals/{id}/ws?last_seq=N`。升级前同时验证 HTTP session/用途/过期、精确 Origin、Host、会话项目归属及当前授权；认证值绝不放在 URL、frame、日志或证据。项目 SSE、Chat/MCP SSE、Operation/outbox 与通用事件总线永远不承载 Terminal input/output。

客户端 JSON text 信封只允许：`{type:"input",data}`、`{type:"resize",cols,rows}`、`{type:"ping",nonce}`。服务端只允许：`output(seq,data)`、唯一 `exit(exit_code,signal)`、`status(session)`、`closed(closed:true,session)`、`pong(nonce)`。未知 type、未知/重复 JSON 字段、非 text frame、非法 UTF-8、深度超过 4、消息超过 65536 字节、越界尺寸或不符合类型的字段在 dispatch 前拒绝。

`seq` 从 1 开始，单会话严格递增，不因 `clear`、resize、重连或 listener 更换重置。连接先发送 `(last_seq, watermark]` 内的升序回放，紧接 live output，不能丢失、重排或重复；游标早于保留水位、未来、错误或无法证明连续性时返回 `terminal_replay_gap`/`terminal_cursor_too_old`，不伪造 output。`exit` 对每个 session 最多一次，并发生在最后一个 output 之后；正常终态随后发最终 `status`，然后 `closed`。现有 Node IPC 的显式 close 仅回流 `closed` 的历史行为保持兼容，不能被解释为 Go WebSocket 的缺失 exit。

| 项目 | 硬上限与失败行为 |
| --- | --- |
| 帧/消息 | 65536 UTF-8 字节；解析深度 4；超大帧关闭 `1009`，非法 UTF-8 `1007`，未知类型或重复字段 `1002`。 |
| 内存回放 | 每 session 最多 4096 entries 且 1 MiB；按字节和条目任一水位淘汰旧内容，绝不持久化。 |
| 会话/连接 | 全局 32 session、每项目 8 session、每 session 4 连接、全局 128 连接。达到上限拒绝，不排队。 |
| 输出背压 | 每连接最多 64 未发送 frame 和 1 MiB；满时以 `1013` 关闭慢连接，不阻塞 PTY reader、其他连接或 daemon。 |
| 输入/resize | 每 session 每 10 秒最多 256 KiB input、20 次 resize；超限稳定限流失败。 |
| 心跳/deadline | ping 每 15 秒、pong 宽限 10 秒、读 deadline 30 秒、写 deadline 10 秒。超时只 detach 该连接。 |
| 保留/生命周期 | 断线运行中 PTY 最多保留 5 分钟；单 session 最长 4 小时。进程退出、显式 kill/close、保留到期、资源驱逐和 daemon shutdown 都关闭同一 runtime 的完整进程树。 |

`1008` 表示授权/Origin/policy 拒绝，`1011` 表示不可恢复内部故障。服务端开始 shutdown 时不创建新连接，并以 `1012` 关闭已有 WebSocket；它不将 Go 句柄交给 Node。

## 生命周期、PTY 与持久化隔离

创建后的状态是 `starting -> running -> (exited | killed | error) -> closed`。每个 session 的 output 只能是 `data* -> exit -> status -> closed`，终态事件唯一且 close/kill/超时/取消并发时必须幂等。任何 transport 只能调用共享 Terminal application service；REST、WebSocket、renderer adapter 都不能直接持有 PTY 或 session map。

PTY spawn 前必须以服务端项目记录取得 workspace，并执行共享 Files policy 的规范化、realpath、目录存在性、允许根与调用方授权检查。相对路径、`..`、Windows 盘符/大小写/UNC/junction、Unix/Windows symlink 和 TOCTOU 逃逸默认拒绝。shell、args、env 由受控配置/allow-list 构造；禁止 shell 字符串拼接。缺少 ConPTY/Job Object、Unix PTY/process group、Files policy 或资源限制时拒绝 spawn，不能退化为无监管 child process。

原始 terminal input/output、完整 env、secret、认证材料、完整命令环境、PTY/PID/handle、真实 userData 及未授权绝对路径不得进入项目 SSE、Chat/MCP SSE、Operation、outbox、snapshot、SQLite、审计正文、日志、fixture、证据或错误。只有脱敏会话元数据和受限审计结果可以离开内存；原始 output 只存在上表约束的内存回放和已授权的本 WebSocket 连接。

## 门禁与兼容切换

唯一开关是 `go_terminal_api` / `AUTOPLAN_SIDECAR_GO_TERMINAL_API`，在 Go `features.go` 和 renderer 合同中默认 `false`。它必须一次性决定**新** Terminal REST 创建和 WebSocket 连接：只打开 REST 或只打开 WS 都是 `terminal_feature_disabled`。本 P001 仅登记该 default-off 合同，P006 以前不改变任何 Node IPC 路由。

新会话在 admission 固定 runtime。切 flag、回滚、renderer 重连、daemon 重启或同 ID 请求均不得接管另一 runtime 的 PTY：Node 会话继续 Node IPC；Go 会话继续 Go REST/WS。Go daemon 重启只能关闭/报告它自己的 Go 会话，不能把它们重建为 Node 会话；Node/Electron 重启同理。Terminal 失败只影响 Terminal，不得重置 Chat/MCP gate、SSE、Operation 或已完成的 P13 回滚边界。

Windows、macOS、Linux 必须分别提供对应打包 artifact 的真实 PTY、input/output、resize、exit、kill/close、reconnect、进程树清理、签名和退出码证据。任何一个平台不可证明时，该平台保持旧 IPC/Node PTY，记录 `blocked`；不得用源码单测、交叉编译或另一平台结果代替。
