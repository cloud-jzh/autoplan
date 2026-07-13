# P07 验证、切换与回退手册

本手册只适用于 P07 的脱敏 fixture、系统临时库或调用方显式授权的独立副本。它不授权读取、修复、迁移或回写真实 Electron profile、生产数据库、真实 workspace 或 P07 以外领域。

## 1. 前置条件

执行 P07 前必须同时满足：

- P05 `migration:p05:verify` 真实通过，并存在最新的不可覆盖、manifest 哈希关联、`completed` / `ok: true` evidence。
- P06 `migration:p06:verify` 真实通过，并存在同等完整的 evidence。
- P07 `state-machine-cases.json`、`expected-errors.json`、OpenAPI action schema、capability schema、owner guard、renderer transport 和 golden comparator 均保持冻结检查可复算。
- 数据库只能是系统临时库或显式授权副本；Go writer 必须持有 `DatabaseOwnerProof`，以 `AuthorizedCopy` 打开，并通过 schema/checksum。
- Node/sql.js writer 已关闭同一副本。不得通过热回退、IPC 写入口或开发模式同时打开 Go writer 和 Node writer。

统一执行：

```text
npm.cmd run migration:p07:verify
```

前置不满足时保留 `blocked` evidence，不手改 summary，不伪造退出码，不更新 golden，不补跑后续命令。

## 2. 能力和 owner 映射

P07 limited 环境中，只有纯持久化方法允许在 capability enabled 后走 HTTP：

| 能力 | HTTP owner |
| --- | --- |
| plans/tasks/events 查询 | Go application service |
| plans reorder/delete | Go application service |
| plan/task accept、unaccept、batch accept、batch unaccept、redo | Go application service |

以下长任务仍由 IPC owner 处理真实用户动作；Go HTTP 端只提供 disabled contract：

| Action | P07 HTTP 结果 | 用户动作 owner |
| --- | --- | --- |
| plan run/stop/resume/re-execute/recreate | non-2xx `not_implemented` | IPC |
| task run/run-batches/stop | non-2xx `not_implemented` | IPC |

HTTP mutation 一旦被选中，非 2xx 不得静默回退 IPC 重放同一意图；renderer 只能在请求发出前因 capability disabled 或 discovery 失败保留 IPC owner。

## 3. 验证步骤

1. 确认 P05/P06 evidence 的 `summary.json` 哈希被各自 `evidence-manifest.json` 引用。
2. 运行 P07 verify；检查首三条命令必须依次为 `p05-gate`、`p06-gate`、`p07-safety-preflight`。
3. 核对 Node golden command 的结束时间早于所有 Go writer command，且命令区间无重叠。
4. 核对矩阵覆盖状态机、排序、删除、accept/unaccept、redo、事务回滚、并发冲突、审计事件、OpenAPI、MCP、renderer、disabled action 和脱敏扫描。
5. 核对 `npm.cmd run check` 和 `npm.cmd test` 只按 P00 冻结签名接受既有结果；新增、消失、重命名或未分类失败均为不通过。

## 4. 并发冲突与事件恢复

P07 事务由 Go repository 在顶层事务中完成。重排、删除、accept/unaccept、redo 依赖 project ownership、版本或 compare-and-swap 前置；冲突应返回稳定错误而不是覆盖新状态。

事件只在事务提交后可见。恢复或重放时只允许根据已提交业务行和事件 ID 做幂等检查；不得根据客户端请求体合成成功 snapshot，也不得为回滚事务发布成功事件。

## 5. 回退边界

- 未进入 Go writer：保留 blocked evidence，修复前置后重新创建新的 evidence run。
- Go writer 只写了临时库或显式副本：丢弃该副本，关闭 P07 HTTP route/feature flag，保持 IPC owner。
- 已选择 HTTP mutation 但返回错误：向上暴露原始稳定错误，不重放 IPC，不伪造 success snapshot。
- 不把 Go 写过的数据库热交回 Node/sql.js；需要回到 Node owner 时，必须使用未被 Go 写入的授权副本或重新从受控来源生成。

## 6. 证据收集

每次 `migration:p07:verify` 创建新的：

```text
docs/migration/p07/evidence/runs/<UTC-run-id>/
```

失败、blocked、cleanup 失败和 P00 红灯比较结果都必须保留。只能用新的 run 重试，不能覆盖、编辑或删除既有 run。
