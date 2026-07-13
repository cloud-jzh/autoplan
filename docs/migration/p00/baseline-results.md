# P00 命令基线与证据

## 当前状态

P005 已落地可复现驱动器、精确红灯策略和证据格式。按本阶段执行约束，本次开发修改没有启动 check、test、build 或 smoke，因此本文不伪造退出码或运行日志；首次统一验收由 `npm.cmd run migration:p00:verify` 写入一个不可覆盖的 `docs/migration/p00/evidence/runs/<UTC>-pid-<pid>/` 目录。

受控期望以 `baseline-expectations.json` 为机器权威来源。驱动器先运行全部迁移专项测试，再依次运行四条项目基线命令；每个子命令的真实退出码均单独保留。总命令只在专项测试、精确红灯比对、build、安全 smoke、来源哈希和证据前置条件全部满足时成功。

## 冻结结果

| 步骤 | 实际命令 | 期望 | 稳定失败签名 |
| --- | --- | --- | --- |
| P00 专项测试 | 当前 Node 可执行文件 `--test scripts/migration-baseline/*.test.js`（展开为确定文件列表） | 全绿 | 无 |
| check | `npm.cmd run check` | 仅接受既有精确红灯 | `file-length\|scripts/smoke-test.js\|limit=3800` |
| test | `npm.cmd test` | 全绿 | 无；任何非零均为新增红灯 |
| build | `npm.cmd run build` | 全绿 | 无 |
| smoke | `npm.cmd run smoke` | 安全前置条件满足且全绿 | 无 |

check 红灯归类为 `source-policy`：`scripts/check-file-lengths.js` 对 `scripts/smoke-test.js` 明确设置 3800 行临时上限，而当前集中 smoke 源文件静态超过该上限。签名有意忽略易漂移的实际行数，只冻结文件和受控上限。该项由 P15 遗留红灯处置门槛负责；在此之前，退出码不是零、日志不会被吞掉，新增签名、签名变化、红灯无处置消失或退出码变化都会使验证失败。

当前没有冻结 test 红灯。若统一验收发现 test 非零，驱动器会生成稳定测试名、依赖缺失或归一化输出哈希并失败，不会在首次运行时自动学习或放行。

## 证据内容

每次运行生成并哈希以下内容：

- 开始和结束的 `git status --porcelain=v1 --untracked-files=all`，以及每条命令前后的工作树差异；
- 所有受控来源在开始和结束时的 SHA-256，运行中发生来源变化即失败；
- 完整命令、UTC 起止时间、真实退出码、signal 和错误；
- 每条命令独立、未截断的 stdout 与 stderr 日志；
- 平台、架构、Node、npm 可执行文件、locale、时区和临时根摘要；环境变量值不会进入证据；
- build 前后的 `dist` 文件路径、字节数、SHA-256 和增删改差异；
- 运行目录内除自描述 manifest 外全部产物的字节数和 SHA-256。

build 写入的 `dist/` 已由 `.gitignore` 忽略，但仍作为独立目录快照记录。驱动器不调用 reset、checkout，也不删除、覆盖或恢复任何工作树文件；每次证据目录若已存在会直接拒绝覆盖。

## smoke 安全边界

运行 smoke 前会从源码重新证明以下不变量：

- `scripts/smoke-test.js` 使用 `fs.mkdtempSync(path.join(os.tmpdir(), ...))`；
- 数据库固定为该唯一临时根下的 `data/autoplan.sqlite`；
- 临时根在 smoke 自身的 `finally` 中清理；
- Agent CLI 路径通过 `loadPatchedLoopService` 的 `spawnOverride` 和 fake child 执行；
- 仅使用显式假密钥，且源码不读取 Electron `userData`、不直接导入 `child_process`。

驱动器还为 smoke 创建独立 TEMP/TMP/TMPDIR，移除名称含 key、token、secret、credential、password 或 auth 的环境变量，只记录被移除的变量名。任一源码安全断言不成立时，smoke 记录为 `blocked` 且不 spawn；由于 smoke 是序列中的最后一个高风险步骤，不会再继续任何后续高风险命令。

## 漂移判定

失败集合采用排序后的 exact-set 比对。TypeScript 错误按“相对路径 + TS 编号 + 消息”、Node 测试按测试名、长度守卫按“相对路径 + 上限”、依赖错误按模块名形成签名。不能分类的非零输出使用路径和耗时归一化后的 SHA-256，仍然失败而不是放行。

驱动器同时扫描 `src` 和 `scripts/migration-baseline` 测试文件中的 `test/it/describe.skip` 与 `.only`；发现任一控制标记，即使子命令退出零，总验证也失败。
