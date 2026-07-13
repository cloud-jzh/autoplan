# P04 脱敏迁移夹具

本目录只保存确定性配方，不保存生成后的 SQLite 二进制。`manifest.json` 固定历史阶段、异常类别、源 `user_version`、预期目标版本、结果和稳定错误码；生成器在调用方明确提供的系统临时目录中写入数据库，并生成 `generated-manifest.json`，逐文件记录字节数与 SHA-256。

夹具数据均为合成值：固定 UTC 时间、仓库相对占位路径、非凭据文本和空秘密字段。配方禁止写入真实用户目录、Electron `userData`、环境变量、聊天或日志正文、可用 token 及真实绝对路径。

覆盖范围包括：

- 空文件、空 SQLite、初始单项目、`ensureColumn` 中间态、旧 `scan_files` 主键；
- 缺少 intake links、项目级 AI 配置、缺少 conversation 归属、当前 Node schema 与 schema v1；
- Unicode、边界 ID、合法 null/default、多项目引用与确定性计划聚合；
- 孤儿关系、路径遍历/UNC、外键冲突、checksum/schema 漂移、损坏页和截断文件。

生成器不会覆盖既有文件。两次生成必须得到相同数据库字节、大小、SHA-256 和 canonical generated manifest。异常夹具是预期的失败输入，不能通过删除、置空、重排或修改记录来制造通过。

恢复演练只使用由 `CreateBackup` 产生的不可变备份 manifest，在新的暂存文件校验 SQLite 版本和调用方验证器后原子替换显式副本；不执行 down migration，也不修改备份本身。

生成过程在返回前关闭所有 SQL.js 数据库句柄；后续 Go 演练才会串行取得副本所有权，禁止两个运行时同时写同一文件。测试仅清理自身 `TempDir`，失败信息只引用稳定夹具 ID 与产物文件名，使临时目录中的 manifest 保持为故障证据索引。
