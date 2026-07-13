# P15 文件清单

| 领域 | 当前保留/目标 | 替代路径 | 删除门禁 |
| --- | --- | --- | --- |
| Electron native shell | `src/main.js`、`src/preload.js` | DesktopBridge、更新、sidecar 生命周期 | 保留；仅允许 IPC 白名单 |
| Go sidecar | `backend/cmd/autoplan-server`、`resources/sidecar` | Go application/repository | 三平台 binary manifest 与包内验证 |
| Node/sql.js | `src/database.js`、`src/data` | Go SQLite repository | P006 blocked，禁止删除或回退 |
| Loop/Plan/Task | `src/loopService.js`、`src/loop` | Go Runtime REST/SSE/application | P005/P006 等价证据 |
| Chat/MCP | `src/chat`、`src/mcpServer.js`、`src/mcpTools.js` | Go Chat REST/SSE、Go MCP | P005/P006 等价证据 |
| Scripts/Executors/Terminal | `src/executors`、`src/terminal`、`src/agentCli.js` | Go process/runtime、Terminal WebSocket | P005/P006 与进程树证据 |
| Intake/attachments/files | `src/intakeService.js`、`src/attachments.js`、`src/fileAccess` | Go Intake/attachment/files services | 副本迁移、审计与恢复证据 |

当前所有删除候选均保留。`legacy-removal-manifest.json` 是唯一可审计的删除状态来源；除非其 `deletion_authorized` 为 true 且证据清单完成，禁止按“无引用”删除任何模块。
