# P08 验证、回退与恢复手册

本手册仅适用于 P08 脱敏 fixture、系统临时目录或调用方显式授权的独立副本。
它不授权扫描、读取、修复或写入真实 Electron profile、生产数据库、真实
workspace 或现有用户内容。

## 1. 前置条件

执行前必须同时满足：

- P07 `migration:p07:verify` 真实通过，最新 evidence 为不可覆盖、哈希关联的
  `completed` / `ok: true` 记录。
- P00 `check` 的既有红灯仅按冻结精确签名接受，`test` 必须成功；不得新增、
  隐藏、跳过或重命名失败。
- P08 static contract、Node golden、manifest、分页/错误夹具、schema 和
  OpenAPI 保持冻结哈希。
- P00 `check`/`test` 在任何 P08 Node 或 Go writer 前执行；`check` 仅可按
  冻结精确签名保留既有红灯，`test` 必须为零退出。
- Go writer 只打开显式授权副本，并持有 `DatabaseOwnerProof` 与
  `AuthorizedCopy`；Node/sql.js 已关闭同一副本。
- secret-store 与 secret-key 根均在业务库之外的受控临时根中，且不与数据库
  文件或备份目录重用。

统一执行：

```text
npm.cmd run migration:p08:verify
```

前置失败时保留 `blocked` evidence。不要手改 summary、伪造退出码、更新
golden 或跳过顺序。

## 2. 静态能力与既有 runtime owner

| 能力 | P08 Go 边界 | 既有 runtime owner |
| --- | --- | --- |
| scripts/executors CRUD、排序、toggle、仅持久化导入 | Go static application | 无执行行为 |
| conversations/messages 历史、AI/Claude/MCP 静态配置 | Go static application | 无 Chat/MCP runtime |
| scripts/executors run/stop/action、hook、schedule | disabled，非成功响应 | IPC/runtime owner |
| Chat send/stop/queue/pump/stream/title | disabled，非成功响应 | IPC/runtime owner |
| MCP start/stop/listener | disabled，非成功响应 | 既有 runtime owner |

HTTP static transport 仅在显式开发配置下启用，默认仍是 IPC。HTTP mutation
返回非成功时不得回退 IPC 重放同一意图。

## 3. Secrets 与明文副本迁移

1. 通过 `prepare-secret-copy.js` 从调用方提供的 `.sqlite.copy` 创建新的
   authorized copy、不可变备份目录、secret-store 和 secret-key 根。
2. 先执行 dry-run，报告只能包含 kind、表/列、计数、动作与哈希。
3. 执行迁移时先写 provider secret 并确认可用，再提交 `secret_refs`，最后
   清理旧字段。数据库提交失败必须补偿新 provider 写入。
4. 恢复演练只能从不可变备份生成新的目标副本。核验 integrity、schema、
   行数、关系、snapshot 和 active secret references。

OS 凭据库 locked、denied、corrupt 或 malformed 时立即失败。仅 provider
确实 unavailable 且策略允许时才使用 AEAD fallback；fallback 密钥和
envelope 与业务库分离。任何恢复、清理或失败证据都不得保存明文、末四位、
provider locator、环境赋值或绝对路径。

## 4. 冲突、事务与回滚

- Scripts/Executors、Conversation、配置默认值和 Secrets 替换/删除使用
  project scope、version/CAS 与顶层事务。冲突返回稳定错误，不能覆盖新状态。
- Executor 批量导入、Conversation 删除与消息更新时间、AI 配置解绑、Claude
  默认切换和 secret provider 补偿必须完整提交或完整回滚。
- 验证期间创建的测试副本、secret-store、key 根与 evidence 临时根可以丢弃；
  不应回写任何业务库。

## 5. 回退边界

- P08 Node golden 或 Go 写入开始前失败：保留 blocked evidence，修复前置后
  在新的临时根重试。
- Go 只写了临时库或显式副本：关闭 P08 static route/feature 配置并丢弃该
  副本、secret-store 和 key 根，保持既有 IPC runtime owner。
- 已选择 HTTP mutation 但失败：向调用方暴露稳定错误，禁止 IPC 重放或伪造
  success snapshot。
- 绝不把 Go 写过的库热交给 Node/sql.js。若需回到 Node owner，必须使用
  未被 Go 写入的授权副本或从受控来源重新生成副本。
- 清理旧明文和删除不可变备份属于独立、不可逆批准点；本手册不授予该批准。

## 6. 证据审阅

每次验证创建新的 `docs/migration/p08/evidence/runs/<run-id>/`。审阅时确认：

1. `p07-gate`、`p08-safety-preflight` 分别为前两条命令；`check`、`test`
   紧随其后，任一前置失败后没有 P08 Node 或 Go writer。
2. Node static golden 在所有 Go writer 前结束，命令区间没有重叠。
3. fixture、schema、golden、分页/错误、备份和 source guard 的 SHA-256
   可复算。
4. 矩阵覆盖项目隔离、分页/关联、并发、回滚、secret provider、迁移恢复、
   HTTP/MCP/UI、runtime closure 与敏感扫描。
5. evidence 不含任何秘密、正文、tool data、session、locator、真实
   userData/workspace 或未授权绝对路径。
