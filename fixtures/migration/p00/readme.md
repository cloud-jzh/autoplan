# P00 脱敏迁移 Fixture

本目录只提交生成配方和说明，不提交任何真实或生成后的数据库。生成器为 [`scripts/migration-baseline/fixtures.js`](../../../scripts/migration-baseline/fixtures.js)，受控配方为 [`manifest.json`](./manifest.json)。

## 五类 Fixture

生成结果固定包含：

1. `empty.sqlite`：当前有效 Schema，没有项目/业务行，仅保留 DDL 初始化产生的 singleton `loop_state` 和不可用 MCP token 占位；
2. `legacy-normal.sqlite`：旧单项目形态，覆盖 `ensureColumn`、默认项目、scan 表重建、Plan 排序和 Chat/AI 回填输入；
3. `orphan-cross-project.sqlite`：缺失 Plan/owner/config、跨项目 Intake-Plan 和 Conversation-Message 引用；
4. `invalid-paths.sqlite`：`../` traversal、保留的不存在 POSIX/Windows 路径、无效 cwd 和过期 installer；
5. `large.sqlite`：由 seed 和 `largeCount` 决定的 N 条 Requirement 与 N 条 Event。

同时生成 `invalid-path-cases.json` 和 `generated-manifest.json`。后者记录 seed、固定时间、实际表/行数和每个数据库的 SHA-256，供最终证据校验。

## 生成方式

默认输出到系统临时目录下一个确定性命名的新目录：

```powershell
node scripts/migration-baseline/fixtures.js
```

调整大数据量与 seed：

```powershell
node scripts/migration-baseline/fixtures.js --seed 20260711 --large-count 1000
```

显式输出必须位于系统临时目录，或位于预先存在且显式授权的根目录：

```powershell
node scripts/migration-baseline/fixtures.js --allow-root D:\tmp --output D:\tmp\autoplan-p00-fixtures-run-1
```

生成器没有 `--force` 或覆盖模式。同一目标再次运行会直接失败；需要重新生成时使用新的目标目录。

## 安全边界

- 默认和显式目标都必须是尚不存在的新目录。生成器先持有 per-target 独占锁，在同目录 staging 中完成五个数据库、检查行数、计算哈希和扫描敏感值，最后 rename 为目标。
- 输出只允许位于真实系统临时目录或 `--allow-root` 内。目标父目录必须已存在；生成器不会递归创建未授权路径。
- Windows AppData、macOS Application Support 和 Linux `.config/autoplan` 等已知 Electron userData 永久拒绝，即使位于授权根也不例外。
- 目标或任何已存在祖先是符号链接时拒绝生成，避免授权根被重定向。
- 已存在目标一律拒绝，因此不会覆盖或打开正由 Node/sql.js、Electron 或其他进程使用的数据库。独占锁还阻止两个生成器并发竞争同一目标。
- 失败只清理生成器本次创建、名称以 `.autoplan-p00-staging-` 开头且位于目标父目录的 staging；不删除目标、父目录或其它文件。
- 生成过程直接使用 sql.js 内存数据库，不实例化 `AppDatabase`，不会触发真实 userData、MCP token 生成、Electron、CLI、网络或 PTY。

## 脱敏与异常路径

所有名称、正文、hash、token 占位和路径都是显式合成值。数据库中不使用本机用户名、HOME、AppData、真实 workspace、真实附件、来源数据库正文或可用凭据。

异常绝对路径只使用保留假根：

- POSIX：`/__autoplan_fixture__/...`；
- Windows：`Z:\__autoplan_fixture__\...`；
- URL：`https://example.invalid/...`。

生成完成前会扫描所有产物，拒绝 HOME/AppData、本机 userData 路径、OpenAI/GitHub token 形态和 PEM 私钥。字段名 `api_key`、`auth_token`、`env_vars` 可以存在，但其值只能是空值或不可用的 fixture 占位。

`pathCases` 还描述 symlink 越界预期。生成器本身不要求 Windows symlink 权限；最终平台测试如在临时 workspace 创建 `link-to-outside`，必须以 realpath containment 失败。Windows 假盘符样本始终覆盖无效绝对路径边界。

## 可复现与验收预期

同一代码版本、sql.js 版本、seed 和 `largeCount` 应生成相同数据库 bytes 与 SHA-256。时间统一为 `2026-01-02T03:04:05.000Z`，ID、名称、正文和 Event meta 都由顺序与 seed 决定。

验收读取 `generated-manifest.json`，至少核对：

- 五个数据库均能由 sql.js 打开；
- 表集合与实际行数和 manifest 一致；
- legacy fixture 经迁移后满足 P003 最终 Schema；
- orphan/cross-project 记录被审计报告发现而非静默删除；
- traversal、越界、缺失和过期路径全部 fail closed，不执行 native open/spawn；
- 大数据量行数精确等于 `largeCount`；
- 产物扫描不含用户路径或凭据。

## 清理

成功生成的目录完全位于临时或显式授权根中。最终验收读取并关闭所有 sql.js handle 后，按 `generated-manifest.json` 记录的输出目录进行一次显式清理。清理前必须再次确认解析后的目标仍位于临时/授权根，且名称是本次生成目标。生成器自身不自动删除成功产物，以便保留验收证据。
