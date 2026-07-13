# P00 Go 后端迁移基线索引

P00 把当前 Electron/Node/sql.js 行为冻结为可机器检查、可复现、可审计的迁移起点。它不迁移业务实现、不改变 renderer 状态模型，也不接触真实 Electron `userData`。后续阶段必须引用本索引中的受控清单和 ADR，不能以新的非受控文档替代机器基线。

## 来源与完整性

- 来源规划稿：`D:\docs\autoplan-go-backend-separation-plan.md`，v1.0，2026-07-11。
- 持久化附件：`C:\Users\98000\AppData\Roaming\autoplan\data\attachments\requirement\7\1783710392772-387701ce3b10-c12198aaf47c-autoplan-go-backend-separation-plan.md`。
- 来源 SHA-256：`c12198aaf47cd3bef0a1b79b8d3e73363a52da45a01b2b7a4c1597f11e5a93ef`。

上述绝对路径只作为需求来源的审计标识保留。迁移脚本、测试和证据驱动器不得读取持久化附件、真实 userData 或来源数据库；复现依赖仓库内的受控清单、合成 fixture 和临时目录。

## 基线入口

| 基线 | 机器权威 | 说明 |
| --- | --- | --- |
| IPC、renderer 与事件能力 | [capability-matrix.json](./capability-matrix.json) | [阅读说明](./capability-matrix.md)；唯一 owner、目标 transport、阶段、flag 与回退 |
| DTO、snapshot、状态机与副作用 | [contract-baseline.json](./contract-baseline.json) | [阅读说明](./contract-baseline.md)；字段、nullability、排序、错误、事件顺序与副作用 |
| 数据库与外部依赖 | [database-and-dependencies.json](./database-and-dependencies.json) | [阅读说明](./database-and-dependencies.md)；schema、backfill、敏感列、文件/CLI/进程/PTY |
| 五类脱敏 fixture | [fixture manifest](../../../fixtures/migration/p00/manifest.json) | [生成与安全说明](../../../fixtures/migration/p00/readme.md)；空库、旧库、孤儿、异常路径和大数据量 |
| 命令与既有红灯 | [baseline-expectations.json](./baseline-expectations.json) | [基线结果说明](./baseline-results.md)；真实退出码、精确失败签名和不可覆盖证据 |
| P00 证据定义 | [evidence/manifest.json](./evidence/manifest.json) | [证据规范](./evidence/readme.md)；日志、git/dist、来源哈希和阶段状态 |
| P00→P15 路线 | [p00-p15-roadmap.md](./p00-p15-roadmap.md) | 单向依赖、阶段门禁、feature flag、退出与回滚 |

## 架构决策

1. [ADR-0001：本机 Go sidecar 与 Electron 薄壳](./adr/0001-local-go-sidecar.md)——仅监听 `127.0.0.1`，Electron 只保留窗口/应用生命周期、sidecar、原生选择/打开和更新安装。
2. [ADR-0002：数据库单 writer](./adr/0002-single-database-writer.md)——Node/sql.js 与 Go 不得同写同库，首笔 Go 正式写入后不能直接切回 Node writer。
3. [ADR-0003：REST/SSE/WebSocket 与共享服务](./adr/0003-rest-sse-websocket-and-application-service.md)——查询/CRUD/命令、单向事件和 Terminal 双向流各有唯一 transport，UI/MCP/HTTP 复用 application service。
4. [ADR-0004：契约、文件安全、flags 与回滚](./adr/0004-contract-files-flags-and-rollback.md)——保持既有 DTO/状态/副作用，realpath/会话/Origin 默认拒绝，并固定三个高风险门禁。

## P00 冻结结论

- 业务能力的目标 owner 是 Go API；系统 dialog/shell、更新、安装和 sidecar 生命周期永久归 `DesktopBridge`。旧 IPC 只是 flag 关闭时的临时兼容路径，不是第二 owner。
- Go 采用本机模块化单体；REST/JSON 负责查询、CRUD 和命令，SSE 负责 snapshot/patch、Operation、Chat 和配置事件，WebSocket 负责 Terminal。
- 当前 Node/sql.js 是生产库唯一 writer。开发和迁移只使用脱敏 fixture、系统临时库或一致性显式副本。
- snake_case、兼容双字段、完整 snapshot、字段存在性 patch、状态机、错误结果、事件顺序和业务副作用在迁移期间保持不变。
- 密钥、token、`env_vars`、Terminal 环境值、未授权绝对路径和真实用户数据不得进入 API、日志、事件、fixture 或证据。
- `npm.cmd run check` 只冻结 `file-length|scripts/smoke-test.js|limit=3800` 这一既有失败签名；任何新增、变化或未处置消失均失败。`npm.cmd test` 没有被自动放行的红灯。

## 复现与验证

最终统一入口是：

```powershell
npm.cmd run migration:p00:verify
```

该命令先运行全部 `scripts/migration-baseline/*.test.js` 专项测试，再依次执行 check、test、build 和安全 smoke。它在开始/结束记录 git status 和来源哈希，为每个命令保存完整 stdout/stderr、真实退出码、受影响文件、dist 变化和 SHA-256，并只允许精确冻结的既有 check 失败。

P00 开发任务按计划不在各任务内启动测试或构建。首次统一验收产生的实际证据写入不可覆盖的 `docs/migration/p00/evidence/runs/<UTC>-pid-<pid>/`。在证据尚未产生或任一门禁不满足时，阶段状态只能是 `blocked`，不能用本文描述代替实际命令结果。

## 后续阶段约束

每个 P01–P15 阶段都必须提交代码/文档、自动化测试、实际命令与退出码、证据哈希、受影响文件和剩余风险。阶段只能沿 [路线图](./p00-p15-roadmap.md) 单向进入；真实 userData 所有权切换、首笔 Go 正式写入、删除 Node writer 三个门禁不得由 feature flag、口头确认或“测试大致通过”替代。

P15 是遗留红灯最终处置点：冻结的 check/test 失败必须清零，或逐项形成有负责人、理由、风险和期限的明确决定。未记录的继承、skip/only、放宽断言或吞掉退出码均不构成完成。
