# P02 验证证据

`npm.cmd run migration:p02:verify` 每次在 `docs/migration/p02/evidence/runs/<UTC>-pid-<pid>/` 创建不可覆盖的运行目录。不得手工制造成功 summary、改写既有 run，或用空日志替代失败命令。

每个 run 包含：

- 每条实际命令的脱敏 stdout/stderr 日志；
- `summary.json`：环境摘要（不含值）、前置门禁、真实退出码/信号、失败签名、秘密扫描结果、源哈希、git 状态、受影响文件、临时目录清理和剩余风险；
- `evidence-manifest.json`：证据文件的字节数与 SHA-256。

日志写盘前会替换仓库根、用户目录、系统临时目录和凭据形状值。原始环境值不会进入 summary；凭据形状变量名只记录被移除的名称。检测到可用密钥、Bearer、私钥或凭据赋值时，即使命令退出码为 0 也判定失败。

P00 或 P01 非零时，summary 状态必须为 `blocked`，后续 Go/Node P02 命令不会启动，原始非零退出码仍保留。完成状态也只有在所有命令符合冻结预期、无 skip/only、源文件运行期间未漂移、git status 可用且系统临时根成功清理时才可 `ok: true`。

证据目录只记录验证结果，不是运行时数据目录，不得放入数据库副本、Electron `userData`、会话材料、请求正文或真实用户文件。
