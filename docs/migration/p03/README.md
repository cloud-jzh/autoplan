# P03 只读 Projects / Snapshot 迁移阶段

P03 只建立可对照的读取路径：Go 从本次 fixture 或显式声明的脱敏数据库副本读取 `projects`、`settings`、`project_states`，application service 生成兼容 `Project` 与 `AppSnapshot`，HTTP 仅暴露探针、Projects 和 Snapshot 读取。Node/sql.js 仍是产品中的唯一 writer；本阶段没有生产 Go writer、双写、真实事件流，也没有 UI 或状态管理重写。

## 单一 transport 开关

renderer 只有 `VITE_AUTOPLAN_TRANSPORT` 一个 transport 选择入口。缺失、空值、`ipc`、非法值，以及生产构建中的任何值都解析为 IPC；只有非生产环境显式配置 `http` 才创建 `HttpAutoplanClient`。HTTP 运行参数从一次性内存配置读取，不写入 URL、argv、日志或 evidence。

HTTP 模式只接管 `health`、`ready`、项目分页、单项目和项目 snapshot。尚未迁移的写操作、聊天、终端及已有业务事件继续委托同一个 `IpcAutoplanClient`，因此组件树不需要按 transport 分叉。项目事件连接只访问 `/api/v1/skeleton/sse`：它验证 P02 envelope、取消和连接生命周期，但不发布业务事件，不冒充真实 live stream。关闭 client 时会释放占位连接；默认或回退 IPC 不创建 HTTP/SSE 连接。

## HTTP 与数据库授权边界

Go HTTP 仅监听 `127.0.0.1`，每个 Projects/Snapshot 请求均经过精确 Host、Origin 和进程内随机会话鉴权；会话不得来自环境变量、命令行、URL 或 evidence。API 只有 `GET`/`HEAD`，分页、错误 envelope、request id、超时和取消均由共享边界处理。

repository 不发现 Electron `userData`，也不接受产品数据库默认路径。允许的来源只有：

1. 当前受控系统临时根中的 `.sqlite` fixture；
2. 授权根内、显式声明已脱敏且使用 `.copy`、`.backup` 或 `.bak` 后缀的普通数据库副本。

路径必须是绝对路径、位于授权根内部、没有符号链接或 WAL/SHM/journal/lock sidecar，且不能名为 `autoplan.sqlite`。reader 一次性以只读文件句柄载入字节，在内存中解析固定三表，不链接 SQL engine，不执行 SQL、migration 或建库；打开后通过大小、mtime、文件身份和 SHA-256 检测漂移。Node 读取并关闭 sql.js 后，Go 才能串行打开同一临时 fixture，前后字节哈希必须相等。

## 阶段验证

统一入口为：

```text
npm.cmd run migration:p03:verify
```

入口按以下顺序工作：

1. 实际运行 P00、P01、P02 门禁，并重新校验各阶段最新完成 evidence、manifest 哈希与冻结源码哈希；
2. 执行 repository、路由、OpenAPI、授权表和 loopback 静态只读守卫；
3. 在系统临时目录派生 P00 脱敏数据库，串行读取 Node 规范和 Go 规范，比较已提交黄金 JSON，并证明数据库前后 SHA-256 相等；
4. 执行 Go repository/application/HTTP 契约、React IPC/HTTP 双传输、运行时只读及凭据形态扫描；
5. 执行完整 Go gate，再按 P00 冻结签名评价 `npm.cmd run check` 与 `npm.cmd test`。

P03 专项 Node、Go 和 renderer 命令必须以零退出码结束。`check/test` 只允许 P00 记录的精确既有失败签名；新增签名、签名消失但未被基线规则接受、P03 相关失败或基线漂移都会失败。P15 仍须清零或逐项处置所有冻结红灯，本阶段不会把旧红灯改写成成功。

验证参数只接受精确的 `verify` 模式；没有覆盖 evidence、绕过门禁、筛选部分测试或保留临时数据库的入口。P00/P01/P02 命令或完成证据任一不满足时，结果为 `blocked`，后续 P03 命令不会启动，也不会生成虚假黄金。静态只读守卫失败时，同样不会继续触碰数据库。

## 证据与清理

每次运行创建 `docs/migration/p03/evidence/runs/<UTC-run-id>/`，记录实际命令、起止时间、原始退出码、脱敏 stdout/stderr、日志哈希、受控源/fixture/黄金哈希、数据库前后哈希、Node/Go diff 摘要、接口覆盖、git 受影响文件和剩余风险。证据不保存环境变量值、可复用会话、凭据、数据库字节或内容、真实用户路径。目录创建后不覆盖；manifest 固化该次 summary 和日志。

成功、失败或 blocked 都会删除验证器拥有的 `autoplan-p03-verify-*` 系统临时根；黄金比较器也只删除自己创建的 `autoplan-p03-compare-*` 根。清理边界不匹配时拒绝删除并令验证失败。若进程被外部强制终止，可只在确认目录位于操作系统临时目录且名称匹配上述前缀后清理；不得清理仓库、Electron `userData` 或任意产品数据库目录。
