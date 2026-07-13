# P05 Project/Config 写链路阶段门禁

P05 将 Project/Config mutation 从 Node 基线迁移到 Go repository、application 和 HTTP 层，并由 renderer 通过 loopback HTTP transport 调用。阶段门禁冻结版本冲突、幂等重放、完整 snapshot、文件访问策略，以及 Node/Go mutation snapshot 的深比较结果。

统一入口：

    npm.cmd run migration:p05:verify

非 Windows 环境可使用 `npm run migration:p05:verify`。验证器不接受其它参数，也不提供更新 golden、忽略字段或跳过门禁的开关。

## 门禁顺序

1. 首条真实命令运行 P04 统一门禁。P04 非零，或最新 P04 evidence 的 summary/manifest/hash/源码稳定性无效时，P05 立即记录 `blocked`，不启动任何 P05 Node/Go writer。
2. 校验 P05 manifest、Node golden、稳定错误目录、write contract、P04 schema/migration checksum、OpenAPI、renderer loopback 边界和文件策略的冻结输入。
3. Node 在生成器拥有的系统临时 SQLite 中串行完成 mutation，关闭 sql.js 与 owner lock 后提交已脱敏 artifact。
4. Go repository、application、HTTP 与 filesystem 契约按顺序运行；每项只使用测试创建的事务副本或 fake，不打开 Electron `userData` 或产品数据库。
5. renderer transport 契约验证 Project/Config mutation 的 version、`Idempotency-Key`、错误映射与完整 snapshot。
6. 深比较 Node/Go mutation bundle，再执行 OpenAPI/安全漂移扫描以及 P05 验证器自身契约。
7. 最后运行项目级 `check`、`test`；仅按 P00 冻结 expectations 接受已知红灯，任何新增或变化的失败均不通过。

所有 P05 专项都要求退出码为零。命令不会并发执行；evidence 时间线必须证明唯一 Node writer 已关闭，随后才出现 Go writer。

## 安全边界

- 数据库输入只允许合成 fixture、生成器拥有的临时数据库或测试内授权副本。
- 禁止自动发现或读取 Electron `userData`、`autoplan.sqlite`、环境中的数据库路径和生产凭据。
- 验证环境剔除凭据、会话和数据库路径形态的环境变量，仅记录剔除数量。
- 只清理由本次验证器创建、位于系统临时目录且名称匹配 `autoplan-p05-verify-*` 的根。
- golden 深比较覆盖数组顺序、未知字段、完整 snapshot、稳定错误码与版本轨迹；不存在宽松比较或更新模式。

详细操作见 [runbook.md](./runbook.md)，证据结构与脱敏要求见 [evidence/README.md](./evidence/README.md)。
