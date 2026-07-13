# P09 前置门禁与统一验证

P09 只允许在显式生成、脱敏且无活跃 writer 的近真实规模副本上执行。它不读取 Electron `userData`、持久化 `autoplan.sqlite`、会话目录或用户主目录，也不允许 Node/sql.js 与 Go 同时取得 writer 所有权。

统一入口：

```powershell
npm.cmd run migration:p09:verify -- --fixture-root <绝对路径的已生成脱敏规模副本>
```

`<fixture-root>` 必须是 P07 `generate-scale-copy.js` 创建的目录，且包含 `.autoplan-p09-scale-copy`、`scale-copy.json` 与 `scale-manifest.json`。路径位于系统临时目录，或名称明确为 fixture/sanitized/temp 的受控目录；任何真实 `userData`、符号链接、活动数据库、lock/WAL/SHM/journal 文件都会被拒绝。

## 不可跳过的顺序

1. 先执行 `check-prerequisites.js`，复核 P00 精确冻结的 `check` 红灯签名、`test` 成功结果、P04–P08 的不可变完成证据、显式副本授权和空闲 writer。
2. 顺序重新验证 P04–P08；前置步骤失败即记录原退出码并停止，绝不启动后续切换或演练。
3. 仅在系统临时目录中运行 Node 数据库审计、GoDataClient 契约、daemon readiness、维护切换/备份恢复、脱敏规模生成、旧运行时兼容性和故障演练。
4. `legacy-runtime-godata` 验证 Loop、Chat、脚本钩子和执行器经 GoDataClient 运行，同时确认 Node SQL 被拒绝、第二个 Go owner 被拒绝、writer 数量为一。
5. `cutover-recovery-drill` 覆盖维护 lock、drain、备份恢复、故障注入、进程/句柄清理；临时规模副本只能由最后的清理步骤删除。

命令返回 `0` 仅表示所有门禁和验证成功；`2` 表示 blocked 或 failed。P00 的既有 `check` 红灯是精确冻结的签名，而非被忽略的失败：签名变化、消失或被 `skip/only`、重命名、吞掉退出码替代时均会阻断。

## 证据与风险

每次运行以不可覆盖目录写入 `docs/migration/p09/evidence/runs/<run-id>/`。其中包含实际命令、开始/结束时间、原始退出码、经过脱敏的 stdout/stderr、输入哈希、受影响源文件哈希、规模副本 schema/行数/表哈希、owner 时间线、临时副本清理结果和演练报告哈希。

原始秘密、token、cookie、session、用户数据、绝对用户路径和未授权路径一旦出现在输入、命令输出或准备写入的证据中，运行即失败关闭。证据不会保存原始数据库行或用户内容。阶段仍保留的风险会在 `summary.json` 的 `remaining_risks` 中记录；P15 对 P00 冻结红灯的正式处置仍是独立风险。
