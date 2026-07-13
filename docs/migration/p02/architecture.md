# P02 Go sidecar 架构边界

## 形态与约束

P02 建立的是 Electron 薄壳管理的本机 Go sidecar，Go 进程内部保持模块化单体，不拆成微服务，也不增加远程可访问的管理面。独立 module 为 `github.com/lyming99/autoplan/backend`；`autoplan-server` 与 `autoplan-migrate` 只负责把进程参数、上下文和标准输出交给 `internal/bootstrap`，命令入口不得装配 repository、复制业务规则或直接产生持久化副作用。

当前包依赖方向固定为：

```text
cmd -> bootstrap -> application -> domain
                    |    |
                    |    +-> repository ports -> repository adapters
                    +------> runtime/platform outbound ports -> adapters

REST / SSE / WebSocket / future MCP / Electron compatibility adapter
                    -> the same application.Boundary
```

`domain` 不依赖其他层；`application` 只编排领域规则和 outbound ports；`repository` 定义持久化端口；`runtime` 承载生命周期、后台任务及时钟等运行边界；`platform` 承载证据门禁、实例、日志、会话等基础设施；`bootstrap` 是唯一依赖装配点。transport 不得直接访问 repository、数据库、文件系统、进程或 PTY，repository/runtime/platform 也不得反向依赖 HTTP adapter。

`internal/config` 与 `internal/httpapi` 是后续 P02 任务的目标包；P001 只保留目录边界，不提前实现配置、路由、鉴权、health/ready 或业务 endpoint。`migrations` 是显式 migration 注册边界，`openapi` 是版本化契约边界。

## 共享 application service 与 transport 分工

REST/JSON 负责查询、CRUD、命令提交和 Terminal 控制面；SSE 负责 AppSnapshot/patch、Operation、Chat 和配置类单向事件；WebSocket 只负责 Terminal input/data/resize/status/exit/closed 双向流。未来 MCP、UI transport 与上述 adapter 必须调用同一个 `application.Boundary`，项目授权、状态转换、批量预校验、幂等规则和副作用顺序只实现一次。SSE/WebSocket 的连接、背压和编码逻辑属于 adapter，不成为第二套业务服务。

迁移期间保持 P00 冻结语义：JSON 使用既有 snake_case 兼容策略；Project、AppSnapshot、Error、Operation、nullability、默认值、排序和 UTC 时间不由 transport 擅自改变；完整 snapshot、字段存在性 patch、Loop/Plan/Task/Chat/Terminal 状态机、错误结果、事件顺序及既有副作用必须保持兼容。具体 DTO 与事件信封由后续契约任务冻结，P001 不提前发明生产契约。

## 启动前置门禁

两个命令在创建运行目录、监听器或 persistence dependency 之前，只从仓库内读取 P00/P01 最新不可覆盖 evidence run：

- P00 必须 `ok=true`，来源哈希、expectations 哈希和证据完整性稳定；manifest 必须匹配 summary；当前 capability matrix 与 contract baseline 必须和证据哈希一致。
- P01 必须 `status=completed` 且 `ok=true`；P00 gate、IPC inventory 与 renderer boundary 命令必须被证据接受；当前 inventory、renderer guard 和统一客户端/桌面桥关键源码必须和证据哈希一致。
- 缺少 evidence、最新运行失败、manifest 非法或源码漂移时，只输出不含路径、环境值和底层错误的稳定 `blocked` JSON code，然后非零退出。门禁不会自行运行验证命令，也不会读取 Electron `userData`。

P001 尚未实现 HTTP runtime。即使前置证据通过，`autoplan-server` 仍以 `server_runtime_not_implemented` 安全拒绝，不监听端口、不创建数据。后续任务在相同门禁之后装配安全配置、loopback 随机监听、readiness 和优雅关闭。

## Migration 安全边界

`autoplan-migrate` 只接受显式 `--target`、`--target-kind`，以及互斥的 `--status` 或 `--dry-run`。目标类别仅允许仓库内脱敏 fixture、系统临时目录或以 `.copy`/`.backup`/`.bak` 明确标识的数据库副本；普通 `autoplan.sqlite` 被拒绝。P001 仅做词法安全校验，不 `stat`、打开、创建或写入目标，空 catalog 表示没有生产 migration。参数、目标路径和底层错误不会被回显。

任何实际 apply、schema 读取、业务表写入、自动寻找数据库、推断 Electron 应用目录或连接活动 sql.js 数据库均不属于本任务。

## 单 writer 与本阶段非目标

当前生产数据库唯一 writer 仍是 Node/sql.js。Go 不得与 Node 同写同库，不得自动打开真实 `userData` 或现有 `autoplan.sqlite`；首笔 Go 正式写入前后均受 P00 ADR-0002 的不可逆所有权门禁约束。

P001 不实现生产业务查询或 mutation、数据库 driver/schema、HTTP endpoint、MCP、SSE 事件流、WebSocket/PTY、会话/Origin、配置解析、日志、实例锁、feature flag 切换、renderer transport 切换或 UI/状态管理重构。目录与接口只建立后续任务可扩展的依赖方向，不代表 sidecar 已可用于生产读写。
