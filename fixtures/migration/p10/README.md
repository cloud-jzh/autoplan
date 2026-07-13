# P10 Operation 与 SSE 故障矩阵 Fixture

这些 fixture 仅描述合成的 Operation、outbox 和 SSE 恢复场景；不包含真实数据库副本、workspace 路径、会话凭据、命令、环境变量、终端输出或用户数据。

- `operation-cases.json` 覆盖幂等重放/冲突、取消竞争、唯一终态和启动恢复，期望的事件序列只使用 Operation 状态与稳定码。
- `event-streams.json` 覆盖初连、Last-Event-ID 重连、重复事件、revision 缺口、保留水位、跨项目事件和心跳。`event_id` 是十进制不透明游标，不能转换成 JavaScript number。

`scripts/migration-p10/protocol-contract.test.js` 会验证 fixture 的版本、字段、终态数、P10 信封连续性与脱敏边界。它只读取仓库内这两个静态 JSON 文件；不会启动服务、打开 SQLite、访问 Electron userData 或调用外部网络。
