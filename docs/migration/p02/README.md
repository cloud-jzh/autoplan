# P02 Go sidecar 基座

P02 交付可独立启动的 loopback Go sidecar、安全默认配置、共享 application service 边界、受保护的 REST/SSE/WebSocket skeleton，以及先行冻结的 OpenAPI/JSON Schema。当前端点不提供生产业务读写、Operation 执行/持久化、事件重放或终端数据传输。

## 独立启动

启动前必须已有通过且未漂移的 P00/P01 evidence。运行目标只能是系统临时目录或显式数据库副本，不能指向 Electron `userData` 或生产数据库。PowerShell 示例：

```powershell
$runtime = Join-Path ([IO.Path]::GetTempPath()) ("autoplan-p02-" + [guid]::NewGuid().ToString("N"))
$env:AUTOPLAN_SIDECAR_LISTEN_HOST = "127.0.0.1"
$env:AUTOPLAN_SIDECAR_LISTEN_PORT = "0"
$env:AUTOPLAN_SIDECAR_ALLOWED_ORIGINS = "http://127.0.0.1:43124"
$env:AUTOPLAN_SIDECAR_RUNTIME_DIR = $runtime
$env:AUTOPLAN_SIDECAR_RUNTIME_TARGET_KIND = "temporary"
Push-Location backend
go run ./cmd/autoplan-server
Pop-Location
```

进程成功监听后，stdout 只输出一行 JSON：

```json
{"version":1,"type":"autoplan_server_ready","pid":1234,"host":"127.0.0.1","port":49152,"ready":true}
```

`port` 是操作系统分配的随机端口；调用方只能解析该消息，不得猜测端口。消息不包含路径、配置、环境值或会话凭据。运行日志只写 stderr 的固定字段 JSON。

## 探针与关闭

- `GET /healthz` 只证明进程存活，不检查 repository/application readiness。
- `GET /readyz` 仅在配置、P00/P01 前置、迁移状态、单实例锁、application service 和 listener 全部通过时返回 ready。
- 收到支持的终止信号或父 context 取消后，readiness 先永久切换为 `shutting_down`，再按反向顺序有界关闭 HTTP server、依赖与实例锁。重复关闭幂等。
- 临时目标由 sidecar 创建时，正常关闭会清除锁及空运行目录。异常退出后的残留目录可在确认进程不存在后人工删除。

## 会话与默认拒绝

REST、SSE 和 WebSocket 共用同一个进程内随机会话、Host 校验和精确 Origin allowlist。会话只能通过受控进程间内存交接进入 `X-Autoplan-Session`，或由 host-only、HttpOnly、SameSite=Strict Cookie 承载；不能进入 argv、URL、环境变量、readiness、日志、fixture、localStorage 或持久配置。

缺失/重复/错误会话，缺失/非精确 Origin，转发头、URL 凭据、备用认证头和错误 Host 均在 application service 前被拒绝。调试配置不能关闭该策略，也不能把监听地址放宽为 `0.0.0.0`。

当前 `/api/v1/skeleton/rest`、`/api/v1/skeleton/sse`、`/api/v1/skeleton/websocket` 只冻结受保护握手和依赖注入形状，授权成功后仍返回稳定 `not_implemented`。独立命令不会把内存会话打印到 stdout，因此它不构成可供外部脚本调用生产 API 的凭据分发机制。

## 契约与共享 fixture

- OpenAPI：`backend/openapi/openapi.yaml`
- JSON Schema：`backend/openapi/schemas/`
- Go DTO/严格解码：`backend/internal/domain/contracts/`
- Node/Go 共用合成样本：`fixtures/contracts/p02/manifest.json`
- 契约语义：`docs/migration/p02/contracts.md`

共享 manifest 覆盖最小/完整 Project，空/最小/完整 Snapshot，稳定 Error 目录，OperationAccepted、六种 Operation 状态、SSE/WS v1，以及未知字段、缺字段、重复键、非 UTC 时间、非法终态/幂等键/版本/方向、敏感字段和绝对路径负例。负例由声明式 mutation 派生，fixture 本身不保存可用凭据或真实机器路径。

## 最终验证入口

唯一 P02 验证入口：

```powershell
npm.cmd run migration:p02:verify
```

验证器按顺序执行 P00 与 P01 硬门禁；任一失败就记录 `blocked` 并停止 P02 命令。前置通过后执行 Node 共享 fixture 漂移守卫、Go 契约/HTTP 安全/生命周期专项测试、`go test ./...`，再按 P00 冻结预期运行 `npm.cmd run check` 与 `npm.cmd test`。P00 已登记的精确已知红灯只能按原签名保留；新增失败、签名变化、退出码吞没或 skip/only 都失败。

验证只继承剔除凭据形状变量后的环境，并把临时工作根设置到系统临时目录。证据记录实际命令、真实退出码、时间、脱敏 stdout/stderr、源文件哈希、git 状态、受影响文件和剩余风险；不会读取 Electron `userData`，也不会与 Node/sql.js 同写数据库。

## 回退与剩余风险

回退 P02 时停止 sidecar，清理其明确拥有的系统临时目录，并移除 P02 backend/契约/验证入口变更；不要删除或改写 P00/P01 evidence、生产数据库或用户改动。当前仍缺少生产 repository/migration、业务 handler、Operation 执行与持久化、SSE 重放/live 衔接、WebSocket upgrade 和 Electron 受控会话交接，这些能力必须由后续阶段在相同 application service 与安全边界上实现。
