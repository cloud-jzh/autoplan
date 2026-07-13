# P09 脱敏规模副本

`manifest.json` 定义 P09 的确定性近真实规模数据形状。生成器只写入由调用方指定的 fixture/temp 输出目录，生成 `scale-copy.json` 和带行数、哈希、seed、schema 与清理规则的 `scale-manifest.json`；不读取或复制 Electron `userData`、真实 workspace、SQLite、`.bak` 或 `.mirror`。

副本覆盖深分页、同时间戳排序、长计划、历史事件、排队/中断消息、多脚本/执行器以及明确标记的异常关系。所有文本、路径和标识均为合成数据，且生成器会拒绝 credential、token、secret、真实 userData 路径和符号链接输出目录。

示例（仅未来验收阶段执行）：

```powershell
node scripts/migration-p09/generate-scale-copy.js --output-dir <fixture-temp-output>
node scripts/migration-p09/verify-legacy-runtime.js --copy <fixture-temp-output>\scale-copy.json
```

退出码 `0` 表示受控副本验证完成；`2` 表示路径、敏感扫描、所有权或兼容性门禁失败。清理必须显式调用生成器的 `--cleanup`，并且只会删除带完整生成标记的安全输出目录。
