# ADR-0004：兼容契约、文件安全、功能开关与回滚门禁

- 状态：Accepted
- 日期：2026-07-11
- 决策范围：Go 迁移兼容性、安全边界、按域放量和不可逆点
- 关联基线：[能力矩阵](../capability-matrix.md)、[契约基线](../contract-baseline.md)、[依赖审计](../database-and-dependencies.md)、[P00 基线结果](../baseline-results.md)、[ADR-0002](./0002-single-database-writer.md)

## 背景

迁移的目标是替换后端归属，不是重写 renderer 状态模型或借机改变业务行为。现有契约包含 snake_case 数据库 DTO、部分兼容双命名、完整 snapshot、字段存在性 patch、状态机、错误文本/结构、事件顺序和跨数据库/文件/进程的副作用。与此同时，本机 HTTP 并不天然可信，工作区路径、密钥、token、env_vars 和用户内容需要统一边界。

## 决策一：兼容契约是门禁

`contract-baseline.json` 是迁移期间的机器权威。Go API、SSE/WebSocket adapter 和 Electron 兼容层必须保持：

- 既有字段名、snake_case、公开的 camelCase/snake_case 兼容字段、可选与 `null` 的区别；
- AppSnapshot 的完整键、空值、排序、整数 ID、Terminal 字符串 session ID 和 UTC ISO-8601 时间；
- mutation 返回 AppSnapshot、Chat `{accepted|stopped|cleared,error}`、Terminal `{ok,code,message,details?}` 等现有结果形态；
- Loop/Plan/Task/Chat/Terminal 状态机、snapshot/patch 合并、事件顺序、默认值和恢复行为；
- 数据库写、附件/Plan 文件、CLI/进程/PTY、网络与 DesktopBridge 等成功和失败副作用。

Go 可以在不改变现有 message/DTO 的前提下附加稳定错误 code、trace ID 或 transport metadata，但 renderer adapter 不依赖这些新增字段完成旧行为。任何字段删除/改名、排序、事件顺序或副作用变化必须先版本化契约并显式迁移，不能由 feature flag 静默引入。

## 决策二：工作区与文件边界默认拒绝

### 授权根

1. 授权根只能来自用户通过 `DesktopBridge` 原生选择、已存在项目配置的受控迁移，或系统临时 fixture 根。renderer/MCP/HTTP 直接提交的任意绝对路径不能自行成为授权根。
2. sidecar 保存规范化根及其项目归属。所有文件读写、附件、Plan、Script/Executor cwd、Terminal cwd 和工具调用先绑定项目，再匹配该项目允许根。
3. 不确定项目、空根、相对基准不明、跨项目引用或策略服务不可用时一律拒绝。

### realpath 与路径比较

- 对已存在目标解析 `realpath`；对待创建目标解析最近存在父目录的 `realpath`，再逐段验证新名称。
- 拒绝越过允许根的 `..`、符号链接、junction/reparse point、设备路径、命名管道和非预期 UNC/网络共享。若明确支持 UNC，必须作为单独授权根并采用 Windows 大小写/分隔符语义比较。
- Windows 盘符和大小写、尾随点/空格、短文件名等必须使用规范化后的组件边界比较，禁止字符串前缀判断（例如根 `C:\work` 不能授权 `C:\workspace-escape`）。
- 校验与打开之间重新确认目标/父目录身份；敏感写入优先基于已验证目录句柄并使用不跟随链接的方式，防止 TOCTOU 替换。
- 返回 UI 的路径按既有 DTO 最小化；日志、事件和错误优先使用项目相对路径或短标识，不回显未授权绝对路径。

DesktopBridge 只负责选择和系统打开动作，不替代 Go 文件策略。打开文件/目录/外链前仍校验协议、允许根和存在性；校验失败不得发生系统副作用。

## 决策三：localhost 会话、Origin 与敏感数据

- Electron 每次启动 sidecar 生成高熵、短生命周期会话材料，仅存内存并通过受控父子/renderer 引导通道传递。禁止写入 argv、URL query、日志、事件、数据库或 fixture。
- 所有 REST/SSE/WebSocket 请求都验证会话、用途、过期时间和精确 Origin。允许 Origin 是当前打包 renderer 的受控 origin 以及显式开发 origin；缺失、`null`、通配、浏览器任意 localhost 页面和跨站 Origin 默认拒绝。
- CORS 不使用 `*`，也不因源地址是 127.0.0.1 而跳过认证。会话 ID、process ID、project ID、Terminal session ID 都不能单独作为授权。
- API、日志、事件和 fixture 禁止包含可用密钥、AI `api_key`、Claude/MCP token、Terminal `env` 值、`project_states.env_vars` 值、私钥、认证 header、带凭据 query 或未授权绝对路径。
- 列表、snapshot 和广播只返回 `has*`、masked suffix 等现有脱敏形态。`readMcpAuthToken` 的既有明文读取是用户触发、点对点、不可缓存/广播/记录的唯一冻结例外；扩展明文读取必须新决策。
- 错误和 trace 在进入日志前结构化脱敏；用户正文、Chat/tool data、脚本输出和外部 CLI stderr 不复制到基础设施日志。无法确认是否安全时丢弃字段而不是尝试“最佳努力”输出。

## 决策四：按域 feature flag

所有 Go flag 默认关闭，配置属于本机受控迁移状态，不由远端内容、项目文件或 renderer 任意修改。`go_sidecar_enabled` 是主开关；域开关只有在 sidecar 健康、契约匹配和 ADR-0002 writer 状态允许时才有效。

| 域 | 受控 flags | 开启约束 | flag 关闭时 |
| --- | --- | --- | --- |
| 基础/读取 | `go_meta_api`、`go_file_policy_api`、`go_projects_api`、`go_snapshot_api` | 先完成只读 shadow、路径策略与 snapshot diff | 首笔 Go 写前走既有 IPC；写后只能走 Go 兼容 adapter |
| Intake/Plan/Task | `go_intake_api`、`go_intake_plans_api`、`go_plans_api`、`go_tasks_api`、`go_acceptance_api` | 同一事务/副作用链涉及的读写与事件一起切换 | 不允许拆成 Node 写 + Go 读同一活动库 |
| Loop/事件 | `go_loop_api`、`go_loop_events` | API、snapshot/patch publisher、Operation 恢复共同验收 | 关闭事件时必须重新同步完整 snapshot |
| Script/Executor | `go_scripts_api`、`go_executors_api` | 进程注册、停止、日志上限和恢复证据齐备 | 不接管仍由另一 runtime 启动的进程 |
| Chat/配置 | `go_chat_api`、`go_chat_config_api`、`go_chat_events` | 消息写入、FIFO、abort、done 顺序和 secret masking 共同验收 | 不把 Go 写入后的 conversation 交给 Node writer |
| MCP | `go_mcp_api` | 与 UI 共用 application service、项目授权和文件策略 | 旧 MCP 只在 Node owner 阶段使用 |
| Terminal | `go_terminal_api` | REST 控制面、WebSocket、PTY 顺序/replay 一起切换 | 现存 session 留在创建它的 runtime，禁止跨 runtime 接管句柄 |
| 永久桌面桥 | `desktop_bridge_v1`、`desktop_bridge_updates` | 始终由 Electron 实现，不是 Go 放量项 | 保留现有原生 IPC/bridge |

依赖顺序是 file policy/session → 项目/snapshot → 具体业务域 → 该域流事件。一个用户操作跨多个域时，路由在提交前固定，不能执行到一半翻 flag。运行中的 Operation、Chat turn、plugin 或 Terminal session 留在创建它的 runtime 直到终态；新请求才使用新路由。

## 启用证据与观测窗口

每个域从 `off` 进入 shadow/limited/on 前必须具备 P00 清单无漂移、专项测试通过、冻结红灯集合无新增、脱敏 fixture 通过、契约/snapshot diff、错误与事件顺序指标、回滚演练和明确 owner。shadow 不得产生正式写副作用。

最低观测窗口如下；“时间”和“样本数”两项都满足后才能移除该域旧路由：

| 域 | 最低窗口与样本 |
| --- | --- |
| 只读/file policy/snapshot | 48 小时、3 次冷启动，且所有 shadow diff 可解释 |
| Intake/Plan/Task/Acceptance CRUD | 7 天、50 次成功 mutation，覆盖创建/更新/删除/失败恢复 |
| Loop/Script/Executor | 7 天、20 个完整 start→终态→重启恢复周期，含取消和 timeout |
| Chat/配置/MCP | 7 天、20 个完整 turn/队列周期，含 abort、重启恢复和脱敏检查 |
| Terminal | 7 天、20 个 session，覆盖 data→exit/status→closed、resize、kill 和 replay |
| Go 全库 writer | 14 天、5 次冷启动、一次备份恢复演练，之后才可评估删除 Node writer |

若真实使用量不足，延长窗口，不用合成成功次数替代生产观测。指标只记录计数、code、耗时和短 ID，不记录秘密、正文、环境值或绝对路径。

## 回滚触发条件

以下任一项立即停止该域放量并进入 blocked/rollback：

- DTO、nullability、排序、snapshot/patch、状态机、错误或事件顺序与基线不一致；
- 数据库行数/hash/关系意外变化，重复/丢失 mutation，跨项目数据或双 writer 迹象；
- realpath/Origin/session 默认拒绝失效，出现越界文件访问、未授权 API 或秘密泄露；
- sidecar crash loop、连接恢复失败、SSE gap 无法重同步、Terminal 乱序/重复 PTY；
- 失败率或耗时超过该域发布前阈值，且无法在当前观测窗口解释；
- 备份、恢复、审计证据缺失，或 operator 无法确定当前 writer owner。

安全、秘密、路径越界和双 writer 触发器不允许继续 limited rollout。

## 三个高风险门禁

| 门禁 | 进入条件 | 退出/恢复性质 |
| --- | --- | --- |
| 真实 Electron `userData` 所有权切换 | Node writer 已排空关闭，一致性备份及隔离恢复通过，Go 排他所有权可证明 | 首笔 Go 正式写入前尚可按 ADR-0002 退回；不能边运行边切换 |
| 首笔 Go 正式写入 | 契约、schema、snapshot、文件副作用和 writer 证据全部通过 | 数据所有权不可逆，禁止直接切回 Node writer |
| 删除旧 Node writer | 全域观测、零回退使用、恢复演练、证据与 P15 红灯处置全部满足 | 工程不可逆；恢复只依赖 Go 二进制/前向修复/Go 备份 |

三个门禁都必须记录进入时间、owner、数据库/来源哈希、命令退出码、备份与恢复证据。任一条件缺失只能标记 `blocked`，feature flag 不能替代门禁批准。

## 回滚步骤与不可逆点

### 首笔 Go 正式写入前

停止接收新域命令，等待/取消已接受操作，关闭对应流和 sidecar 资源；验证生产库仍由 Node owner 且 hash/schema 未被 Go 改写，然后关闭域 flag 并恢复既有 IPC。保留失败日志、diff、进程和 git/证据 manifest，不自动 reset、删除数据库或覆盖用户文件。

### 首笔 Go 正式写入后

这是数据所有权不可逆点。域 flag不能把写路径直接切回 Node/sql.js。回滚按以下顺序进行：

1. 冻结新 mutation，保全生产库、附件/Plan 文件和活动进程证据；
2. 保持 Go 为唯一数据库 owner；客户端可退回兼容 UI/IPC adapter，但 adapter 仍调用 Go application service；
3. 优先部署契约兼容的上一版 Go sidecar 或前向修复，并从完整 snapshot 恢复流；
4. 必须恢复切换前备份时，先记录将丢弃的正式写入和负责人决定，在隔离副本演练后执行；
5. 绝不让旧 Node 热打开 Go 已写生产库。重新引入 Node 只能作为新的离线数据迁移和所有权切换。

### 删除旧 Node writer 前

必须同时具备：

- Go 全库 writer 达到最低观测窗口，所有域无未处置契约/数据/安全告警；
- P00→当前阶段的实际命令、退出码、来源哈希、git/dist 状态和证据 manifest 完整；
- 单 writer 证明覆盖冷启动、崩溃重启、更新安装和陈旧锁恢复，代码/依赖审计确认 Node 不再打开业务库；
- 一致性备份与隔离恢复演练成功，schema/行数/hash/snapshot/附件和 Plan 文件复验通过；
- 已保存可部署的上一版兼容 Go sidecar、前向修复流程和数据恢复 runbook；
- 所有旧 IPC 使用量为零，feature flag 回退不再依赖 Node 数据访问；
- P15 对冻结 check/test 红灯逐项清零或形成有负责人、理由、风险和期限的明确处置。

删除后只允许“回滚 Go 二进制、前向修复、恢复 Go 备份”三类恢复。历史 Node writer 可作为只读源码参考，但不构成可执行恢复方案。上述任一条件后来无法证明，状态只能是 blocked，不能宣称可安全删除。

## 结果

这些规则使迁移可以按域观察和回退，同时把数据所有权、安全和契约变化排除在普通 flag 翻转之外。代价是需要双 adapter 过渡、细粒度指标、较长观测窗口和正式恢复演练。任何放宽默认拒绝、允许秘密进入证据、跨 runtime 接管活动句柄，或在 Go 写后直接启用 Node writer 的实现均违反本 ADR。
