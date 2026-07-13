# P15 第 15、19 节证据矩阵

状态说明：`passed` 仅表示可验证的真实命令、退出码、脱敏摘要、哈希、平台和源修订齐全；`blocked` 表示未满足，绝不等价于跳过或通过。

| 来源 | 场景与证据 | 所需平台/fixture | 当前状态 | 阻塞原因 |
| --- | --- | --- | --- | --- |
| 15.1 自动化 | Go domain/application、SQLite 集成、httptest/OpenAPI、Node/renderer contract 与 snapshot | 隔离临时目录、P09–P14 证据 | blocked | 前序不可变 accepted evidence 未齐全 |
| 15.2 数据迁移 | 行数/关联、孤儿审计、附件和路径解析、`integrity_check`、中断与备份恢复 | 授权数据库副本 | blocked | 未采集真实迁移/恢复 run |
| 15.3 安全 | Origin/session、路径穿越、大小写、符号链接、cwd/workdir、输出边界和秘密扫描 | synthetic fixture | blocked | Electron-Go 安全 E2E 未执行 |
| 15.4 功能 | CRUD、Loop、Task、Chat、MCP、Scripts、Executors、Terminal 的 UI/REST/MCP/实时等价 | synthetic Electron-Go fixture | blocked | 运行驱动和结果摘要缺失 |
| 19 拓扑 | renderer→Go REST/SSE/WS、唯一 writer、无业务 IPC、无 Node/sql.js writer | P15 topology fixture | blocked | P005 业务 IPC 注册仍存在 |
| 19 sidecar | 随机端口、会话、readiness、完整进程树清理 | 三平台临时运行目录 | blocked | 三平台 sidecar 运行证据缺失 |
| 19 发布 | 包内 binary、权限、Windows 签名、macOS hardened runtime/notarization/staple、Linux 冒烟 | Windows/macOS/Linux 原生 runner | blocked | 未生成或验证签名产物；默认 publish never |
| 19 恢复 | 兼容版本、备份校验、恢复演练、失败安全关闭 | 授权副本与恢复包 | blocked | 恢复包与演练证据缺失 |

矩阵中的每一行必须关联 `evidence/manifest.json` 的条目，条目必须包含：稳定场景 ID、平台、fixture/copy 授权、实际命令的脱敏表示、原始退出码、开始/结束时间、源提交或可构建标签、日志/产物的相对路径与 SHA-256。任何一个字段缺失均为 `blocked`。
