# P00 及后续阶段证据规范

本目录保存迁移的可复现证据定义。P00 的机器格式由 [manifest.json](./manifest.json) 固定；实际运行目录由 `npm.cmd run migration:p00:verify` 创建。当前开发文档不伪造尚未执行的命令、退出码或哈希。

## 证据原则

1. **原始结果优先**：保存完整命令、UTC 起止时间、真实退出码、signal、未截断 stdout/stderr；汇总状态不能覆盖子命令非零退出码。
2. **不可覆盖**：每次运行使用新的 UTC + PID 目录。目标已存在即拒绝，不复用“latest”覆盖历史。
3. **来源可证明**：记录来源文件的相对路径、字节数和 SHA-256，并在命令前后复核；运行中来源变化即失败。
4. **工作树可解释**：开始/结束及每条命令前后记录 git status、文件内容哈希和 `dist` 差异；不得 reset、checkout、clean 或恢复用户改动。
5. **默认无秘密**：不记录环境变量值、认证 header、密钥、token、`env_vars`、Terminal 环境、真实用户内容或未授权绝对路径。必须保留的需求来源绝对路径只存在于 P00 索引，不复制进每次运行证据。
6. **安全数据源**：fixture、数据库和 smoke 只使用系统临时目录或明确授权副本，不读取真实 Electron userData，不允许 Node/sql.js 与 Go 同库写入。

## P00 运行目录

```text
docs/migration/p00/evidence/runs/<UTC>-pid-<pid>/
├── git-status-start.json
├── git-status-end.json
├── dist-start.json
├── dist-end.json
├── 01-specialized.stdout.log / .stderr.log
├── 02-check.stdout.log / .stderr.log
├── 03-test.stdout.log / .stderr.log
├── 04-build.stdout.log / .stderr.log
├── 05-smoke.stdout.log / .stderr.log
├── summary.json
└── evidence-manifest.json
```

`summary.json` 保留每个子命令的真实退出码、稳定失败签名、环境摘要、受影响文件、工作树/dist 差异、来源哈希和判定理由。运行内 `evidence-manifest.json` 对除自身外的全部产物记录字节数和 SHA-256；仓库内 [manifest.json](./manifest.json) 定义必需文件和隐私规则。

## 每阶段最小证据包

P00–P15 的每个阶段都必须提供以下六类内容，缺一项即不能标记通过：

| 类别 | 必需内容 |
| --- | --- |
| 代码与文档 | commit/来源标识、阶段范围、ADR/契约变更、受影响文件；明确无关用户改动未触碰 |
| 自动化测试 | 新增/调整的单元、契约、集成、迁移和安全测试；禁止 skip/only 或放宽断言 |
| 实际命令 | 完整 argv、工作目录摘要、UTC 起止时间、实际退出码、stdout/stderr 原始日志 |
| 数据与运行证据 | fixture/副本来源哈希、schema/行数/完整性、进程/owner、事件顺序、备份恢复结果 |
| 完整性 | 所有证据文件的相对路径、字节数、SHA-256，命令前后来源/git/dist 差异 |
| 风险与决策 | 剩余风险、负责人、触发条件、回滚点、不可逆点和未处置红灯 |

建议每阶段的汇总至少包含以下字段：

```json
{
  "stage": "Pxx",
  "status": "passed | blocked",
  "source_sha256": [],
  "commands": [],
  "tests": [],
  "affected_files": [],
  "data_and_process_evidence": [],
  "gate_approvals": [],
  "remaining_risks": [],
  "rollback_point": "",
  "artifacts": []
}
```

`passed` 只表示该阶段所有退出条件和前置门禁均有证据。没有运行、等待批准、证据缺失、环境失败或安全前置条件不满足时统一使用 `blocked`，不能使用“基本通过”“预期通过”或空成功记录。

## 三个高风险门禁证据

### G1：真实 Electron userData 所有权切换

必须由 Migration Owner、Data Owner、Release Owner 和 Security Owner 对各自项目署名/留痕确认，并保存：

- Node writer 停止接收 mutation、活动任务排空、`AppDatabase` 关闭及文件句柄释放；
- 主库、mirror/bak、附件和 Plan 文件的一致性备份，字节数、SHA-256、schema、关键行数、最大 ID、orphan/异常路径审计；
- 备份在隔离临时路径恢复并通过完整性、snapshot 和契约复验；
- Go 单实例、排他锁、PID/实例 ID、监听 `127.0.0.1` 和 writer owner 证据；
- 切换失败时停止 Go、验证库未被写入、释放所有权并恢复 Node 的演练记录。

任一项缺失时 G1=`blocked`，不能进入真实库切换。

### G2：首笔 Go 正式写入

在 G1 通过后单独记录首笔 mutation 的 operation/request ID、开始/提交时间、前后 schema/行数/业务快照、Go owner 和副作用结果。Data Owner 确认数据一致性，Migration Owner 确认 Node writer 已永久禁止热开现库，Release Owner 确认可部署的兼容 Go 回退版本，Security Owner 确认会话/路径/秘密策略。

G2 证据必须引用 G1 一致性备份的 manifest 与 SHA-256，保存首笔写入后的完整性复核，并再次证明只有一个 Go PID/实例 ID 持有 owner/数据库句柄、Node 句柄数为零。任一引用、完整性或单实例证据缺失时不得提交首笔写入。

G2 后不能直接恢复 Node writer。失败恢复证据必须展示：冻结新写入、保全现场、回滚兼容 Go sidecar 或前向修复；若恢复切换前备份，必须记录将丢弃的数据区间和负责人决定。

### G3：删除旧 Node writer

必须保存全域观测窗口、零旧路由使用、Node 不再打开业务库的源码/依赖审计、Go 冷启动与崩溃恢复、备份恢复演练、打包安装、上一版 Go 回滚/前向修复 runbook，以及 P15 check/test 红灯清零或逐项处置证据。

删除前重新生成当前 Go 生产库及文件副作用清单的备份 manifest/SHA-256，完成隔离恢复、schema/行数/snapshot 完整性复验，并证明正常启动、更新和 crash recovery 后始终只有一个 Go writer 实例。四类 Owner 必须分别确认迁移代码删除范围、数据恢复、发布回退和安全边界。

G3 执行中任一删除/打包/恢复检查失败时，在尚未发布删除版本前撤销该发布候选并保留旧代码，不改变 Go 数据 owner；删除版本发布后恢复只允许回滚 Go 二进制、前向修复或恢复 Go 备份。历史 Node/sql.js 代码不能作为热开当前生产库的恢复方案。

## 失败签名与红灯

check/test 的已知失败按排序后的 exact-set 比较。新增失败、签名内容变化、退出码变化、无法分类的非零输出，或既有红灯消失但没有处置记录，都使阶段失败。P15 前每个暂存红灯都要持续显示真实退出码和 owner；P15 必须清零，或记录负责人、理由、风险和到期日，禁止静默继承。

## 清洁环境复现

复现使用干净工作树副本、已锁定依赖和系统临时根；先验证来源 SHA-256，再生成确定性 fixture，最后运行阶段命令。任何需要真实 userData、真实密钥、真实 CLI 凭据或与活动 writer 共用数据库才能“通过”的证据无效。

P00 的统一入口仍是：

```powershell
npm.cmd run migration:p00:verify
```

各阶段在此基础上追加自己的契约、迁移、集成、打包和恢复命令，但不能删除 P00 漂移检查或用阶段总命令成功掩盖子命令失败。
