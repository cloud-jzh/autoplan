# P10 运行手册

## 前置门禁

1. 使用 `npm.cmd run migration:p10:verify -- --fixture-root <authorized-p10-fixture>`；不得直接调用子命令替代该入口。
2. verifier 必须先验证 P00 固定红灯签名和 P09 的哈希完整完成证据。任一缺失、篡改或非完成状态均为 `blocked`。
3. fixture 必须显式标记为 P10 授权副本，且无活动 owner lock、SQLite sidecar 或真实 userData 路径。P10 不自动创建、发现或热交接任何生产库。
4. 前置失败时只保留已执行 gate 的脱敏 stdout/stderr、真实退出码与 blocked 原因；不得继续启动服务、写入数据库或运行 P10 测试。

## 状态、幂等与恢复

- Operation 只能经历 `queued → running → succeeded|failed|cancelled|interrupted`，`queued` 也可直接 `cancelled|interrupted`。终态不能回退。
- 幂等作用域为项目和 Operation 类型；相同 key 与 digest 返回原 Operation，不同 digest 返回稳定冲突，绝不启动第二副作用。
- 取消 queued 立即裁决为 `cancelled`；取消 running 仅记录请求，最终由同一 version 前置条件在完成和确认取消间裁决。
- 启动恢复将遗留 running 转 `interrupted`；queued 不自动执行，只能由匹配恢复器以原 digest 与 expected version 认领。不可认领或到期 queued 转 `interrupted`。

## 事件、保留与 SSE

- 每个已提交的业务/Operation 变更在同一事务分配项目连续 `project_revision` 与全局递增十进制 `event_id`。客户端按整数语义比较字符串 cursor，不转换为 JavaScript number。
- outbox 是权威来源。dispatcher 可在发布与确认间崩溃；重启只重放已提交事件，不能重执行业务 mutation。
- Last-Event-ID 缺失时从可用历史开始；无效、未来、项目不匹配、保留水位过期、revision 缺口和慢消费者均发送无 cursor 的 `resync_required`，不合成 patch。
- heartbeat 不改变 cursor。项目流要求 revision 连续；Operation 筛选流允许跳过其他项目变更造成的 revision 间隔。
- SSE 与 REST 使用相同 session、Host、Origin、项目归属及 Operation 归属验证。关闭、取消或慢消费者必须释放订阅和计时器。

## Renderer resync

- renderer 仅通过 `AutoplanClient` 订阅，不在组件中直接使用 EventSource/fetch。
- 已接纳的 persistent event 才推进 event/revision 水位；重复或倒序事件丢弃，未知、跨项目或缺口事件触发单飞权威 snapshot。
- `resync_required` 会暂停 reconnect。snapshot 原子替换成功后重置并恢复订阅；失败则保留错误状态并用有界退避重试，不清空为默认数据。
- 高频事件在固定窗口合批；Operation 终态优先刷新。项目切换、StrictMode、卸载与 transport 关闭必须 abort 请求并释放队列、计时器和订阅。

## 输出、脱敏与回退

- stdout/stderr 先在内存按 UTF-8 和字节/行上限清洗；不可安全内容丢弃，绝不进入 Operation、outbox、SSE、日志、fixture 或证据。
- 证据拒绝 secret、token、session、cookie、Authorization、真实 userData、绝对用户路径和 file URL；日志只保存脱敏副本、哈希和字节数。
- 回退仅关闭 P10 创建的订阅/dispatcher，关闭 `go_operation_api` / `go_operation_events`，并删除 verifier 自己的临时目录和授权副本。不得接管运行中的任务、把 Go 写过的库交回 Node，或触碰真实 userData。

## 证据与剩余风险

每次运行创建不可覆盖的 `docs/migration/p10/evidence/runs/<run-id>/`，记录命令、起止时间、实际退出码、stdout/stderr 哈希、source/input 哈希、fixture 授权、清理结果和剩余风险。P00 的冻结红灯签名必须保持原样；P10 verifier 不隐藏、重命名、跳过或以重试掩盖它。
