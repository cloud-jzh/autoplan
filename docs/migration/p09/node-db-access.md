# P001 Node 数据库访问审计与旧运行时命令闭环

机器可读的基线在 [node-db-access.json](./node-db-access.json)。它记录的唯一源码信息是相对路径、调用类别、调用行号和脱敏契约标识；不记录 SQL 文本、数据库行、配置值或本机位置。

`scripts/migration-p09/audit-node-db-access.js` 只读取 `src` 的生产 JavaScript。每次执行都会重新枚举调用点，并对照 JSON 中每个模块的 `get`、`all`、`run`、`insert`、`runBatch` 与 settings API 计数。新增来源、调用方法、直接 settings SQL、动态语句、`AppDatabase` 实例、sql.js 初始化、导出、持久化、mirror、bak 或关闭流程都会以稳定错误码失败关闭。报告不回显语句或业务数据。

## 调用类别

| 类别 | 含义 | 迁移风险 |
| --- | --- | --- |
| `read` | `get` / `all` 查询 | 必须迁移到同一 application query 和快照排序。 |
| `write` | `run` / `insert` / `runBatch` | 必须迁移到带事务、授权和提交后副作用的命令。 |
| `settings_read` / `settings_write` | settings API 访问 | 不能以自由表读写替代配置服务。 |
| `app_database_constructor` / `sqljs_initialization` | sql.js 打开入口 | Go owner 模式下由 P003 拒绝。 |
| `database_persist` / `database_export` | 全库内存快照落盘 | 属于整库覆盖风险。 |
| `mirror_overwrite_risk` / `backup_overwrite_risk` | mirror、bak 文件入口 | 不受 SQL 事务保护，P005 维护编排处理。 |
| `database_close` | sql.js 与 owner 释放 | 必须在 handoff 前完成。 |

动态语句并非允许任意 SQL 的白名单。JSON 只把现存的封闭内部表/列选择、占位符展开和批量语句数组标成 `reviewed`；对应模块的方法计数仍是冻结基线。任何新来源或调用方法都失败，P002/P003 如改变该面必须同时以明确 Go 命令归属更新该清单。

## Node 模块到 Go 命令的闭环

| Node 调用面 | 契约标识 | Go application / REST | 状态 |
| --- | --- | --- | --- |
| `database.js`、主进程数据库构造、独立 MCP 数据库构造 | `database_lifecycle` | bootstrap owner 与 maintenance cutover；无表级 REST | P003/P005 待接管 |
| `main.js` 项目、快照与删除链路 | `project_gateway` | `projects.Service`、`snapshot.Assembler`；`/api/v1/projects` | 已有静态能力 |
| `loopService.js`、`loop/runtime.js` | `loop_runtime` | Loop application service；项目 Loop action 路由 | P002 待补齐 |
| `loop/plan*`、`loop/task*`、`loop/validation.js` | `plans` | Plans/Tasks application service；`/api/v1/plans/actions`、`/api/v1/tasks/actions` | 查询已有，运行态 P002 |
| `intakeService.js`、`loop/intake*` | `intake` | `intake.Service`；requirements / feedback 路由 | 已有静态能力 |
| `attachments.js` | `attachments` | `attachments.Service`；attachment 路由 | 已有静态能力 |
| `chat/chatController.js`、`chat/chatQueue.js`、`chat/chatTools.js` | `chat_runtime` | Chat application service；conversation runtime 命令 | P002 待补齐 |
| `chat/aiConfigService.js`、`chat/claudeCliConfigService.js` | `chat_config` | Config static 与 chat history service；config / conversation 路由 | 已有静态能力 |
| `executors/executorStore.js`、`loop/scriptHooks.js` | `automation` | Automation application service；scripts / executors 路由 | 静态已有，运行态 P002 |
| `mcpTools.js`、`mcpServer.js` | `mcp_runtime_bridge` | MCP adapter 调用共享 application service | P002 待补齐 |
| `mcpConfig.js`、`loop/snapshots.js` | `config` | `config.Service`；loop-config、mcp-config 路由 | 已有静态能力 |
| `fileAccess/policy.js` | `file_policy` | Files policy service；`/api/v1/file-access-policy` | 已有静态能力 |
| `terminal/terminalConfig.js` | `terminal_config` | Config application service；terminal config 命令 | P002 待补齐 |
| `updateChecker.js` | `update_config` | Config application service；update config 命令 | P002 待补齐 |
| `loop/snapshots.js` | `snapshot` | `snapshot.Assembler`；`/api/v1/projects/{project_id}/snapshot` | 已有静态能力 |

所有契约均在 JSON 中固定了输入/输出 DTO 名称、snake_case 规则、快照、事务、副作用、授权边界及稳定错误集合。`planned_p002` 只表示已知缺口，不表示 Node 可以继续通过原始 SQL 绕过这些业务边界。

## 旧运行时冻结边界

1. Node 只在 legacy owner 生命周期内持有 sql.js；切换到 Go owner 后，P003 的 guard 必须在所有初始化、schema/backfill、写入、导出及落盘入口失败。
2. Loop、Chat、脚本钩子、执行器与 MCP 必须复用 REST/MCP/UI 相同的 application service。兼容桥不能接受任意 SQL 或暴露表级 CRUD。
3. `runBatch` 的旧内存事务不覆盖 mirror、bak、附件、Plan 文件或进程副作用；这些资源由 P005 的维护状态机冻结和交接。
4. 审计报告只能作为迁移证据，不能用于定位真实用户数据，也不能变更数据库、文件或进程。
