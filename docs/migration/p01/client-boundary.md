# P01 Renderer 客户端边界

## 前置与依据

本边界以 P00 `capability-matrix.json` 为硬前置。当前受控清单包含 108 项能力和 12 类事件；每一项均唯一归属 `go-api` 或 `desktop-bridge`，现有 IPC 能力具有对应 preload/handler，计划中的桌面能力则明确标记为 planned。若后续清单出现未分类、重复归属或既有 IPC 缺少映射，P01 必须停止，不能通过扩大客户端或放宽守卫继续迁移。

## 唯一归属

`AutoplanClient` 是 renderer 业务访问的唯一边界，覆盖 P00 的全部 `go-api` 能力：项目与 snapshot、Loop、MCP、计划/任务/验收、Intake、脚本/执行器、Terminal、Chat、AI/Claude 配置、会话和文件访问策略。其签名直接复用 `src/renderer/types.ts` 的现有 DTO；字段命名、snake_case 兼容字段、nullability、Promise rejection、mutation 返回 `AppSnapshot` 以及状态机语义均不在此层转换。

事件边界保持 11 类 `go-api` 订阅：完整 snapshot、增量 patch、Terminal data/exit/status/closed、Chat chunk/done/queue、AI 配置变化和 Claude CLI 配置变化。每次订阅返回 `Unsubscribe`。消费者仍负责项目/会话隔离；完整 snapshot 覆盖排队 patch，Chat chunk 先于 done，Terminal closed 为最终生命周期通知，这些顺序不由适配层重排。

`DesktopBridge` 独占 Electron 原生能力：项目目录、脚本文件和 tasks.json 选择，打开项目目录/工作区文件/外链，拖入文件路径与受控文件 URL，版本与更新，以及未来 sidecar 生命周期。它不得承载 snapshot、数据库、Loop、Intake、计划、任务、验收、自动化执行、Chat、MCP、Terminal 或配置业务调用。当前 `updates:status` 事件也归 DesktopBridge，而不是 AutoplanClient。

## Provider 与 transport

renderer 根部的 `AutoplanProvider` 同时注入客户端和桌面桥，业务组件和 Hook 分别通过 `useAutoplanClient`、`useDesktopBridge` 获取依赖。两个默认实例都在模块加载期创建一次；React StrictMode 的挂载、卸载和再次挂载不会重建 transport/bridge，也不会由 Provider 注册全局监听器。显式依赖注入仅用于相应的契约测试。

全应用的 transport 配置来源只有 `VITE_AUTOPLAN_TRANSPORT`。P01 唯一可用值和默认值均为 `ipc`；配置缺失、为空、关闭、非法或要求回退时统一选择完整的 `IpcAutoplanClient`。本阶段不实现 HTTP、SSE 或 WebSocket，也不允许组件按领域增加 feature flag。未来 Go transport 必须扩展同一 resolver 并实现相同的 `AutoplanClient`，不能改变组件依赖面。

## IPC 回退与阶段限制

`IpcAutoplanClient` 是业务层访问既有 `window.autoplan` 的唯一兼容适配器；DesktopBridge 的 IPC 实现是原生能力访问该全局对象的另一条独立白名单。回退只切换实现，不改变 DTO、事件载荷、错误或到达顺序。

P001 仅建立契约、事件、Provider、单一配置和边界说明，不迁移 UI 或状态管理，不修改 preload、主进程 handler、Node 业务实现、数据库或 Electron `userData`，也不启动 Go sidecar。IPC 客户端与 DesktopBridge 的具体适配、调用方迁移和验证门禁由 P01 后续任务分别完成。
