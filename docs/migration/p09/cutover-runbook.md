# P09 切换副本演练 Runbook

本 runbook 仅适用于显式脱敏的 fixture、临时目录或副本。它不是生产切换授权，也不能用于 Electron `userData`、活动 `autoplan.sqlite`、真实 workspace 或未知来源的数据库。

## 前置门禁

1. 操作目录必须是绝对路径，名称或祖先目录应明确表明 fixture、sanitized、temp、tmp 或 drill。
2. 目录、数据库、附件和 Plan 文件均不得是符号链接；目标数据库必须是 `.copy`、`.backup` 或 `.bak` 副本，不能命名为 `autoplan.sqlite`。
3. Node 与 Go writer 时间线不得重叠。先冻结 UI mutation、新任务、Loop、Chat、CLI、脚本和执行器；完成最终 Node persist 并释放 owner 后，再进入 Go owner lock。
4. 备份 manifest、附件和 Plan 资源均需完成哈希验证。任一资源缺失、漂移、活动 WAL/SHM/journal、owner lock 或审计失败均为阻断，不得重试覆盖现场。

## 演练命令

在仓库根目录执行。`<fixture-root>` 是新建的脱敏临时目录，`<evidence-dir>` 必须位于其中且尚不存在报告文件。

```powershell
node scripts/migration-p09/run-cutover-drill.js `
  --fixture-root <fixture-root> `
  --evidence-dir <fixture-root>\evidence
```

预期退出码：

- `0`：维护故障矩阵、恢复边界和脚本契约均通过；报告写入 `cutover-drill-report.json`。
- `2`：任一子命令失败、路径不安全、矩阵不完整或报告目标已存在。保持维护状态，保留现场和原始 immutable backup。

报告只保存稳定命令标识、退出码、耗时、输出哈希、故障数量和不变量；不保存绝对路径、stdout/stderr、token、环境变量、备份内容或会话信息。

## 切换决策点

| 阶段 | 允许动作 | 阻断后的处理 |
| --- | --- | --- |
| 冻结/排空/最终 Node persist | 终止旧 writer，保存证据 | UI 保持关闭；不得重新启动 Node writer |
| preflight 与 immutable backup | 只验证受控副本 | 保留原备份与故障现场；不得覆盖 backup |
| Go owner、审计、迁移、readyz/smoke | 仅由 Go 取得唯一 RW owner | 终止/阻断 writer，保持 maintenance |
| UI 打开前且首笔正式 Go 写前 | 允许恢复到**新的独立副本** | 见 rollback runbook；原演练源不得被覆盖 |
| 首笔正式 Go 写后 | 仅 Go 二进制回退或前向修复 | 旧备份恢复需要显式数据截断确认 |

## 文件清单与剩余风险

演练必须核对数据库、`.bak`、`.mirror`、声明的附件、Plan 文件和 manifest；恢复后复核 schema、行数/关系审计、snapshot 以及附件/Plan 内容。当前阶段没有真实 userData 的授权参数、环境变量旁路或自动回切 Node writer。真实生产切换仍需未来阶段单独审批、独立变更窗口和人工授权。
