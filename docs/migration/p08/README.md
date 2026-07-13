# P08 静态持久化验证门禁

P08 将 Scripts、Executors、Conversations、ChatMessages、AI/Claude 配置和
MCP 静态配置迁入 Go sidecar 的静态持久化边界。它不接管执行进程、Chat
stream、队列、工具调用或 MCP listener。

统一入口：

```text
npm.cmd run migration:p08:verify
```

非 Windows 环境使用 `npm run migration:p08:verify`。该命令不接受真实
Electron `userData`、生产数据库、真实 workspace、路径参数、golden 更新、
跳过检查或继续失败前置的参数。

## 固定门禁顺序

1. 真实执行 `migration:p07:verify`，保留其实际退出码和脱敏 stdout/stderr。
   失败时结果为 `blocked`，不会启动 P08 Node golden、Go 命令或秘密迁移。
2. 执行 `scripts/migration-p08/check-safety.js preflight`。它核验最新 P07
   evidence、P00 冻结红灯签名、P08 manifest/golden/contract 哈希、schema、
   owner guard、显式副本准备器、secret provider 隔离、OpenAPI、MCP 和
   renderer 边界。
3. 真实执行 P00 `check`/`test`。`check` 的既有非零退出仅在冻结精确签名
   匹配时接受；任何新增、隐藏、重命名或未分类失败均阻断后续 writer。
4. Node static golden 只在生成器拥有的系统临时数据库中构建并与已提交
   artifact 严格比较，不写回 fixture。Node/sql.js 关闭后才允许 Go 命令开始。
5. 串行运行 Go repository/application/secrets/httpapi/MCP/迁移恢复覆盖、
   renderer 静态 transport 和 P08 编排测试。
6. 写入新的不可覆盖 evidence run，并只清理本次创建的
   `autoplan-p08-verify-*` 系统临时根。

任一 gate、fixture/schema、owner、授权副本、秘密隔离或安全检查失败时，
验证器记录实际退出码并停止，不跨阶段补跑 mutation。

## 安全范围

- 验证器只创建系统临时目录中的数据库、Node/Go 黄金副本、secret-store、
  secret-key、备份、sentinel、用户目录镜像和 Go cache 根；不会自动发现、
  打开或修改真实 profile、生产数据库、workspace、用户目录或 Go cache。
- Node/sql.js 与 Go 的写入区间必须串行。Go 使用 owner proof 和
  authorized copy；任何 Go 写过的副本不得热交还 Node/sql.js。
- OS 凭据库是优先 provider；仅在显式策略允许且 OS provider 不可用时使用
  独立安装级 AEAD fallback。locked、denied、corrupt 不能降级为明文。
- 所有 evidence 只记录相对路径、稳定错误码、命令、时间、退出码、计数和
  SHA-256，不记录数据库行、请求正文、消息正文、tool data、秘密、locator
  或真实绝对路径。

详细操作见 [runbook.md](./runbook.md)，证据结构与审阅要求见
[evidence/README.md](./evidence/README.md)。
