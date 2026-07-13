# P04 Node schema v1 取证清单

机器清单为 [`schema-inventory.json`](./schema-inventory.json)，生成与漂移守卫为 [`inventory-schema.js`](../../../scripts/migration-p04/inventory-schema.js)。清单只读取仓库相对路径 `src/database.js` 和 P00 `database-and-dependencies.json`，不打开任何 SQLite 文件、Electron `userData` 或工作区文件，也不记录数据库行值。

## 冻结结论

`autoplan-schema-v1` 合并了初始 `CREATE TABLE`、全部 `ensureColumn`、单独创建的 `intake_plan_links`、17 个显式索引、复合/唯一约束以及 `scan_files` 重建后的最终结构。当前结果为 18 张表、275 列、17 个显式索引和 12 组兼容迁移语义。表按名称、列按有效 SQLite ordinal、索引按名称稳定排序；每个列声明记录类型、nullable、SQL default、主键序位以及稳定源码锚点。

| 域 | 表 | schema v1 关键事实 |
| --- | --- | --- |
| 全局/项目 | `settings`、`projects`、`project_states`、`loop_state` | `project_states` 是项目 1:1 状态；`loop_state` 仅保留为旧单项目 seed，不能成为多项目权威状态 |
| Intake | `requirements`、`feedback`、`attachments`、`intake_plan_links` | legacy `linked_plan_id` 只为存在且同项目的 Plan 回填 phase 1；附件 owner 与 intake id 都是多态关系 |
| Plan/Task | `plans`、`plan_tasks`、`events`、`scan_files` | Task key 在 Plan 内唯一；Plan 排序只补正零/非法值；`scan_files` 最终主键为 `project_id,scan_type,file_path` |
| 自动化 | `scripts`、`executors` | 路径、命令、JSON options/actions/dependencies 和日志均保留兼容形状，但进入敏感数据策略 |
| Chat/配置 | `chat_messages`、`conversations`、`ai_configs`、`claude_cli_configs` | 消息归属修复到 Conversation；AI 配置全局化、按完整身份保留最小 id 并先重映射后删除重复项 |

初始 DDL 不是最终结构。尤其是 `requirements`/`feedback` 的 link 与 Claude config id、`plans`/`project_states` 的两套 config id、`chat_messages.conversation_id`、`conversations.codex_session_id`、`scripts.source_type`，以及整张 `intake_plan_links`，均必须从后置兼容逻辑取证。清单保留同一列在 fresh CREATE 和 legacy `ensureColumn` 中的多个声明，因此不会抹平 Executor 必填列默认值、`scan_files.project_id` fallback 等来源差异。

## 兼容迁移顺序

Node 的有效顺序已冻结，后续显式 migration 不得只复制 DDL：

1. 建初始表，依源码顺序补齐全部 `ensureColumn`，并建立 Intake link 与后置索引。
2. 空项目集从 `loop_state` 创建默认项目；默认 settings 使用 `INSERT OR IGNORE`，已有值不覆盖。
3. `scan_files` 在缺少最终复合主键时执行 rename → create → copy/coalesce → drop。
4. 将六张 legacy 表的 NULL `project_id` 分配给默认项目；非 NULL 孤儿或跨项目值不自动修复。
5. 仅对正数、存在且同项目的 legacy plan link 回填 phase 1；冲突使用 `INSERT OR IGNORE` 的事实必须由审计揭示。
6. 按项目、`created_at`、`id` 为零/非法 Plan order 分配递增值，保留全部正数值。
7. 缺失的默认 `project_states` 从 singleton seed 创建，已有行保持不变。
8. AI 配置先全局化，再按完整字段身份保留最小 id、重映射 Conversation，最后删除完全重复行。
9. 无全局 AI 配置时读取 legacy `chat.*` 创建默认项；schema v1 将原始凭据导入 `secret_refs`，而不是复制进报告或新业务列。
10. 修复 Conversation/Message 项目归属，为仍未归属的消息创建或复用每项目“默认对话”。

上述逻辑当前没有 `schema_migrations` 或 `user_version`，`scan_files` 重建也没有显式顶层事务。中断遗留表、AI 去重、ignored link 冲突、非法排序与所有未解释行变化都属于迁移阻断风险，不允许以删除、置空或修改 fixture 制造通过。

## 关系与外键决策

机器清单逐关系给出 `establish-v1`、`defer-audit-application`、`defer-legacy-audit` 或 `retain-without-foreign-key`，并明确 `CASCADE`、`RESTRICT`、`SET NULL` 或 `NONE`。可在审计通过后直接建立的关系包括项目状态、项目归属、Plan→Task、项目→Event/scan、Chat ownership、Conversation→AI config，以及 Intake link→Project/Plan。

以下关系不能无损强制为普通 SQLite FK：

- `attachments.owner_type/owner_id` 与 `intake_plan_links.intake_type/intake_id` 是多态引用，只能由确定性审计和 application service 约束；
- `requirements.linked_plan_id`、`feedback.linked_plan_id` 是可能无效的旧兼容字段，原值保留并报告，规范关系转向 `intake_plan_links`；
- AI/Claude config 的 nullable `project_id` 表示当前全局或历史 scope，必须先完成全局化/默认唯一性审计；
- `loop_state` 只是 seed，不建立到任一项目的外键。

任何准备建立的 FK 都以前置 orphan、跨项目、路径及聚合审计零异常为条件；失败时保留原数据并关闭失败。

## 秘密与敏感数据

清单显式列出六类位置及五类输出策略：迁移、API、日志、事件和 fixture。

- 原始秘密：`ai_configs.api_key`、`claude_cli_configs.auth_token`、`settings:chat.apiKey`、`settings:mcp.authToken` 及全部 Plan generation/execution Claude auth token。schema v1 迁入 `secret_refs`，业务记录只保留脱敏引用或 presence；禁止 API、日志、事件和 fixture 回传原值。
- 会话标识：Agent/Codex/Conversation session id 与 Executor plugin state。它们不能作为授权，日志只允许省略或短标识。
- 环境与命令：`project_states.env_vars`、validation/Agent/Plan 命令、Executor command/args/options/actions、Script body。不得复制进事件或基础设施日志，未知 JSON 不通过解析后丢字段来“脱敏”。
- 本机路径：workspace、source、attachment、Plan、Script path/work_dir、scan file、生成日志与 installer path。迁移保留值但必须只读路径审计；报告仅使用字段名、稳定脱敏 id 或授权根相对形式。
- 内容与日志：Intake body、Chat/tool 内容、Event message/meta、Script/Executor log、生成错误和 Task raw line。只按既有项目/会话契约有界访问，不进入迁移证据或复制 fixture。
- 网络端点：AI/Claude base URL、MCP 地址和更新 URL。与凭据分离，禁止 token-bearing URL 或 header 进入日志/事件。

## 漂移守卫

输入哈希分为三项：`src/database.js` 的有效 schema/索引语义哈希、从 `AppDatabase.migrate` 到 `migrateScanFilesTable` 的兼容方法块哈希，以及 P00 机器基线完整字节哈希。使用语义范围而非整文件行号，可让后续与 schema 无关的 Owner 启动门禁不产生伪漂移；任何表、列、约束、索引或兼容迁移实现变化仍会失败。

源码位置使用 `src/database.js#方法:事实类型:对象` 稳定锚点，避免无关插行改变证据。守卫还会：

- 重新提取 fresh CREATE 与 legacy ensure 声明，交叉核对 P00 的 18 表/列集合；
- 核对全部兼容 marker 的存在与顺序、表/列 ordinal、唯一键和索引定义；
- 要求每表都有历史差异、回填前置、不变量和关系说明，每个 FK 与敏感组都有完整策略；
- 拒绝清单中的凭据形态、本机用户目录和私钥内容；
- 要求 JSON 为 UTF-8、两空格缩进、单个末尾换行的 canonical 字节形式，重复生成结果一致。

本阶段仅完成开发文件，不执行测试、构建、lint、coverage、e2e 或验收命令。
