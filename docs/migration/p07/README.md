# P07 Plans/PlanTasks/Events 验证门禁

P07 只迁移 Plans、PlanTasks、Acceptance 与 Events 的纯持久化能力。长任务 action 仅保留版本化契约和 disabled capability，Go HTTP 端稳定返回 `not_implemented`，renderer 继续把真实执行动作交给 IPC。

统一验收入口：

```text
npm.cmd run migration:p07:verify
```

非 Windows 环境可使用 `npm run migration:p07:verify`。该命令不接受真实 Electron `userData`、生产数据库、真实 workspace、golden 更新、跳过检查或继续执行失败前置的参数。

## 固定门禁顺序

1. 真实执行 `migration:p05:verify`，记录原始退出码、起止时间和脱敏 stdout/stderr；失败时 `status=blocked`，不启动 P07 Node 或 Go writer。
2. 真实执行 `migration:p06:verify`，同样作为硬门禁；失败时 `status=blocked`，不运行 P07 safety、golden、Go、renderer 或 P00 基线命令。
3. 执行 `scripts/migration-p07/check-safety.js preflight`，核验 P07 fixture、稳定错误、OpenAPI action surface、capability catalog、owner guard、renderer capability owner 和 golden comparator。
4. 串行执行 Node Plan golden contract；该阶段结束后才允许 Go repository/application/httpapi/MCP 命令开始。
5. 串行执行 Go repository、Plans application、Acceptance fixture、Events audit、HTTP action/capability、MCP package、renderer transport、P07 编排自身测试，以及 P00 冻结 `check`/`test` 基线比较。
6. 写入新的不可覆盖 evidence run，并只清理本次验证器创建的 `autoplan-p07-verify-*` 系统临时根。

任何 P05/P06 gate、P07 safety、schema/checksum、owner、显式副本授权或脱敏前置失败，结果必须是 `blocked` 或 `failed` 的真实证据，不能继续跨阶段补跑后续 mutation。

## 安全范围

- 验证器只使用系统临时目录内的脱敏数据库、Node/Go 独立副本和 sentinel；不会自动发现、打开或修改真实 Electron `userData`、生产数据库或未授权 workspace。
- Go writer 必须依赖 `DatabaseOwnerProof`、`AuthorizedCopy` 和 schema version；Node/sql.js 与 Go 的写入时间线必须单调、无重叠。
- 证据可记录命令、退出码、时间、相对路径、稳定错误码、scenario ID、矩阵结果、字节数和 SHA-256；不得记录 token、session、CLI command secret、真实路径、`stored_path`、`file://`、数据库内容或用户正文。
- 回退只关闭 P07 HTTP persistence 路由或 feature flag，并丢弃验证副本；不能把 Go 写过的数据库热交回 Node/sql.js writer。

操作细节见 [runbook.md](./runbook.md)，证据结构和审阅要求见 [evidence/README.md](./evidence/README.md)。
