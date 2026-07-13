# ADR-0002：数据库单 writer、所有权切换与恢复边界

- 状态：Accepted
- 日期：2026-07-11
- 决策范围：sql.js → Go 持久化迁移及真实 userData 门禁
- 关联基线：[数据库与依赖审计](../database-and-dependencies.md)、[fixture 配方](../../../../fixtures/migration/p00/readme.md)、[ADR-0001](./0001-local-go-sidecar.md)、[ADR-0004](./0004-contract-files-flags-and-rollback.md)

## 背景

当前 `AppDatabase` 用 sql.js 把整个 SQLite 数据库加载到内存。普通写入会 export 并持久化整个文件，`runBatch` 只保证内存事务，文件落盘还涉及临时文件、mirror 和 `.bak`；Windows 路径使用 copy 覆盖而不是原子 rename。数据库没有外键，附件、Plan 文件、运行中进程和表内关系由应用代码协调。因此 SQLite 自身锁并不足以防止两个运行时以不同内存快照覆盖同一文件。

## 决策

任意时刻，每个数据库文件只有一个业务 writer 和一个负责 schema/backfill 的 owner。迁移前是 Node/sql.js，切换后是 Go。禁止 Node/sql.js 与 Go 同时写入同一个数据库；也禁止旧 Node 以“只读检查”为名热打开已由 Go 接管的生产库，因为旧初始化和兼容迁移本身可能写 schema 或回填。

“生产库”包括真实 Electron `userData` 中的 `autoplan.sqlite` 及其 mirror、bak、临时文件和同一数据集的正式替代路径。复制出来且具有独立绝对路径、独立锁和明确用途的数据库是另一个数据集，不等于共享生产库。

## 开发与迁移数据规则

- 日常开发、自动化测试、迁移演练只允许使用 P00 脱敏 fixture、系统临时库，或用户明确选择后生成的只读源副本。
- 显式副本必须先停止源 writer 或从一致性备份生成；副本名称、来源哈希、生成时间和用途写入证据。不得把真实 userData 数据提交到仓库、fixture 或日志。
- Go 的 shadow read、schema 试算和 backfill 演练默认针对副本，不能在 Node 活跃写入时读取同一个 sql.js 文件并假设快照一致。
- 任一工具若无法证明目标不是活动 userData、无法取得唯一所有权或检测到现有锁/进程，必须默认拒绝。

## 写所有权状态机

```text
NODE_OWNER
  → QUIESCING
  → VERIFIED_BACKUP
  → GO_OWNER_NO_PRODUCTION_WRITE
  → GO_OWNER_WRITTEN
  → NODE_WRITER_REMOVED
```

状态只能单向前进。失败时采用下文允许的恢复路径，不能跳过中间证据。

### 1. `NODE_OWNER`

Node/sql.js 是唯一 writer。Go 只能操作 fixture、临时库或一致性副本。此时域 flag 可以做无副作用的 contract/shadow 对比，但不得把 shadow 结果写回生产库或生产文件。

### 2. `QUIESCING`

切换开始后先阻止新 mutation，再完成以下动作：

1. 停止 Loop 调度，排空或取消 Operation、Chat 队列、Script/Executor/plugin 和会写状态的后台任务；Terminal 可保留视图但不得再写业务状态。
2. 禁止新的 HTTP、MCP、UI 和旧 IPC 写命令，等待已接受命令到达明确终态。
3. 调用 Node owner 的关闭流程，确保 sql.js 最后一次 persist 完成，随后释放数据库、mirror、bak 和临时文件句柄。
4. 记录 Node PID、打开时间、最后写序号/时间、活动任务清单及关闭结果。无法证明已关闭即终止切换。

### 3. `VERIFIED_BACKUP`

在没有 writer 的窗口中创建不可覆盖的一致性备份，并记录：

- 主库及相关恢复文件的路径标识、字节数和 SHA-256；
- schema/迁移版本、所有表与索引、关键行数、最大 ID、UTC 时间与默认值检查；
- P00 审计定义的 orphan、跨项目关系、异常路径和敏感列计数，不能在备份阶段“顺手修复”；
- SQLite 可打开性与完整性检查、应用级 snapshot/契约抽样；
- 附件和 Plan 文件清单及缺失项，明确它们与数据库备份不是同一原子边界；
- 备份恢复到另一个临时路径后的复验结果。

任一哈希、行数、schema、恢复或文件清单校验失败，都回到无 writer 的 blocked 状态；不得启动 Go writer。

### 4. `GO_OWNER_NO_PRODUCTION_WRITE`

Go 取得排他所有权，完成 schema/backfill 并再次校验，但尚未接受首笔正式 mutation。此窗口内若验证失败，可以先完全停止并关闭 Go，证明主库与切换备份仍一致，然后恢复 Node owner。Node 恢复前必须确认没有 Go 进程、连接、后台任务或文件句柄。

### 5. `GO_OWNER_WRITTEN`

Go 接受首笔正式写入的时刻是不可逆门禁。此后：

- Go 是生产库唯一 owner；旧 Node writer 永久禁止热打开该库。
- 关闭某个 Go API flag不能把数据库写入直接切回 Node，只能把客户端路由到仍由 Go application service 执行的兼容 adapter，或回滚到兼容的上一版 Go sidecar。
- 数据恢复必须先停止 Go、备份故障现场，再使用经过演练的 Go 版本迁移/恢复工具或从切换备份恢复。恢复切换备份意味着丢弃门禁后的正式写入，必须作为显式数据恢复决定记录，不能静默执行。
- 若确需重新引入 Node writer，必须离线导出为 Node 明确认识的 schema、在副本上完整验收并建立新的所有权迁移；这不是 feature flag 回退。

### 6. `NODE_WRITER_REMOVED`

删除旧 Node writer 是第二个不可逆工程门禁。只有 ADR-0004 的观测、恢复演练和证据条件全部满足后才能进入。删除后恢复策略是修复/回滚 Go sidecar 或恢复 Go 备份，不是从历史安装包抽取 Node/sql.js 并打开现库。

## 排他所有权实现要求

Go 与 Node 都必须使用同一个应用级所有权标识和 OS 可验证的排他机制；仅依赖“我们不会同时启动”不够。所有权记录至少包含数据库规范路径、owner 类型、实例 ID、PID、启动时间和 schema 版本。陈旧标识只能在确认原进程不存在、文件句柄关闭且恢复证据完整后人工/受控清理。

数据库 owner 还负责串行化 schema/backfill 和业务 mutation。HTTP、MCP、UI、定时任务与后台 worker 不可各自持有绕开 application service 的连接或写队列。

## 回滚触发与动作

以下任一情况阻断切换或触发恢复：无法排空 writer、备份不可恢复、schema/行数/hash 漂移、orphan 数量意外变化、snapshot 契约漂移、Go 获取排他所有权失败、写后出现丢失/重复/跨项目数据，或发现 Node 与 Go 同库句柄。

- 首笔 Go 正式写入前：停止 Go，校验库仍等于受控切换状态，释放所有权，再恢复 Node。
- 首笔 Go 正式写入后：保持 Node 禁用，停止新命令并保全现场；优先回滚 Go 二进制或前向修复。需要恢复备份时记录数据截断点、受影响 mutation 和负责人决定。
- 任何阶段发现双 writer：立即停止接收写入，按最保守方式终止两个 writer，复制现场文件后再判定权威版本；不得让任一方继续 persist 覆盖证据。

## 结果

该决策牺牲了热切换便利，换取可证明的数据所有权和可审计恢复边界。迁移步骤必须安排短暂只读/不可用窗口，并把数据库、附件/Plan 文件和外部进程分别验证。任何“为了快速回退”而允许旧 Node 与 Go 同库、在 Go 写后直接翻回 Node flag、或跳过恢复演练的实现均违反本 ADR。
