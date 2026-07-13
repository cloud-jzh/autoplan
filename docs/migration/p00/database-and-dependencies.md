# P00 数据库 Schema、回填与外部依赖审计

机器清单位于 [`database-and-dependencies.json`](./database-and-dependencies.json)。它将 `src/database.js` 的初始 DDL、后续 `ensureColumn`、表重建和数据回填合并为最终有效结构，并把全仓直接 SQL 表引用和 Node/Electron 外部依赖纳入漂移检查。

## 最终 Schema 轮廓

当前有效结构包含 18 张表：

| 域 | 表 | 关键结构与风险 |
| --- | --- | --- |
| 全局/项目 | `settings`、`projects`、`project_states`、遗留 `loop_state` | `project_states.project_id` 是逻辑 1:1；无外键；`loop_state` 只能表达旧单项目状态 |
| Intake | `requirements`、`feedback`、`attachments`、`intake_plan_links` | owner/link/project 都是应用层关系；附件文件与行可能分离；link 表无外键 |
| Plan/Task | `plans`、`plan_tasks`、`events`、`scan_files` | Task 只有 `UNIQUE(plan_id,task_key)`，无 plan FK；Plan 文件和 scan 路径可能失效 |
| 自动化 | `scripts`、`executors` | 命令、args/options/actions/plugin state 和日志为 JSON/text；依赖 label 非关系约束 |
| Chat/配置 | `chat_messages`、`conversations`、`ai_configs`、`claude_cli_configs` | Conversation/config/message 引用由应用维护；AI/Claude 配置已迁成全局 nullable project_id |

所有关系均缺少 SQLite foreign key。项目、Intake、Plan、Task、Conversation 删除由业务代码手工清理，因此跨项目记录、缺失父记录、陈旧 link/path 都是必须保留给 fixture 和迁移验证的现实风险。

JSON 的 `schema` 列出最终有效列，`columnPolicies` 逐表列出显式默认值和 nullable 列，`tableSemantics` 记录主键、逻辑关系、写入者和孤儿风险。以下列不能从相应初始 `CREATE TABLE` 单独恢复：

- `requirements` / `feedback`: `linked_plan_id`、`plan_generation_claude_config_id`；
- `plans`: 两套 Claude config id；
- `project_states`: 两套 Claude config id；
- `chat_messages.conversation_id`；
- `conversations.codex_session_id`；
- `scripts.source_type`；
- `intake_plan_links` 整表在初始 DDL 后单独创建。

旧数据库还依赖大量重复声明的 `ensureColumn` 才能补齐当前列、默认值和 nullability。机器清单逐项保留这些 definition，删除“看起来已在新库 DDL 中存在”的 ensure 仍会被判定为兼容迁移漂移。

默认值存在两项来源差异：全新 `executors` 表要求显式提供 `project_id/label/command/created_at/updated_at`，旧库经 `ensureColumn` 增列时这些列带 `1` 或空串兼容默认；全新 `scan_files.project_id` 默认 1，旧表重建时默认实际 fallback project id。调用方不得依赖只在升级库存在的宽松默认。

## 索引与约束

当前冻结 17 个显式索引，覆盖项目更新时间、附件 owner、Plan/Task 排序、Script hook、Executor label/sort、Chat Conversation、配置和 Intake-Plan link。关键唯一约束为：

- `settings.key`；
- `scan_files(project_id, scan_type, file_path)`；
- `plan_tasks(plan_id, task_key)`；
- `intake_plan_links(project_id, intake_type, intake_id, plan_id)`；
- `intake_plan_links(project_id, intake_type, intake_id, phase_index)`。

这些唯一约束不等同于外键或级联删除；它们只能阻止同一 key 重复，不能阻止孤儿。

## 迁移与回填顺序

迁移顺序本身属于契约：

1. 初始建表和全部 `ensureColumn`；
2. 建立/补齐索引与 `intake_plan_links`；
3. 从 `loop_state` 创建默认项目，初始化默认设置；
4. 重建旧 `scan_files`，将 `project_id` 加入复合主键；
5. 给旧 requirements/feedback/attachments/plans/events/scan rows 分配默认项目；
6. 只为同项目且实际存在的 legacy `linked_plan_id` 回填 `intake_plan_links`；
7. 按项目和创建顺序给零/非法 `sort_order` 分配递增值；
8. 创建 default `project_states`；
9. 将 `ai_configs` 提升为全局，按完整配置保留最小 id、重映射 Conversation 后删除重复项；
10. 无全局 AI 配置时从 legacy `chat.*` setting 创建默认配置；
11. 修复 Conversation/Message 项目归属，并为未归属消息创建/复用“默认对话”。

`scan_files` 迁移使用 rename → create → copy → drop。中途异常可能留下 legacy 表；这不是一般 `ensureColumn` 能恢复的结构。AI 去重会实际删除行，Plan 排序回填会改变零值顺序，均应在 fixture 副本上验证，不能针对真实 userData 试跑。

## 数据库写入与持久化边界

`AppDatabase` 是当前唯一 sql.js wrapper。`runBatch` 在内存库上使用 `BEGIN/COMMIT/ROLLBACK`，提交后才 export；普通 `run/insert` 在修改后立即 persist。persist 写临时文件和 mirror，尝试复制 `.bak`，随后：

- POSIX 使用 rename 覆盖数据库；
- Windows 使用 copy 覆盖 `dbPath` 后删除 tmp，并非原子 rename；
- mirror、backup、数据库替换和业务附件/Plan 文件都不在 SQL transaction 内。

因此“数据库事务成功”不代表文件副作用原子完成。迁移到单 writer 前必须把数据库、附件、Plan 文件和运行中进程视为独立恢复边界。

## 业务域直接依赖

| 域 | 数据库 | 文件/工作目录 | CLI/进程/PTY | 网络/原生能力 |
| --- | --- | --- | --- | --- |
| Project/Intake | 项目、状态、Intake、附件、Plan/link/event | workspace、附件复制/删除、Plan/manifest | 计划生成 CLI | dialog/open folder 在 Electron |
| Loop/Plan/Task | state/plan/task/event/scan/script/executor | 扫描、Plan Markdown、scope、日志、validation cwd | Agent CLI、validation、Worker、脚本/执行器 | CLI 可间接联网 |
| Chat/Config | message/conversation/config 及 Chat tools 访问的业务表 | 授权 workspace 搜索/读取 | Codex CLI 可 spawn/abort | OpenAI-compatible HTTP client |
| MCP | settings 和 UI 同一业务表/service | 授权文件工具 | stdio transport | `node:http`，默认 localhost |
| Script | scripts/event/state | inline/file source、cwd、临时/日志 | node/bash/PowerShell/cmd | 用户脚本自行决定 |
| Executor | executors/event/state | cwd、tasks.json、日志 | 一次性和 plugin 常驻进程、stdin/signal/kill | 配置命令自行决定 |
| Terminal | project/settings | cwd realpath、shell/profile | `node-pty` | OS shell discovery，无网络 |
| Desktop/Update | settings/project | allowed roots、下载、installer、protocol | custom opener/installer | Electron dialog/shell/net/protocol/window/app |

机器清单还按 external module → source files 冻结全部 `node:*`、Electron、sql.js、node-pty、OpenAI 和 MCP SDK 导入。新增文件系统、进程、PTY、网络或 Electron 依赖而未分类时会失败。

## 敏感列与输出策略

| 分类 | 位置示例 | API / 日志 / 事件 / fixture 规则 |
| --- | --- | --- |
| 凭据 | `ai_configs.api_key`、`claude_cli_configs.auth_token`、MCP/chat setting、所有 Claude auth token 列 | 正常 DTO 只返回 has/mask；显式 MCP 读取是受控例外；禁止日志和广播；fixture 仅不可用假值 |
| 环境与命令 | `project_states.env_vars`、Executor args/options/actions、Terminal env | 只传给授权进程；日志须脱敏；事件不含值；fixture 使用假变量 |
| 本机路径 | workspace、stored/source/file path、script cwd、installer path、Terminal cwd/shell | 仅本机会话且经过 realpath/allowed-root；避免日志绝对路径；fixture 只用临时假根 |
| 内容/日志 | Intake body、Chat content/tool data、event meta、script/executor log、last_error | 项目隔离、有界返回；不复制到基础设施日志；扫描凭据 |
| 会话/进程标识 | agent/codex session、Conversation session、plugin pid | 不能用作授权；日志只保留短标识；fixture 使用确定性假 id |
| 网络端点 | base URL、更新 asset URL、MCP host/header | 校验协议/localhost；不记录 query credential/header；fixture 使用保留无效域名 |

任何无法确认授权、脱敏或路径边界的输出都必须默认拒绝。

## 漂移检查

[`inventory-dependencies.js`](../../../scripts/migration-baseline/inventory-dependencies.js) 只读仓库源码并重新提取：

- 全部 `CREATE TABLE` 字段，合并全部 `ensureColumn` 得到的有效 schema；
- `ensureColumn` 的顺序和完整 SQL definition；
- 所有显式 index；
- 非测试 JS 中出现的 SQL 表引用；
- 非测试 JS 中受控的 Node/Electron/第三方 require 与 dynamic import；
- 表重建、backfill、持久化、文件、process、PTY、network 和 native source marker。

新增/删除/改名表、列、索引、backfill、直接 SQL 表访问或外部依赖未分类时均非零失败。检查器还要求每个业务域声明读写/删除/启动/停止及 transaction 边界，每个敏感组声明 API、日志、事件和 fixture 策略，并拒绝凭据形态或本机用户路径进入清单。

专项测试位于 [`inventory-dependencies.test.js`](../../../scripts/migration-baseline/inventory-dependencies.test.js)。本任务开发阶段按计划未运行测试、构建、lint、分析或验收命令，也未读取真实 Electron userData。
