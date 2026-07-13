# P00 DTO、snapshot、状态机与副作用契约

本文件解释机器可读基线 [`contract-baseline.json`](./contract-baseline.json)。它冻结当前 Electron/Node 行为，目标是让后续 Go sidecar 保持兼容，而不是借迁移重命名 DTO、调整状态或“修正”既有副作用。

## DTO 与序列化规则

机器基线逐字段冻结关键 TypeScript interface 的字段顺序、大小写、可选性和 `null` 联合类型，覆盖：

- `AppSnapshot`、`WorkspaceSnapshotPatch`、`ProjectState`、`ActiveOperation`；
- Plan 读取、Script/Executor 运行结果；
- Project、Intake、Loop 与 MCP 关键输入；
- MCP、Chat、Conversation、AI/Claude 配置；
- Terminal session/error/event 与文件访问设置。

兼容规则如下：

- 数据库快照主体继续使用既有 snake_case，例如 `project_id`、`created_at`、`sort_order`、`validation_passed`。Chat、Executor、Terminal 等既有适配对象同时保留已经公开的 camelCase/snake_case 双字段；Go API 不能擅自“统一命名”而删除一侧。
- 可选字段表示“未携带”，`null` 表示“明确为空”。Patch 合并必须使用字段存在性判断，不能把未携带字段当成 `null` 或空数组。
- `AppSnapshot` mutation 返回值保持现状；创建/更新/删除、Loop/Plan/Task/Acceptance 等兼容入口不能改成只返回 `{ok:true}`。
- ID 来源保持数据库整数 `last_insert_rowid()`；Terminal session ID 保持字符串。时间保持 `new Date().toISOString()` 生成的 UTC ISO-8601 字符串。
- AI `apiKey`、Claude/MCP token 在 snapshot、列表和事件中只通过 `has*` 与 masked suffix 暴露。既有 `readMcpAuthToken` 显式读取结果是唯一冻结的明文例外，只能作为用户触发的点对点响应，绝不能进入日志或广播。Terminal `env`、项目 `env_vars` 的值不得进入日志、错误、事件或契约样本。

## Snapshot 与 Patch

全量 snapshot 固定 19 个顶层字段。无项目或项目不存在时返回相同结构：项目列表和 MCP 状态仍可用，其余项目域为 `null`、`[]` 或零值 `scanSummary`，而不是省略字段。

排序是契约的一部分：

| 集合 | 当前顺序 |
| --- | --- |
| plans | `sort_order ASC, created_at ASC, id ASC` |
| tasks | plan 顺序后接 `plan_tasks.sort_order ASC, plan_tasks.id ASC` |
| requirements / feedback | `updated_at DESC` |
| attachments | `created_at DESC, id DESC` |
| events | `id DESC LIMIT 80` |
| scripts / executors | `sort_order ASC, id ASC` |
| Chat history | `created_at ASC, id ASC` |
| Chat persisted queue | `id ASC` |

默认与归一化规则同样被冻结：scan 计数/大小非法时为 `0`，时间无值为 `null`；Plan 默认状态为 `pending`、排序为 `0`、`is_draft` 从 status 推导；事件 `meta` 尝试 JSON object/string 解析，失败保留原字符串；运行中任务实时计算本轮 duration，累计 duration 不得变负。

Patch 只包含 `projectId/activeProjectId/state/tasks/events` 加 operation 三字段的组合。renderer 以动画帧批处理：

1. `loop:update` 到达时保存最新完整 snapshot，并清空此前排队 patch；
2. 同一帧后续 patch 按项目 key 合并；
3. flush 时先应用 snapshot，再依次应用后续 patch；
4. 非当前项目只更新 `projects` 中的运行状态，不覆盖当前项目详情；
5. Patch 未携带的字段保持原值，显式携带 `null`/`[]` 才清空。

## 状态机

| 域 | 状态与关键语义 |
| --- | --- |
| Loop phase | `idle/running/scan/generate-plan/execute-task/validate/waiting/stopped/error`；异常写 `last_error` 并发 `loop.error` |
| Plan | `pending/running/ready_for_validation/validation_failed/completed/interrupted/draft`；draft 激活为 running，interrupted/validation_failed 恢复为 pending |
| Task event | `pending/running/completed/failed/stopping/stopped/interrupted`；中断后的数据库任务可为 `blocked` |
| Chat generation | idle/active 后以 `done/aborted/error/max_rounds` 终结，再处理队列续跑 |
| Chat queue | `queued -> processing -> removed`；cancel/edit/clear 只影响 queued，重启从 queued 行恢复 FIFO |
| Terminal | `starting -> running -> exited/killed/error`，随后可进入独立 closed 生命周期 |
| Operation | active operation 进入 archive 后写 finishedAt/exitCode/cancel/timeout/logTail，成为 lastOperation |

一个重要兼容差异是：任务执行失败会先把数据库任务状态恢复为 `pending` 以允许重试，同时写入 status 为 `failed` 的 `task.failed` 事件。迁移后不能把事件状态直接覆盖成数据库持久状态，也不能因数据库是 pending 而丢失失败事件。

## 错误契约

当前错误边界并不统一，基线保留它并给出后续稳定 code 候选：

- 普通 IPC 多数以抛出的 `Error` 拒绝 invoke，例如“项目不存在”“计划未在运行中”“仅可验收已完成的计划/任务”。候选 code 为 `PROJECT_NOT_FOUND`、`INVALID_STATE_TRANSITION`、`WORKSPACE_BUSY` 等。
- Chat send/stop/clear 返回 `{accepted|stopped|cleared:false,error}`；缺失或跨项目 history 返回空数组。迁移时不能把这些兼容结果突然改成 transport 404。
- Terminal 已有稳定 `TERMINAL_ERROR_CODES`，所有失败经 `{ok:false,code,message,details?}` 返回；handler 内异常归一为 `INVALID_PAYLOAD`。
- 原生打开、外链和更新安装使用 `{ok,error}` 结果；URL/path/installer 状态校验失败时不得发生系统副作用。

错误文本仍是当前 UI 边界，但 Go 层应在不改变文本与 DTO 的前提下附加稳定 code。`retryable` 只说明候选重试策略，不授权自动重试有写副作用的请求。

## 事件顺序

| 事件链 | 冻结顺序 |
| --- | --- |
| snapshot/patch | snapshot 清理旧 patch；flush 时 snapshot 先于其后 patch |
| Task lifecycle | 任务行写入先于 `task.*` 事件；失败为“持久 pending + 事件 failed” |
| Chat terminal | `active=false -> clear abort -> releaseProcessing -> pump next -> emitDone` |
| Terminal exit | 写 session status/endedAt 后依次发 `terminal:exit`、`terminal:status`；显式 close 最后发 closed |
| 配置 changed | 数据写成功后广播完整脱敏配置列表，后到列表覆盖先到列表 |

Chat 的当前顺序尤其不能按直觉改写：`finishGeneration` 会先释放 processing 队列项并尝试 pump 下一条，最后才发送上一轮 done。后续迁移若要调整，必须作为显式版本化契约变更处理。

## 业务副作用、幂等与恢复

| 域 | 成功副作用 | 失败/取消/恢复 |
| --- | --- | --- |
| Project/Intake | 写数据库、附件与计划生成状态，返回/推送 snapshot | create 非幂等；多步骤遗留路径并非全事务；retry 保留失败计数和日志信息 |
| Loop/Plan/Task | 写状态、Plan 文件、事件，启动 CLI/子进程 | stop 终止进程并归档 operation；中断/验证失败可恢复；timeout 可清 session 后重试 |
| Acceptance | 写 accepted_at 或重置完成状态 | 运行中/未完成状态拒绝；批量目标必须整体先通过输入校验 |
| Script/Executor | 写定义与运行结果，启动脚本/plugin 进程 | plugin 重复 start 拒绝；stop 终止并归档；snapshot 用 live registry 校正 running |
| Chat/Config | 写消息、FIFO、Conversation 和配置，提交后发脱敏事件 | stop abort 当前生成但保留 queued；重启按 id 恢复 queued；配置 secret 不回显 |
| Terminal | 启动 PTY，write/resize/kill，保存有界 scrollback | session 只在内存；失败不泄露句柄；renderer 通过 list/replay 恢复视图 |
| DesktopBridge | dialog、打开文件/目录/外链、更新下载与安装 | 校验失败无系统动作；重复 open 仍是外部副作用，不视为幂等 |

## 漂移检查

[`contract-baseline.js`](../../../scripts/migration-baseline/contract-baseline.js) 只读源码与 JSON，执行以下双向校验：

- 精确比较关键 interface 的 header、字段顺序、可选性、nullability 和规范化类型；
- 比较完整/空 snapshot、patch 和 renderer fallback 的顶层键顺序；
- 比较 Plan、Task、Terminal 状态/错误枚举，并检查 Loop、Chat、Queue 转换 marker；
- 检查默认值、SQL 排序、UTC 时间、ID、secret masking、log 上限与恢复查询；
- 按源码位置检查 snapshot/patch、Task、Chat、Terminal 的事件顺序 marker；
- 扫描契约，拒绝可用凭据、私钥与本机用户目录样本。

任何字段缺失/新增、可选或 null 语义改变、默认值/排序改变、状态枚举漂移、事件乱序或敏感值进入契约都会以非零退出码失败。专项测试在 [`contract-baseline.test.js`](../../../scripts/migration-baseline/contract-baseline.test.js) 中覆盖这些失败模式。本任务开发阶段按计划未运行脚本、测试、构建或分析命令。
