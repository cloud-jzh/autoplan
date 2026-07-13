# P00→P15 Go 后端迁移依赖路线图

本路线图把来源规划稿的八个阶段细化为十六个可独立验收的阶段。依赖只允许从左向右；阶段编号不是并行工作流标签。后续阶段可以提前做不产生副作用的设计实验，但不能启用 flag、写正式数据或宣称通过。

## 单向依赖图

```text
P00 基线冻结
  → P01 客户端适配层 + sidecar 生命周期骨架
  → P02 Go 契约/transport/会话/文件策略骨架
  → P03 持久化基础 + Project/Intake/Snapshot
  → P04 Loop/Operation/SSE
  → P05 Plan 与 Intake-Plan 链接
  → P06 Task/Acceptance/Script/Executor
  → P07 Chat/Conversation/配置
  → P08 MCP 共享 application service
  → P09 Terminal REST + WebSocket + PTY
  → P10 最终 schema/迁移工具/原子切换演练
  → P11 [G1 真实 userData 切换] → [G2 首笔 Go 正式写入]
  → P12 DesktopBridge 与 Electron 薄壳
  → P13 全域观测、打包与恢复加固
  → P14 [G3 删除旧 Node writer]
  → P15 遗留实现/红灯最终处置与迁移闭环
```

P00–P10 的 Go 数据和副作用验证仅使用脱敏 fixture、系统临时目录或一致性显式副本。P11 之前生产库 owner 始终是 Node/sql.js；P11 的 G2 之后生产库 owner 始终是 Go。不存在两者同写同库的中间状态。

## 阶段状态与通用门禁

每个阶段只有 `planned`、`in_progress`、`blocked`、`passed` 四种状态。只有全部前置阶段 `passed`、本阶段退出条件有实际证据、回退点可用时才能标记 `passed`。等待批准、未运行命令、环境失败、安全条件不满足或证据缺失一律是 `blocked`。

每阶段必须按 [证据规范](./evidence/readme.md) 提供：代码/文档、自动化测试、实际命令及真实退出码、原始日志与哈希、受影响文件、数据/进程证据、剩余风险、负责人和回退点。禁止 skip/only、放宽断言、吞掉退出码、自动学习红灯或用汇总成功遮蔽子命令失败。

## 阶段定义

### P00：冻结现状与架构决策

- **前置**：来源规划稿及持久化附件 SHA-256 可追溯；不读取附件中的真实 userData。
- **交付物**：[P00 索引](./readme.md)中的能力矩阵、契约、依赖审计、五类 fixture、命令/红灯基线、四份 ADR、证据规范和本路线图。
- **feature flag**：全部 Go flag 默认关闭；`DesktopBridge` 只记录目标归属，不改变运行路径。
- **退出条件**：`npm.cmd run migration:p00:verify` 产生完整证据；专项测试、build、安全 smoke 通过，check/test 只包含精确冻结集合。
- **回退点**：仅文档和只读盘点，无运行时切换；删除 P00 变更即可回到原实现，但必须保留审计记录。
- **禁止跨越**：不得改业务行为、真实 userData、renderer 状态模型或数据库 owner；P00 未通过时 P01=`blocked`。

### P01：前端适配层与 sidecar 生命周期骨架

- **前置**：P00 passed；能力矩阵中所有 renderer/preload/IPC 调用有唯一 owner。
- **交付物**：`AutoplanClient` 接口、`IpcAutoplanClient`、`DesktopBridge`、依赖注入入口；Electron 启停 sidecar 的最小骨架、版本握手和健康状态，组件不再新增直接 IPC。
- **feature flag**：`go_sidecar_enabled` 仅用于开发健康握手；业务调用仍走 IPC adapter。
- **退出条件**：IPC adapter 下现有行为不变；sidecar 可被 Electron 在 `127.0.0.1` 独立启动、ready、停止和故障展示；安装包/开发模式均不泄露端口或会话。
- **回退点**：关闭 `go_sidecar_enabled` 并停止无状态 sidecar，全部业务继续走 `IpcAutoplanClient`。
- **禁止跨越**：sidecar 不打开数据库、不接收业务 mutation；renderer 不直接发现端口或启动进程；Electron 不新增业务规则。

### P02：Go 契约、transport、安全与文件策略骨架

- **前置**：P01 passed；客户端 seam 和 sidecar 生命周期稳定。
- **交付物**：Go module/模块化单体目录，配置、结构化脱敏日志、`healthz/readyz`、优雅关闭；OpenAPI/JSON 契约、统一错误 adapter；REST/SSE/WebSocket 骨架，本机会话、Origin、realpath/allowed-root 策略。
- **feature flag**：`go_meta_api`、`go_file_policy_api` 可在临时目录 limited；其他域 flag 关闭。
- **退出条件**：契约生成/对比、安全和路径测试通过；非 loopback bind、无会话/错误 Origin、符号链接/越界路径、secret 日志均被拒绝。
- **回退点**：关闭两个 flag，销毁本次内存会话和临时根；旧 IPC/文件策略仍是生产路径。
- **禁止跨越**：不能把 localhost 当授权、使用 CORS `*`、接触生产库或把 DesktopBridge 原生动作迁入 Go。

### P03：持久化基础、Project/Intake/Snapshot

- **前置**：P02 passed；契约、文件策略和测试 fixture 可用。
- **交付物**：Go repository/transaction/migration 基础；Projects、settings、project state、Requirements、Feedback、Attachments、兼容 Snapshot/patch application service 和 REST adapter；黄金 snapshot 对比。
- **feature flag**：`go_projects_api`、`go_intake_api`、`go_snapshot_api` 仅对 fixture/一致性副本 shadow 或 limited。
- **退出条件**：空库、旧库、孤儿、异常路径、大数据量 fixture 的 schema/backfill/CRUD/snapshot 对比通过；排序、null、ID、时间和附件副作用一致。
- **回退点**：关闭域 flag，丢弃临时库/副本；Node 仍是生产唯一 writer。
- **禁止跨越**：不得对真实 userData 写入、把跨项目/orphan 数据静默修复，或让 Go shadow 与 Node 共享活动数据库文件。

### P04：Loop、Operation 与 SSE

- **前置**：P03 passed；Project/state/snapshot repository 和事件 publisher 已稳定。
- **交付物**：Operation 模型、Loop scheduler、取消/超时/恢复、Agent CLI process adapter、`loop:update/patch` 与 Operation SSE；状态机和进程树测试。
- **feature flag**：`go_loop_api`、`go_loop_events`，只在临时 workspace、fake CLI 和副本数据上 limited。
- **退出条件**：start→运行→取消/失败/完成/重启恢复，snapshot 先于 patch、operation 顺序、timeout 和进程树清理全部通过。
- **回退点**：停止 Go 创建的 Operation/进程，关闭事件和 API flag，重新读取 Node 完整 snapshot。
- **禁止跨越**：活动 Operation 不跨 runtime 接管；不能使用真实 CLI 凭据、真实 workspace 或让 Node/Go 同时调度同一项目。

### P05：Plan 与 Intake-Plan 链接

- **前置**：P04 passed；Operation、Intake、文件策略和事件顺序可复用。
- **交付物**：Plans、plan file/spec、Intake-Plan links、计划生成/读取/删除 application service，兼容排序、状态和文件副作用。
- **feature flag**：`go_plans_api`、`go_intake_plans_api`，默认 shadow/fixture limited。
- **退出条件**：计划生成、draft/正式状态、链接、删除级联/孤儿保留策略、Markdown/manifest 文件及 snapshot 黄金对比通过。
- **回退点**：停止 Go 计划 Operation，关闭 flag 并丢弃测试副本；不得把已由 Go 修改的副本交给旧 Node 当正式库。
- **禁止跨越**：Plan DB 行和文件不能分别由不同 runtime 拥有；失败不得留下未分类链接或覆盖用户文件。

### P06：Task、Acceptance、Script 与 Executor

- **前置**：P05 passed；Plan/Operation 和进程 adapter 可用。
- **交付物**：Task 生命周期、Acceptance 单项/批量预校验、Script hooks、Executor 一次性/plugin process manager、日志上限和重启恢复。
- **feature flag**：`go_tasks_api`、`go_acceptance_api`、`go_scripts_api`、`go_executors_api`。
- **退出条件**：任务行先于事件、失败/取消恢复、验收状态机、批量原子预校验、plugin 重复启动拒绝、stop/归档与 snapshot live-state 校正通过。
- **回退点**：每个进程留在创建它的 runtime 到终态；关闭新路由 flag，清理仅由 Go 创建的临时进程和测试数据。
- **禁止跨越**：不能跨 runtime kill/接管句柄，不能并行执行同一 Task，脚本/Executor cwd 必须经过文件策略。

### P07：Chat、Conversation 与 AI/Claude 配置

- **前置**：P06 passed；Operation、队列恢复、项目隔离和 secret mapper 可复用。
- **交付物**：Conversation/message repository、FIFO/abort/restart、Chat tools application service、AI/Claude 配置 CRUD 和脱敏事件、SSE chunk/done/queue/title/config。
- **feature flag**：`go_chat_api`、`go_chat_config_api`、`go_chat_events` 作为一个相容切换单元。
- **退出条件**：发送/排队/停止/清理、重启恢复、当前 finish/pump/done 顺序、跨项目隔离、tool 文件授权和 masked/has-secret 对比通过。
- **回退点**：已开始的 turn 留在原 runtime 到 done/abort；关闭新请求路由并从权威完整状态恢复，不重放非幂等发送。
- **禁止跨越**：密钥/token/正文/tool data 不进入日志或广播；Go 写过的 conversation 不能交给 Node writer 热开。

### P08：MCP 复用 application service

- **前置**：P07 passed；目标业务域 service、会话、项目授权和文件策略齐备。
- **交付物**：Go MCP stdio/localhost adapter、工具注册和配置；MCP/UI/HTTP 调用同一 application service、错误和审计策略。
- **feature flag**：`go_mcp_api`。
- **退出条件**：所有现有 MCP tools 契约一致；同一输入经 UI/MCP 得到同一授权、状态转换和副作用；token 不进日志/事件。
- **回退点**：停止 Go MCP listener/stdio，关闭 flag；仅在 Node owner 阶段恢复旧 MCP adapter。
- **禁止跨越**：MCP 不直接访问 repository/文件/进程，不因本机 transport 绕过认证，也不成为隐藏管理 API。

### P09：Terminal REST、WebSocket 与 PTY

- **前置**：P08 passed；会话/Origin、文件策略、进程生命周期和 transport 背压可用。
- **交付物**：Terminal create/list/close REST、双向 WebSocket、跨平台 PTY adapter、scrollback/replay、resize/kill 和四类事件兼容层。
- **feature flag**：`go_terminal_api`，REST 控制面和 WebSocket 必须一起切换。
- **退出条件**：Windows/macOS/Linux（可用目标平台矩阵）会话、input/data、resize、data→exit/status→closed、断线 replay、背压和非法 cwd 测试通过。
- **回退点**：现有 session 留在创建 runtime，停止接受 Go 新 session；待全部终态后关闭 flag。
- **禁止跨越**：禁止跨 runtime 接管 PTY、把 session ID 当授权、通过 SSE 传 Terminal 数据或广播 env 值。

### P10：最终持久化、迁移工具与原子切换演练

- **前置**：P03–P09 全部 passed；所有会写业务域已有 Go application service，不再缺少“切库后仍需 Node 写”的能力。
- **交付物**：最终 schema v1、全量 ensure/backfill 等价迁移、orphan/异常路径审计、数据库 owner/排他锁、备份/恢复工具和 G1/G2 操作手册；在一致性副本上的完整切换演练。
- **feature flag**：全部 Go 域 flag 仅在副本环境组合演练；生产 flag 不变。
- **退出条件**：五类 fixture 和正式数据一致性副本完成迁移、行数/hash/schema/snapshot/附件/Plan 文件复验；模拟 Node close→Go owner→回滚及双 writer 阻断通过。
- **回退点**：停止副本 Go owner，保留证据并丢弃演练副本；生产 Node owner 未改变。
- **禁止跨越**：不得把演练成功等同真实切换批准，不得读取活动 userData、跳过备份恢复或提前接受正式写入。

### P11：真实 userData 切换与首笔 Go 正式写入

- **前置**：P10 passed；G1 四类负责人确认、维护窗口、一致性备份/恢复、完整性和单实例证据齐备。
- **交付物**：G1 Node quiesce/close 和 Go 排他 owner 证据；切换后只读校验；G2 首笔正式 mutation 的前后快照、行数、operation、文件副作用和 owner 证据。
- **feature flag**：`go_sidecar_enabled` 与所有数据库业务域 flag 按操作手册在固定维护窗口启用；旧路由不能再直接访问数据库。
- **退出条件**：G1 与 G2 分别签署通过；Go 是唯一 writer，关键流程/snapshot/事件正常，Node 无数据库句柄，兼容 Go 回退版本可部署。
- **回退点**：G2 前可停止 Go、证明库未写并恢复 Node；G2 后只能回滚兼容 Go、前向修复或受控恢复备份。
- **禁止跨越**：不得合并 G1/G2 记录、边运行边切换、双 writer，或在首笔 Go 写后翻 flag 让 Node 热开现库。

### P12：DesktopBridge 与 Electron 薄壳

- **前置**：P11 passed；所有业务数据库访问和业务进程编排由 Go owner 提供。
- **交付物**：Electron 仅保留窗口/应用生命周期、sidecar 启停、目录/文件选择与系统打开、版本/更新/安装；renderer 不再新增直接 IPC，业务调用经 client/Go adapter。
- **feature flag**：`desktop_bridge_v1`、`desktop_bridge_updates`；所有 Go 业务 flag 保持开启。
- **退出条件**：能力矩阵证明 Electron 无业务表/service owner；native dialog/shell/update/installer 行为与错误兼容，sidecar 崩溃/更新/退出流程通过。
- **回退点**：可回滚 Electron shell/bridge 版本，但数据库仍由 Go；旧 IPC 兼容 adapter 只能调用 Go，不得恢复 Node writer。
- **禁止跨越**：不能把系统 dialog/安装器放进 Go，也不能以“兼容 IPC”为名在 Electron 重新实现业务逻辑。

### P13：全域观测、跨平台打包与恢复加固

- **前置**：P12 passed；生产路径全部经 Go，DesktopBridge 边界稳定。
- **交付物**：按 ADR-0004 的域观测窗口、指标/告警、冷启动与 crash loop、SSE/WS 重连、安装升级、备份恢复、上一版 Go 回滚和前向修复证据。
- **feature flag**：生产 Go 域 flag 保持 on；旧路由只做零使用量观测，不具备数据库写能力。
- **退出条件**：各域时间和样本量均达标，无未处置契约/数据/安全告警；支持平台的开发、打包、升级、恢复矩阵通过。
- **回退点**：回滚到契约/schema 兼容的上一版 Go 和 Electron shell，或前向修复；数据库 owner 不变。
- **禁止跨越**：样本不足不能用合成次数代替；安全、秘密、路径越界、双 writer 告警不能在 limited 状态继续观察。

### P14：删除旧 Node writer

- **前置**：P13 passed；G3 的全域观测、零旧路由使用、源码/依赖审计、恢复演练、owner 确认和 P15 红灯处置计划齐备。
- **交付物**：删除 `AppDatabase` 生产初始化、Node schema/backfill/persist 和所有直接业务 SQL 写路径；打包物证明只有 Go writer；保留历史实现审计标签和 Go 恢复 runbook。
- **feature flag**：数据库 owner 不再可由 flag 选择；旧业务 flag 回退不能指向 Node 数据访问。
- **退出条件**：G3 签署通过；冷启动、更新、崩溃恢复和安装包扫描均无第二 writer，备份恢复后完整验收通过。
- **回退点**：仅回滚 Go 二进制、前向修复或恢复 Go 备份；不能从历史包恢复 Node/sql.js 热开现库。
- **禁止跨越**：G3 前不得删 writer；G3 后不得以故障应急重新复制旧 writer、旧 migration 或直接 SQL 到 Electron。

### P15：遗留实现、临时 IPC 与红灯最终处置

- **前置**：P14 passed；Node writer 已删除且 Go/桌面边界稳定。
- **交付物**：删除无使用量的临时业务 IPC、旧 client adapter 和死依赖，保留永久 DesktopBridge；最终能力/契约/依赖清单、发布/恢复文档和红灯处置台账。
- **feature flag**：移除不再可回退的旧业务 flag；保留必要发布 kill switch 时，其回退目标必须是兼容 Go 版本而非 Node writer。
- **退出条件**：`npm.cmd run check` 与 `npm.cmd test` 冻结失败全部清零；若确实不能清零，每项必须有负责人、理由、风险、明确到期日和批准记录。build、跨平台 smoke/e2e、迁移/恢复和安全验收全部通过。
- **回退点**：代码回滚仅限不恢复已删除 writer/不违反 schema 的 Go 或 shell 版本；数据恢复遵循 Go 备份流程。
- **禁止跨越**：不允许静默继承红灯、永久 skip/only、放宽断言、删除漂移检查，或把临时 IPC/Node writer 留作未记录后门。

## 三个高风险门禁责任与证据

| 角色 | G1 真实 userData 切换 | G2 首笔 Go 写入 | G3 删除 Node writer |
| --- | --- | --- | --- |
| Migration Owner | 切换步骤、Node close、Go 排他锁 | flag/路由和首笔 operation | 源码/依赖无第二 writer |
| Data Owner | 备份、schema、行数、hash、orphan、恢复 | 写前后数据/snapshot/文件副作用 | Go 备份恢复与数据完整性 |
| Release Owner | 维护窗口、版本、单实例、失败回包 | 兼容 Go 回退版本可部署 | 打包/升级/冷启动无旧 writer |
| Security Owner | userData/秘密不进证据，会话/路径边界 | Origin/session/secret/file policy | 删除后恢复不绕过安全边界 |

门禁证据的详细字段见 [evidence/readme.md](./evidence/readme.md)。四类 owner 的确认是职责记录，不是替代自动化证据。

### G1 失败回滚

在 Go 接受正式写入前停止 Go、关闭句柄并验证主库与受控切换状态一致，释放 owner 后才能恢复 Node。备份不可恢复、完整性/行数/hash 漂移、活动任务未排空或单实例无法证明时保持 `blocked`。

### G2 不可逆点

首笔 Go 正式写入后，数据所有权不可直接回到 Node。故障时冻结新 mutation、保全现场，优先回滚兼容 Go 或前向修复；恢复切换前备份需要记录被截断写入和负责人决定。feature flag 不改变这一事实。

### G3 不可逆点

删除 Node writer 后，恢复仅依赖 Go 二进制、前向修复或 Go 备份。旧源码/安装包只作审计参考，不能作为可执行恢复方案。缺少全域观测、零旧路由、恢复演练或 P15 红灯计划时不得进入 G3。

## 来源规划稿映射

| 来源阶段 | 细化阶段 |
| --- | --- |
| 阶段 0：运行模式与冻结范围 | P00 |
| 阶段 1：前端 API 适配层 | P01 |
| 阶段 2：契约与 Go 骨架 | P01–P02 |
| 阶段 3：持久化领域 | P03、P05–P07、P10 |
| 阶段 4：原子切换数据库所有权 | P10–P11 |
| 阶段 5：Loop 与长任务 | P04、P06 |
| 阶段 6：Chat、MCP、Terminal | P07–P09 |
| 阶段 7：Electron 收缩与旧实现清理 | P12–P15 |

该映射允许先在 fixture/副本上完成各域代码，但不允许在 P11 前把其生产写入 flag 打开。任何需要改变阶段依赖、合并高风险门禁或重新引入第二 writer 的提案必须先新增 ADR 并更新能力矩阵、证据规范和本路线图。
