# P15 Electron 薄壳、IPC 与旧路径冻结契约

状态：**frozen / migration evidence pending**。本契约记录 P001 观察到的 Electron、Node 和 Go 拓扑，以及 P005/P006 后必须达到的薄壳目标。它不是删除授权，也不把当前仍存在的 Node 业务 IPC、sql.js、Loop、Chat、Script、Executor、MCP 或 Terminal 实现标为已完成迁移。

机器可读的 IPC 目标面是 `ipc-allowlist.json`；当前需先移除的 IPC 和 Node 模块在 `legacy-removal-manifest.json`。静态库存由 `inventory-topology.js` 产生；`trace-runtime.js` 为隔离 E2E/集成 runner 提供不含 payload 的 recorder 并校验运行时证据。每个工具仅接受显式、绝对路径的 P15 授权 fixture，拒绝真实 Electron userData、数据库、锁文件、符号链接、秘密、未授权绝对路径和无界证据。

## 冻结的拓扑

```text
renderer components / hooks
          │
          ├── AutoplanClient / transport ──> Go REST + project/operation/chat SSE
          │                                  └── Terminal 专用 WebSocket
          │
          └── DesktopBridge ──> preload ──> Electron native/update/sidecar lifecycle

Go HTTP / SSE / WebSocket / MCP adapters ──> application service ──> domain/repository
```

`src/renderer/lib/api/ipcClient.ts` 是遗留 IPC 兼容 adapter，`src/renderer/lib/desktop/ipcBridge.ts` 只负责桌面原生能力。业务组件、页面和 hooks 不得直接调用 `window.autoplan` 的业务 callable，也不得引入 `ipcRenderer`。库存将任何非这两个 adapter 的 `window.autoplan` 访问作为 P005 前必须消除或迁入 adapter 的拓扑债务；不能因该访问是只读或当前可用而当作薄壳达成。

Go adapter 必须复用 application service：`backend/internal/httpapi`、`backend/internal/mcp` 只能执行认证、输入边界、DTO 映射和流传输，不能实现第二份业务规则或直接绕过 repository。项目、Operation 与 Chat 的实时通道是有界 SSE；Terminal 输入/输出只通过其独立、认证且 Origin 校验的 WebSocket，不进入项目 SSE、Chat/MCP SSE、snapshot、SQLite 或日志。

## DTO、错误和所有权不变量

- 现有 renderer DTO、`snake_case` wire 字段、snapshot 投影、默认值、错误 code 与业务规则均冻结。新 transport 只能映射，不能重新命名、扩张或用 `null`/空字符串改变语义。
- Electron 保留窗口、原生文件/目录选择、受控 shell 打开、更新和 Go sidecar 生命周期；它不拥有业务表、sql.js、业务规则、长期业务进程或 application service。
- 任一数据库数据集同时只有一个 writer。运行追踪中 `database_open`、`database_write`、`go_application` 与 `go_repository` 必须归属 `go`；`node` 事件一旦显示这些行为即为 blocked。Node/sql.js 不能以只读、fallback、镜像或恢复名义热打开 Go 已接管的数据集。
- 创建 runtime 固定其 session owner。旧 Node 和新 Go session 不因相同 ID、flag、重连、Electron/daemon 重启或错误处理而互相接管；不得把 Node PTY、Chat、Loop、CLI 或数据库句柄转交给 Go，反之亦然。
- 真实 `userData`、真实数据库、活跃 writer、会话材料、token、API key、环境、完整命令、PID、原始输出和未授权路径一律不得作为 fixture、trace、日志、错误或证据。验证器只输出稳定 code、计数、哈希和相对 artifact 名。

## IPC 白名单目标

`ipc-allowlist.json` 中每个允许 channel 都明确方向、调用方、来源校验、schema 校验、能力、输入/输出字节上限和 `secret_policy: deny`。允许类别仅有：

- `native_bridge`：受项目/文件策略约束的 picker、打开文件/目录和经允许的外链；
- `updates`：检查、读取、设置更新状态与启动已验证安装器；
- `go_lifecycle`：有界 sidecar 连接描述和状态。

未知 channel、动态通配 channel、旧业务 channel、未验证 renderer 来源、未验证输入、无界输出及任何秘密默认拒绝，并返回不枚举文件、项目、session 或资源是否存在的稳定错误。`runtime:rendererConfig` 仅能传输有界、非秘密的连接描述；会话材料必须由 P002 的受控私有通道持有，不能写入 URL、命令行、日志或 renderer 可读取对象。

P001 当前仅将旧业务 IPC 列入 `legacy-removal-manifest.json`。`check-ipc-allowlist.js` 的普通模式返回 `baseline_frozen`，表示库存完整但仍观察到已登记的 legacy channel；`--require-thin-shell` 才要求它们全部消失。任何未登记 channel 或将业务 channel 放入 allowlist 都立即 blocked。

## 运行时追踪证据格式

P15 trace 是 fixture 内受限 JSON，顶层只允许 `schema_version`、`kind`、`run_id`、可选 `source_revision` 和 `events`。每个 event 只允许严格递增的 `sequence`、scenario、domain、kind、owner、可选 channel 和有界 byte 计数；不允许 payload、路径、命令、PID、环境、秘密或任意 metadata。

`fixtures/migration/p15/topology/trace-scenarios.json` 固定覆盖：

| 场景 | 必须证明 |
| --- | --- |
| `core_crud` | Projects、Requirements、Feedback、Plans 经 renderer→Go→application→repository，并由 Go 打开/写入数据库。 |
| `loop_task` | Loop、Task、Acceptance、Intake 经 Go application service，进程生命周期受控且写入只属于 Go。 |
| `chat` | Chat/Conversation 使用 Go application service、数据库和专用 SSE。 |
| `scripts_executors` | Scripts/Executors 使用 Go application service、受控进程和 Go writer。 |
| `mcp` | MCP adapter 复用 Go application service，不作为第二业务 owner。 |
| `terminal` | Terminal 使用 Go application service、受控进程与独立 WebSocket。 |

Trace 是对已执行的隔离 E2E/集成命令的脱敏观察，不会自行启动 Electron、Go、数据库、CLI、MCP 或 terminal。缺 trace、缺场景、错误 owner、Node 业务写入、敏感内容、非单调序号和 fixture 外路径均为 blocked；P001 不制造或伪造通过 trace。

## 前置与删除门禁

`check-prerequisites.js` 按顺序接受 P09、P10、P11、P12、P13A、P13B、P14 的不可变 evidence run。每份 run 同时要求成功 summary、不可变 manifest、受限相对 artifact、匹配大小/哈希且无敏感内容；每条记录的命令必须显式 accepted 且退出码为 `0`，缺少命令记录或任一非零均为 blocked。缺失 backend `go.mod`、OpenAPI、HTTP adapter、application 或 repository 目录也是 blocked，绝不能因为目录尚未存在而认定前置已完成。

P005 只能在以下证据都真实通过后断开 renderer 调用、preload 暴露、`ipcMain` handler 和 Node event registration：有序 P09–P14 evidence、P15 静态库存、完整 runtime trace、薄壳 allowlist、唯一 writer、Electron+Go E2E、三平台构建/安装证据和可恢复版本边界。P006 只能在 P005 等价回归及相同门禁复核后删除旧模块和依赖。任何失败保持 legacy 路径与 manifest 不变并报告脱敏 `blocked` 原因；禁止“顺手”删除、局部降级、Node/sql.js fallback 或自动触碰真实 userData。

## 证据可追溯性

后续验证 run 必须关联实际命令、原始退出码、开始/结束时间、平台、源提交或可构建标签、fixture/copy 授权、输入和 artifact SHA-256。P001 只提供这些字段及校验规则，不执行命令、不写证据 run、不发布安装包，也不声明任何平台、签名、notarization 或业务迁移已通过。
