# P00 IPC、renderer 调用与事件能力矩阵

本文件解释机器清单 [`capability-matrix.json`](./capability-matrix.json)。JSON 是唯一受控清单；本文不复制调用能力的全部字段，以避免形成第二份可能漂移的事实源。每个 JSON 条目都明确记录前端 API/preload 名称、IPC channel、handler、业务模块、DTO 与副作用、唯一目标归属、目标接口、迁移阶段、feature flag 和回退路径。

## 盘点边界与结论

盘点源固定为：

- `src/renderer/types.ts` 的 `AutoplanApi`（包括 `fileAccess.get/save` 叶节点）及其 DTO 名称；
- `src/preload.js` 的全部静态值、本地 helper、`ipcRenderer.invoke` 映射和 `ipcRenderer.on` 订阅；
- `src/main.js` 的全部字面量 `ipcMain.handle`、Loop/Chat/配置/更新事件生产者；
- `src/terminal/terminalIpc.js` 与 `terminalTypes.js` 的常量 handler 和四类 Terminal 事件；
- `src/renderer` 非测试源码中的全部直接、换行链式、下标选择和局部类型扩展形式的 `window.autoplan` 使用；
- `src/renderer/hooks/useSnapshot.ts` 对 snapshot/patch 的项目过滤、合并和帧内排序语义。

当前源码基线包含 106 个调用或 preload 本地能力、12 个主进程到 renderer 的事件。93 个当前调用能力最终归 Go API，13 个属于永久 Electron `DesktopBridge`；事件中 11 个归 Go 流式接口，更新状态事件归 `DesktopBridge`。矩阵另预留两个无当前 preload/handler 的 `planned` 行：独立应用版本读取与 Electron 内部 sidecar 生命周期，二者都唯一归 `DesktopBridge`，不会伪装成现有 IPC。所有迁往 Go 的能力在 feature flag 关闭时继续走现有 IPC，因此迁移期间保留可回退路径，但“临时 IPC”不是第二所有者。

清单冻结了三个既有兼容面：

- `reorderPlans` 已由 preload 暴露且存在 `plans:reorder` handler，但未声明在 `AutoplanApi`，renderer 当前也未调用；矩阵以 `api: null`、`renderer: ["not-referenced"]` 显式分类。
- `runTaskBatches` 已由 preload 暴露并映射 `tasks:runParallel`，`WorkspacePage` 通过局部类型扩展调用，但 `AutoplanApi` 尚未声明；矩阵保留该动态调用事实。
- `updates:status` 同时是查询 invoke channel 和状态推送 channel。它们分别占用 `update.status` 与 `event.update-status` 两行，命令所有权和事件所有权互不混用。

其它当前声明或暴露但没有 renderer 调用的能力为 `runOnce`、`mcpStatus`、`readMcpAuthToken`、`aiConfigGet`、`claudeCliConfigGet`。这些均被显式标为 `not-referenced`，不是漏盘；新增未使用入口仍会触发漂移失败。

## 唯一归属

业务能力通过 REST/JSON 命令迁移到 Go application service；快照、增量、Chat 和配置事件通过 SSE；Terminal 输入输出与生命周期通过 WebSocket/配套 REST。原生窗口能力只留在 Electron。

| 能力域 | 当前 handler/模块 | 唯一目标 | 阶段 / feature flag | flag 关闭时回退 |
| --- | --- | --- | --- | --- |
| snapshot、项目与 Intake | `LoopService`、`intakeService`、数据库/附件适配 | Go REST `/v1` | P03 / `go_snapshot_api`、`go_projects_api`、`go_intake_api` | 对应现有 IPC |
| Loop、Plan、Task、Acceptance | `LoopService`、`intakePlanLinks` | Go REST + SSE | P04–P06 / `go_loop_api`、`go_plans_api`、`go_tasks_api`、`go_acceptance_api` | 对应现有 IPC 与 `loop:update/patch` |
| Script、Executor | script application service、`executorStore`、Loop runner | Go REST | P06 / `go_scripts_api`、`go_executors_api` | 对应现有 IPC |
| Chat、Conversation、AI/Claude 配置 | `chatController`、`aiConfigService`、`claudeCliConfigService` | Go REST + SSE | P07 / `go_chat_api`、`go_chat_config_api`、`go_chat_events` | 对应现有 IPC/事件 |
| MCP | MCP server lifecycle、`mcpConfig` | Go REST | P08 / `go_mcp_api` | 对应现有 IPC；token 不进入日志/事件 |
| Terminal | `TerminalService`、`terminalIpc` | Go REST + WebSocket | P09 / `go_terminal_api` | 九个 Terminal IPC 与四个事件 |
| 文件访问策略 | `fileAccess/policy` | Go REST | P02 / `go_file_policy_api` | `file-access:get/save` |
| 目录/文件选择、打开文件夹/文件/外链 | Electron `dialog`、`shell`、`webUtils`、preload URL 转换 | `DesktopBridge` | P12 / `desktop_bridge_v1` | 现有 IPC 或 preload helper |
| 更新检查、下载、安装 | `updateChecker`、Electron `net/shell` | `DesktopBridge` | P12 / `desktop_bridge_updates` | 现有 updates IPC/事件 |

目录或文件选择只返回用户选择结果；打开动作继续由 Electron 执行。Go application service 不接管系统 dialog、系统文件管理器、浏览器或安装器。反过来，`DesktopBridge` 不直接拥有项目、计划、Chat、配置、Terminal 等业务状态。

## 事件生产者、消费者与顺序

| channel | 生产者 | renderer 消费者 | 载荷 | 顺序/终态语义 |
| --- | --- | --- | --- | --- |
| `loop:update` | `LoopService update` → `main.webContents.send` | `useSnapshot.ts` | `AppSnapshot` | 完整快照覆盖同一帧内已排队 patch；下一动画帧提交 |
| `loop:patch` | `LoopService patch` → `main.webContents.send` | `useSnapshot.ts` | `WorkspaceSnapshotPatch` | 按项目帧内合并；若同帧有 snapshot，先应用 snapshot 再应用其后的 patch |
| `updates:status` | `updateChecker` check/dismiss/setAutoCheck | `useUpdateStatus.ts` | 完整 `UpdateStatus` | 到达即整体替换，后到状态胜出 |
| `terminal:data` | `TerminalService DATA` → `safeTerminalEvent` | `useTerminalSessions.ts` | session + `data` | 同一 session 保持 PTY chunk 顺序，先于对应 exit/closed |
| `terminal:exit` | `TerminalService EXIT` | Terminal hook、`WorkspacePage` | session + exitCode/signal | PTY 终态；保留会话仍可随后 closed |
| `terminal:status` | `TerminalService STATUS` | Terminal hook、`WorkspacePage` | 规范化 session | 同一 session 按发出顺序覆盖状态 |
| `terminal:closed` | `TerminalService CLOSED` | Terminal hook、`WorkspacePage` | `TerminalClosedEvent` | 生命周期最终通知，位于相关 exit/status 之后 |
| `chat:chunk` | `ChatController.onEvent` | `useChat.ts` | `{type,data}` | 同一对话按 controller 发出顺序，全部位于该轮 done 之前 |
| `chat:done` | `ChatController.onDone` | `useChat.ts` | status/error/conversationId/title | 每轮一个终态；队列续跑发生在上一轮终态之后 |
| `chat:queue` | `ChatController.onQueue` | `useChatQueue.ts` | 完整 `ChatQueueSnapshot` | 活跃对话按完整快照替换，不跨对话合并 |
| `ai-config:changed` | AI 配置 mutation 与 `chat:saveConfig` | `useChat.ts` | source/configId/完整脱敏 configs | 数据提交后广播，后到完整列表胜出 |
| `claude-cli-config:changed` | Claude 配置 mutation | `WorkspaceSettingsView.tsx` | source/configId/完整脱敏 configs | 数据提交后广播，后到完整列表胜出 |

`useSnapshot` 保留项目隔离：非当前项目的完整快照只更新项目列表；非当前项目 patch 只把项目运行状态合入 `projects`。当前项目 patch 按字段存在性更新 `state/tasks/events/activeOperation(s)/lastOperation`，没有携带的字段不得被清空。

## DTO、副作用与敏感边界

JSON 的 `contract` 使用现有 TypeScript DTO 名称，不在 P00 重命名字段。返回 `AppSnapshot` 的 mutation 继续保持现有兼容行为。`sideEffects` 明确区分数据库/文件写入、进程或 PTY 启停、网络/更新下载、系统打开动作和纯读取。

以下数据即使是现有 DTO 的输入，也不得进入事件、日志或后续 Go API 响应：AI `apiKey`、Claude/MCP token、Terminal `env` 的值以及未经文件策略授权的绝对路径。配置列表和 changed 事件只能携带现有脱敏形态。目录选择、文件选择与拖放路径归 `DesktopBridge`；业务 API 只接收经过统一 realpath/允许根策略校验的路径。

## 漂移检查

`scripts/migration-baseline/inventory-ipc.js` 每次从源码重新提取四个集合并与 JSON 双向比较：

1. `AutoplanApi` 叶成员；
2. preload 暴露叶成员以及 invoke/订阅 channel 映射；
3. `main.js` 与 `terminalIpc.js` 的全部 handler；
4. renderer 非测试源码中的全部 `window.autoplan` 调用（含链式换行、下标选择和局部类型扩展）。

检查还验证能力 id、preload、handler channel 和事件 channel 的唯一性，以及每项必需的 handler、模块、renderer 归属、契约、副作用、唯一 owner、目标、阶段、flag 和回退。新增、删除、改名、缺 handler、缺 preload 映射、renderer 未分类、事件语义不完整或重复归属都会以非零退出码失败。脚本只读上述源码和矩阵，不导入 Electron、不启动应用、不访问 `userData`，也不修改任何业务源码。

直接检查入口为 `node scripts/migration-baseline/inventory-ipc.js`；专项测试文件是 `scripts/migration-baseline/inventory-ipc.test.js`，由 P00 最终统一验收流程执行。本任务开发阶段未运行检查或测试命令。
