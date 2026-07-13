# P03 Node projects / snapshot 黄金输出

本目录保存从 P00 脱敏配方派生的 Node 规范输出，不保存 SQLite 数据库。生成入口为 `scripts/migration-p03/generate-node-golden.js`，规范化规则为 `scripts/migration-p03/normalize-contract.js`。

## 来源与语义

默认流程先验证 P00、P01、P02 最新不可变 evidence、实际完成状态、证据清单哈希以及冻结源码哈希。任一 evidence 缺失、命令未被接受、清单损坏或源码漂移时，进程以 `blocked: <稳定原因码>` 失败；此时不会创建临时数据库、黄金文件，也不会启动 Go。

门禁通过后，生成器在真实系统临时目录运行 P00 fixture 生成器，以 `empty.sqlite` 当前 schema 为基底，加入三个仅含合成内容的 project 与两条 project state。随后重新打开派生库，通过现有 `LoopService.projects()`、`LoopService.snapshot()`、有效 project snapshot 和不存在 project snapshot 读取契约。`LoopService` 启动时允许的运行态复位语句必须实际修改零行，否则按疑似活跃数据库安全失败。sql.js 关闭后才提交黄金 JSON，临时根无论成功失败都会清理。

三个输出分别覆盖：

- `projects.golden.json`：`updated_at DESC, id DESC` 排序、state 摘要合并，以及缺失 state 的默认摘要；
- `snapshot-empty.golden.json`：无 project id 时保留项目列表的完整空 `AppSnapshot`；不存在项目必须与它结构化相等；
- `snapshot-project.golden.json`：有效项目、完整 state、nullable/default、UTC RFC3339、空的未迁移业务数组、MCP 安全状态，以及 Claude 配置和 `env_vars` 的不可逆掩码。

`manifest.json` 记录 P00 配方、派生数据库、规范化源码、必须完成的阶段集合和每个黄金文件的 SHA-256。具体 evidence run id、运行时间与动态证据哈希不写入黄金文件；生成器仍在内存中逐项核验其清单及冻结源码哈希，避免相同源码因不同验收批次产生无意义的 manifest 漂移。项目自增 ID 使用同一 project 映射表稳定化；null、布尔、数字、枚举、默认字段和数组顺序均保留。临时根和保留的合成 workspace 根替换为固定占位符，运行时 session 使用稳定映射，非 UTC 时间会被规范成 UTC；未知绝对路径、非有限数字或无法安全规范化的值直接失败，不通过删字段制造相等。

## 安全边界

默认模式只读取 P00 生成器在本次系统临时根中创建的数据库。调用方副本模式必须同时提供绝对 `--database`、绝对 `--allow-root` 和 `--sanitized-copy`；目标必须位于授权根内、是普通 `.sqlite` 文件且没有符号链接、WAL/SHM/journal/lock 旁路文件。名为 `autoplan.sqlite` 的数据库和 Electron userData 路径即使显式授权也拒绝。

生成日志只输出稳定状态和产物文件名，不输出数据库、仓库、HOME、userData 或 workspace 的绝对路径。JSON 在提交前扫描本机路径、可用 provider 凭据、Bearer 值和私钥形态；检测命中时删除本次临时产物并安全失败。黄金文件只保留 `····mask`、`<redacted>`、`<redacted-env-vars>` 等不可用占位，不保存原始认证材料或环境变量值。

## 生成与清理

最终阶段由统一 P03 验证入口执行默认生成：

```text
node scripts/migration-p03/generate-node-golden.js
```

显式脱敏副本模式：

```text
node scripts/migration-p03/generate-node-golden.js --database <authorized-copy.sqlite> --allow-root <authorized-root> --sanitized-copy
```

生成器没有 `--force`、跳过门禁或保留临时数据库选项。成功时只更新本目录的三个黄金 JSON 和 manifest；README 不由生成器改写。失败时保留既有已提交黄金文件，并仅清理由本次进程创建且名称受控的系统临时根。
