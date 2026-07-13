# P05 mutation 验证与切换运行手册

本手册用于 P05 的验证、证据审阅和后续受控切换。所有 `<...>` 均为占位符。不得将真实用户数据库、Electron `userData`、可复用凭据或未脱敏日志代入命令和 evidence。

## 1. 前置条件

1. 工作树包含冻结的 P04 schema inventory、migration checksum、P05 write contract、manifest、Node golden 与 stable error catalog。
2. P04 统一门禁可通过，并存在最新的 `completed`、`ok: true`、哈希链接有效且源码稳定的不可变 evidence run。
3. Node 生成器只使用自身创建的系统临时根；Go 测试只使用事务 fixture、临时副本或 fake filesystem。
4. 不运行 Electron，不传入产品数据库路径，不设置绕过 owner、version、idempotency、root policy 或 golden 比较的开关。

本阶段只固化服务与 transport 契约，不切换真实数据库、不重写业务 UI，也不迁移 attachments、scripts、executors、terminals 或其它后续阶段能力。

## 2. 运行统一门禁

在仓库根执行：

    npm.cmd run migration:p05:verify

验证器创建唯一 `docs/migration/p05/evidence/runs/<UTC-run-id>/` 和唯一系统临时根。首条子命令必须是：

    npm.cmd run migration:p04:verify

该命令失败时只允许生成 P05 blocked evidence；不得继续运行 P05 writer、手工补 golden 或把 blocked 改成 completed。

## 3. 串行验证编排

P04 通过后，验证器按以下固定顺序执行：

1. `node-write-contracts`：在生成器拥有的临时数据库中验证 Node mutation/write contract，并证明数据库和 owner lock 已关闭。
2. `go-repository`：验证事务、约束、版本 compare-and-swap、幂等记录及重复 key/payload 规则。
3. `go-application`：验证 Project/Config mutation、运行中删除拒绝、snapshot 与文件策略应用服务。
4. `go-http`：验证路由、状态码、稳定错误 envelope、version 与 `Idempotency-Key`。
5. `go-files`：验证授权根、规范路径、符号链接/重解析点与越界拒绝。
6. `renderer-transport`：验证 renderer 仅访问 `127.0.0.1`，传输 version/idempotency，并接收完整 snapshot。
7. `mutation-golden-compare`：读取已关闭的 Node artifact，之后运行 Go export，深比较全部 mutation snapshot 和 writer handoff。
8. `p05-safety-preflight`：复核 OpenAPI 路由/错误、冻结哈希、owner-copy、transport 边界和脱敏 fixture。
9. `p05-orchestration-tests`：验证门禁 blocked 路径、串行时间线、清理边界及 comparator 深比较。
10. 项目级 `check`、`test`：与 P00 expectations 精确比较。

每条记录必须包含命令、cwd 语义、起止时间、实际退出码/signal、脱敏 stdout/stderr 哈希与 evaluation。任意两个记录的时间区间不得重叠。Node writer 必须恰好一次，五个 Go 写链路验证必须都在其结束后出现。

## 4. 验收重点

- Project create/update/configure/delete 成功后返回完整且排序稳定的 snapshot；版本单调递增。
- 缺失 version、旧 version、重复 idempotency key 使用不同 payload，以及缺失/运行中 Project 返回冻结错误码和 HTTP status。
- 相同 key 与相同 payload 重放原响应，不重复 mutation；无 idempotency key 的合法请求仍遵循版本规则。
- Node 与 Go 的成功/失败场景、集合顺序、空值、错误、版本轨迹和 writer handoff 全部深相等；未知字段或缺字段都失败。
- OpenAPI、Go HTTP、renderer transport 和 file policy 对路由、header、错误 envelope 及 snapshot shape 一致。
- 源码与冻结 artifact 的起止哈希一致，临时根清理成功，证据中没有数据库内容、环境值或真实绝对路径。

## 5. 失败处理

- `blocked`：P04 命令或最新 P04 evidence 无效。保留 evidence，不启动 P05 写链路。
- `failed`：P04 已通过，但 P05 某项真实命令不满足。保留失败退出码与日志，并停止尚未开始的 writer 和跨阶段步骤。
- `completed` 且 `ok: false`：P05 已运行，但专项、baseline、哈希、时间线、脱敏或清理至少一项不满足。按首个稳定失败定位，在新的 run 中重试。
- 工具启动失败、权限拒绝或超时：保留实际 exit/signal 和脱敏 stderr，不以手工日志或退出码覆盖结果。
- golden mismatch：修复实现或经独立变更流程重新生成 fixture；本门禁不提供原地更新 golden 的能力。
- 临时清理失败：只处置 evidence 记录的本次 owned root。禁止扩展通配符、扫描用户目录或删除不匹配 P05 前缀的路径。

禁止删除失败记录、编辑 immutable run、关闭版本/幂等校验、用 skip/only 隐藏普通测试、并发运行 Node/Go writer，或用生产数据复现测试。两个 golden export 入口在受控环境变量完全缺失时允许 fail-closed skip；验证器按文件和精确语句冻结该例外，环境不完整时仍必须失败。
