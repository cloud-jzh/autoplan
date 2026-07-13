# P06 Intake 与附件阶段门禁

P06 将 Requirements、Feedback、附件和 `intake_plan_links` 迁入 Go sidecar 的共享 application service。HTTP、MCP 和 renderer 只能经该服务访问已迁移能力；附件对外只提供 `id`、`display_name`、`size`、`mime_type` 和受控 `download_url`。

统一门禁命令：

```text
npm.cmd run migration:p06:verify
```

非 Windows 环境可使用 `npm run migration:p06:verify`。该命令不接受更新 golden、跳过检查、指定真实数据库或指定附件根的参数。

## 固定门禁顺序

1. 运行 P05 的真实门禁，并保留其实际退出码、stdout/stderr 脱敏副本。
2. 校验最新 P05 成功 evidence、P06 冻结契约、fixture/manifest 哈希、schema/checksum、Files policy、数据库 owner guard、OpenAPI、renderer 受控 URL 和 MCP 路径输入边界。
3. Node/sql.js 仅在生成器拥有的系统临时数据库和附件根中串行生成 Intake golden；生成器关闭数据库并释放 writer 后，才允许任何 Go 专项命令开始。
4. 串行运行 Go repository、Intake application、attachments、HTTP、MCP、filesystem，随后运行 renderer transport、黄金比较器、P06 编排自身测试及 P00 `check`/`test` 基线比较。
5. 写入不可覆盖的脱敏 evidence run，再清理仅由本次验证器创建的系统临时根。

P05 门禁、P05 evidence、冻结输入、Files policy、owner guard、测试控制扫描任一不通过时，P06 状态为 `blocked`。此时不会运行 Node golden、Go writer、附件动作或后续跨阶段步骤。

## 安全范围

- 验证器只创建名称为 `autoplan-p06-verify-*` 的系统临时根，并仅清理该前缀且位于系统临时目录下的本次根。
- 禁止传入或自动发现 Electron `userData`、生产 `autoplan.sqlite`、home/profile 数据库、真实附件根或未授权目录。
- 运行时的数据库副本必须由调用方显式授权，并由 P05 owner proof 保护；验证器不会从环境变量、当前目录或用户配置推断副本路径。
- Node/sql.js 与 Go 不得同时写同一副本；P06 仅认可 Node 已关闭后进入 Go 的单调时间线。
- 证据可记录命令、退出码、时间、相对路径、占位符、哈希、稳定错误码和 scenario ID；不得记录数据库内容、附件 bytes、正文、凭据、token、`stored_path`、`file://` 或真实绝对路径。

详细恢复处置见 [runbook.md](./runbook.md) 与 [attachment-recovery.md](./attachment-recovery.md)。evidence 结构和审阅要求见 [evidence/README.md](./evidence/README.md)。
