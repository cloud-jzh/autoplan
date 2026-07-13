# ADR-0001：本机 Go sidecar 与 Electron 薄壳边界

- 状态：Accepted
- 日期：2026-07-11
- 决策范围：P00–P15 后端分离
- 关联基线：[能力矩阵](../capability-matrix.md)、[依赖审计](../database-and-dependencies.md)、[ADR-0003](./0003-rest-sse-websocket-and-application-service.md)、[ADR-0004](./0004-contract-files-flags-and-rollback.md)

## 背景

当前应用把窗口、IPC、业务编排、sql.js 持久化、文件访问、Agent CLI、MCP、Chat、Terminal 和更新能力放在同一个 Electron/Node 进程边界内。能力矩阵已经把 104 项业务能力归为目标 Go API，把原生选择/打开、更新以及应用生命周期等 16 项能力归为永久 `DesktopBridge`。迁移必须保持现有 renderer DTO、snapshot、事件和副作用，不能把旧 IPC 与新接口变成两个长期业务所有者。

## 决策

采用由 Electron 主进程启动和监管的本机 Go sidecar。Go 后端保持单进程“模块化单体”，按业务域划分 package 和依赖方向，不在本阶段拆成多个本机服务或微服务。

### 网络与进程边界

1. sidecar 只能绑定 IPv4 loopback `127.0.0.1`。禁止绑定 `0.0.0.0`、局域网地址、公开网卡或仅以 `localhost` 名称代替明确地址；IPv6 若未来启用，必须另作决策并具备同等 Origin/会话约束。
2. Electron 为每个应用实例启动一个 sidecar，并使用操作系统分配的空闲端口。端口和一次性会话材料经受控父子进程通道传递，不写入固定配置、命令行、URL query、日志或数据库。
3. 就绪握手至少校验 sidecar 进程身份、协议版本、契约版本、数据库所有权状态和健康状态。握手失败时不得把 renderer 路由到半初始化 sidecar。
4. sidecar 生命周期从属于 Electron 应用实例：正常退出先停止接收新命令、排空或取消有界任务、关闭数据库和监听器，再由 Electron 等待退出；超过时限才终止子进程。崩溃重启不得绕过单 writer 门禁。
5. 仅监听 loopback 不构成授权。所有 HTTP、SSE 和 WebSocket 连接仍执行 ADR-0004 的会话、Origin、项目和文件边界校验。

### Electron 仅保留的职责

Electron 是薄壳和 `DesktopBridge`，只负责：

- 窗口、托盘、单实例、应用启动/退出以及 renderer 生命周期；
- sidecar 二进制定位、启动、就绪探测、版本匹配、停止和崩溃状态展示；
- 系统原生目录/文件选择、拖放路径转换、打开文件/目录/外链；
- 应用版本读取、更新检查/下载、安装器启动和应用重启安装；
- renderer 到 loopback transport 的最小会话引导，不解释或改写业务状态。

Electron 不拥有 Project、Intake、Plan、Task、Acceptance、Loop、Script、Executor、Chat、配置、MCP 或 Terminal 业务规则，不直接访问这些域的表，也不成为 Go application service 的第二实现。迁移期间旧 IPC 只是 feature flag 关闭时的临时兼容路径，其所有权仍由能力矩阵唯一确定。

### Go 模块化单体

Go sidecar 内部采用以下单向依赖：

```text
HTTP / SSE / WebSocket / MCP adapters
                 ↓
         application services
                 ↓
       domain policy and workflow
                 ↓
 database / file / process / PTY adapters
```

- transport adapter 只做认证、解码、输入边界和 DTO 映射；不包含业务分支。
- application service 是 UI、HTTP、MCP 和后台调度的共同入口。
- domain 层不依赖 Electron、HTTP、SQLite driver 或具体 CLI。
- 基础设施 adapter 实现数据库、工作区文件、进程、网络和 PTY；不得反向调用 transport。

## 启动与降级

在首笔 Go 正式写入之前，`go_sidecar_enabled` 关闭时可继续使用既有 Node IPC；sidecar 启动或版本握手失败时保持该 flag 关闭并报告可诊断状态。在首笔 Go 正式写入之后，sidecar 不可用属于服务故障，不能通过让旧 Node/sql.js 热打开生产库来“降级”；恢复必须遵守 ADR-0002。

renderer 不直接猜测端口或重试另一个实例。Electron 只在已认证且版本匹配的 sidecar 就绪后发布连接信息。sidecar 意外退出时，renderer 的新命令失败为明确不可用状态，流连接断开并在同一实例恢复后重新同步完整 snapshot。

## 不采纳的方案

- **在 Electron 主进程内嵌 Go 共享库**：生命周期、崩溃和发布边界耦合，无法形成清晰 transport 与单 writer 门禁。
- **让 renderer 直接启动或发现 sidecar**：renderer 不应拥有进程、密钥或端口生命周期。
- **监听局域网以便远程访问**：扩大攻击面，超出本机桌面应用授权模型。
- **按业务域启动多个 sidecar**：会提前引入跨进程事务、端口和版本协调，不符合模块化单体方向。

## 结果与约束

正面结果是业务后端可独立测试和演进，Electron 原生能力边界明确，UI/MCP/transport 可复用同一服务。代价是需要管理子进程、协议版本、流重连、安装包内多平台二进制和健康诊断。

任何后续实现若让 Electron 新增业务表访问、让 Go 调用 Electron 原生 UI、让 sidecar 监听非 loopback 地址，或为同一业务保留两个长期实现，都必须先用新 ADR 取代本决策。
