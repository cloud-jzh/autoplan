# P14 Terminal 门禁与回滚

状态：**default-off / blocked until independent evidence**。本文件定义 P14 的运行时和三平台门禁。P001 没有运行打包、PTY、WebSocket、回归或安全验证，也没有声称任一平台已通过。

## 唯一开关与状态

| 门禁 | feature flag | 环境变量 | 默认 | 只控制 |
| --- | --- | --- | --- | --- |
| P14 Terminal | `go_terminal_api` | `AUTOPLAN_SIDECAR_GO_TERMINAL_API` | `false` | 新 Go Terminal REST admission **和** Terminal WebSocket upgrade 的原子组合。 |

解析只接受严格 `true`/`false`；重复或非法值是 `runtime_feature_duplicate`/`runtime_feature_invalid` 并失败关闭。`go_terminal_api` 不蕴含、也不受 `go_chat_api`、`go_mcp_api`、Script、Executor 或任何其他 flag 蕴含。状态为 `off`、`blocked`、`shadow`、`limited`、`on`、`rollback`：除 `on` 外均不得接受生产 Go Terminal admission；`shadow` 只能使用脱敏 fixture、临时库或明确副本。

P008 的旧 Node PTY 收缩不是第二个运行时 feature flag，也不能由环境变量、renderer 或 WebSocket 请求触发。它是随 Electron 包发布的按平台证据策略，见 [`legacy-removal-manifest.json`](legacy-removal-manifest.json)。当前 Windows、macOS、Linux 都是 `blocked`，因此 `node-pty`、`asarUnpack` 资源和旧 IPC 均保留。只有该平台已接受的 manifest hash、至少三次连续打包 smoke、owner 与未过期风险记录同时写入随包策略后，才停止**未来** Node 创建。

## 不可替代的前置

进入 shadow、limited 或 on 前，所有条件必须可复核；任一缺失、过期、hash 不一致、未脱敏或结论不确定即 `blocked`。

1. P11 runtime/process tree 与 P13 Chat/MCP 双门禁都有可验证 passed evidence，P00 红灯签名没有被掩盖或跳过。
2. Go 是业务 SQLite 的唯一 writer；Node/sql.js 不双写。Terminal output 不写 SQLite、snapshot、outbox、Operation 或项目 SSE。
3. 服务端 session、精确 Origin、Host、项目/会话归属、Files policy realpath 和调用方授权完整可证；session ID、localhost、PID 和 cwd 都不替代授权。
4. P14 REST、WebSocket、renderer adapter 都只调用同一 Terminal application service；transport 不直接管理 PTY、process tree 或 session map。
5. 所有输入、输出、backpressure、session/connection、replay、速率、deadline、保留期和终止路径均有 P14 契约硬上限；无界队列/缓存/日志是 failed。
6. 证据只使用脱敏 fixture、临时库或明确副本；不自动发现、读取或写入真实 Electron userData。

## 平台专属 gate

`go_terminal_api` 不能覆盖平台能力。以下每个平台要独立收集**打包后**实证；没有该平台 artifact、签名信息、命令、argv、真实退出码、UTC 时间、hash、脱敏 output 与剩余风险即保持旧路由并标记 `blocked`。

| 平台 | 必须证明 | 未证明时 |
| --- | --- | --- |
| Windows | ConPTY，Job Object 或可证明等价进程树，盘符/大小写/UNC/junction Files policy，kill/close 后无孤儿树。 | 保留 Windows Node IPC/node-pty；不创建 Go session。 |
| macOS | PTY、process group、正确信号、symlink Files policy、input/output/resize/reconnect/终止清理。 | 保留 macOS Node IPC/node-pty；不创建 Go session。 |
| Linux | PTY、process group、正确信号、symlink Files policy、input/output/resize/reconnect/终止清理。 | 保留 Linux Node IPC/node-pty；不创建 Go session。 |

必须分别覆盖：create/list/write/resize/rename/replay/clear/kill/close、自然与异常 exit、唯一 `exit/status/closed`、cursor 水位、replay-live 交界、慢客户端、半开连接、心跳、非法 UTF-8/重复字段/未知 frame、跨项目/权限变化、cwd escape、spawn/read/write/resize/kill 失败和 daemon shutdown。源码单测、交叉编译、模拟 PTY、另一 OS 或未签名 artifact 不能作为平台通过证据。

## P008 旧路径收缩与清单

停止 Node 创建前，目标平台的 release manifest 必须同时列出并复核：`terminal:create` 至所有旧 IPC handler、preload 暴露、renderer IPC adapter、`terminal:data/exit/status/closed` 事件、`node-pty` 依赖、`asarUnpack` 资源及对应运行时零旧 admission 指标。任一条缺失、证据过期、hash 不匹配或指标非零，结论为 `blocked`；不得删除共享代码、依赖或其它平台的资源。

收缩采用两步：先在该平台关闭 `terminal:create` 对新 Node PTY 的 admission，既有 Node session 继续使用 list/write/resize/rename/replay/clear/kill/close 与原 IPC events；待它们排空、零旧路由使用和独立回滚演练都有新证据后，才允许在后续发布删除该平台专属打包资产。共享 `node-pty`、preload API 或 renderer 适配器只能等 Windows、macOS、Linux 全部完成该步骤后删除。

## 切换

1. 固定 P11/P13/P00 前置、sidecar/Electron 版本、schema/OpenAPI hash、当前 flag、数据库 owner、平台 artifact hash 与脱敏 manifest。
2. 仅当目标平台 gate 与所有共同前置均为 passed，才将该平台的新会话 admission 切到 `go_terminal_api`。
3. 一个 admission 同时固定 REST owner 与 WS owner；Go REST 成功后 WS 失败不调用 Node IPC 重试，Node 创建成功后也不能改接 Go WS。
4. 已存在 Node 会话仍只通过 Node IPC；已存在 Go 会话仍只通过 Go REST/WS。切 flag 后仅影响新会话，不复制输入、输出、scrollback 或 PTY 句柄。
5. Chat/MCP、项目 SSE、在途 Operation 和各自回滚点保持原样。Terminal gate 不启动、停止、接管或复位它们。

## 回滚与异常处理

关闭 `go_terminal_api` 意味着停止**新的** Go Terminal admission 和 WS upgrade；不是跨 runtime 接管，也不是 Node/sql.js writer 回滚。现有 Go session 由 Go 保留至 exit/kill/close/保留期到期；已有 Node session 保持 Node。daemon 重启时 Go 仅终止并报告自己的 session，renderer 显示可恢复状态后显式创建新会话，绝不把旧句柄伪装为另一 runtime。

以下任一项立即使目标平台转为 `blocked` 并停止新 Go admission：PTY/进程树未清理、seq 重复/缺失、exit 非唯一、replay gap 被伪造、慢客户端阻塞读取、权限/Origin/Files policy 绕过、原始 output/env/secret 泄漏到持久化或 SSE、任一资源无界、跨 runtime 接管、打包/签名/证据失真、P00/P11/P13 前置漂移。保全脱敏证据，关闭 Go listener；不要自动把失败请求投递到 Node。

`go_terminal_api` 的 rollback 和旧 Node PTY 收缩是独立的：关闭 Go 只停止未来 Go REST/WS admission；不会把现有 Go session 改成 Node，也不会把现有 Node session 改成 Go。若某平台已关闭 Node 创建，Go rollback 后的新创建请求会明确失败，绝不自动 fallback、复制输入/输出或双写 PTY。恢复旧 Node 创建只能通过新的平台发布和新的已接受证据，不通过运行时开关。任一平台收缩失败即安全停止，其他平台以及 Chat/MCP 状态不受影响。
