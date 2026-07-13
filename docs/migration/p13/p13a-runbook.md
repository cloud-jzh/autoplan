# P13A Chat 验证与回滚 Runbook

状态：`default-off / evidence-required`。本文件描述 P13A 的独立验证流程，不表示 Chat 已开启，也不授权访问真实 Electron userData。

## 验证入口

只使用仓库内已标记的脱敏 fixture：

```text
npm.cmd run migration:p13a:verify
```

验证器创建临时 HOME、APPDATA、缓存与 Go cache，剥离环境中的凭据、数据库和 `AUTOPLAN_*` 值，强制 `AUTOPLAN_SIDECAR_GO_CHAT_API=false`。它先检查：

- P00 精确已知红灯与 P10、P11、P12 已完成、带 hash 的 evidence；
- Go 唯一 writer、GoDataClient 回退边界及 `go_chat_api` 独立 gate；
- Renderer 只经 `AutoplanClient` 访问 Chat，不允许 `window.autoplan.chat*` 或 `window.autoplan.conversation*` 直调；
- fixture 标记、真实路径拒绝、无 active owner/sidecar 锁和无凭据证据；
- P13A 测试没有 `skip`、`only` 或测试名过滤。

任何前置失败只写入脱敏 `blocked` evidence，并且不会启动 provider、Chat listener 或 database writer。

前置通过后，验证器依次执行 renderer transport/hook contract、Go Chat application/HTTP/SSE/CLI security contracts，再记录 sanitized logs、source hashes、worktree 状态和 immutable manifest。命令任一失败即停止后续命令；它绝不将超时发送自动重放为第二个 turn。

## 覆盖矩阵

fixture 和专项测试覆盖：FIFO admission、重复幂等键、顺序 chunk 与唯一 done、queued/processing queue snapshot、stop/cancel 终态、重启 interrupted 恢复、Last-Event-ID 去重、乱序/resync、会话切换/StrictMode 清理、跨项目过滤、CLI 参数注入拒绝，以及 secret/path/tool data 的边界。

SSE 的恢复动作只能重读 history 与 queue 后调用 `completeResync`；不得重新发送消息、重复 provider 请求或让另一 runtime 接管 stop。

## 启用和回滚

只在专属 evidence `summary.ok=true`、所有 command accepted、source hash 稳定且无 remaining risk 后，才可由受控部署设置：

```text
AUTOPLAN_SIDECAR_GO_CHAT_API=true
```

该变量只控制新的 Chat HTTP/SSE admission。关闭它不改变 Go 的数据库 owner，也不将已开始的 turn、Operation、queue pump 或 stop 转交给 Node/sql.js。发生异常时：

1. 关闭新 admission，保留现有 connection 和证据；
2. 等待 origin runtime 产生 done、aborted、error 或 interrupted；
3. 用权威 history/queue 与 SSE cursor 复核，不重放 provider；
4. 保持 P13B 状态不变，并将 P13A 记录为 blocked 或 rollback。

## Evidence

每次运行在 `docs/migration/p13/evidence/runs/<run-id>/` 创建不可变目录，包含 `summary.json`、sanitized stdout/stderr、hash manifest 和不包含环境值、凭据、真实用户目录、原始 provider 输出或路径的结果。未运行的验证不得写成通过证据。
