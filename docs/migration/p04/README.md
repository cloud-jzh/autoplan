# P04 数据库 Owner 原子切换副本演练

P04 只针对系统临时目录中的脱敏 fixture，或调用方已明确授权的数据库副本，建立 schema v1 迁移、审计、锁、readiness、旧 Node 拒绝和备份恢复闭环。本阶段不发现或修改 Electron `userData`，不把迁移能力暴露给 REST、MCP 或 renderer，也不切换真实产品数据库。

统一验证入口：

    npm.cmd run migration:p04:verify

入口仅接受固定 `verify` 模式，没有跳过门禁、覆盖 evidence、保留临时数据库、筛选用例或强制迁移参数。

## 执行顺序

1. 实际执行 P02、P03 统一门禁，并验证各自最新完成 run 的 summary、manifest、源码哈希稳定性和不可变声明。
2. 在验证器拥有的系统临时根中生成 P04 脱敏历史与故障夹具；Node/sql.js 进程退出并关闭数据库后，Go 才能串行读取或写入副本。
3. 执行 schema inventory 漂移守卫、夹具确定性/迁移/no-op 契约，以及真实 `autoplan-migrate preflight` 进程。
4. 执行迁移 runner、备份、故障注入、真实 restore、审计、Owner 锁、readiness 和旧 Node 新 schema 拒绝专项测试。
5. 执行完整 Go gate，再按 P00 冻结规则评价 `npm.cmd run check` 与 `npm.cmd test`。P04 专项失败不能借用既有红灯放行。
6. 生成脱敏 summary、逐夹具 matrix、命令日志和 evidence manifest，随后只清理本次拥有的系统临时根。

预期故障用例必须真正返回非零退出码，并同时匹配稳定阶段与错误码。例如截断数据库的 preflight 只能以 `source_invalid` blocked；意外成功、错误阶段、错误码漂移或退出码被吞没均为验证失败。某个故障用例按预期失败不会阻止后续相互独立的场景执行。

## 结果状态

- `blocked`：P02/P03 命令或完成 evidence 不满足；后续 P04 副本不会被打开。
- `failed`：门禁已通过，但夹具生成、matrix 取证或安全前置失败。
- `completed`、`ok: true`：全部 P04 专项、预期故障、Go gate 和基线比较均被接受，源哈希稳定且临时根清理成功。
- `completed`、`ok: false`：真实命令已执行但存在新增失败、基线漂移、缺失 restore 覆盖或清理失败。

验证 evidence 只记录稳定数据库 ID、版本、表计数、分类差异、SHA-256、错误码和仓库相对文件名。数据库内容、聊天/日志正文、凭据、环境变量值、真实 workspace/userData 路径及可复用会话均不得进入输出。

人工副本演练、切换及恢复步骤见 [runbook.md](./runbook.md)，evidence 字段与审阅规则见 [evidence/README.md](./evidence/README.md)。
