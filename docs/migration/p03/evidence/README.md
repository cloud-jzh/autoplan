# P03 evidence 说明

`npm.cmd run migration:p03:verify` 在 `runs/<UTC-run-id>/` 下创建一次性、不可覆盖的阶段证据。仓库不预置成功 run；只有统一验证入口的真实执行结果可以生成 run。

每个 run 包含：

- `summary.json`：阶段状态、blocked 原因、命令清单、实际起止时间和原始退出码；
- `NN-<command>.stdout.log` / `stderr.log`：脱敏后的完整进程输出及其字节数、SHA-256；
- `evidence-manifest.json`：生成 manifest 前已存在的 summary 与日志文件哈希，并声明 run 目录不可变。

`summary.json` 还记录 P00/P01/P02 完成证据摘要、受控源码起止哈希、fixture/黄金哈希、数据库读取前后 SHA-256、Node/Go 结构化比较结果、接口覆盖、测试控制扫描、git 起止状态、受影响文件、临时目录清理和剩余风险。数据库哈希只用于证明同一字节流未改变；数据库字节和行内容不会进入证据。

## 状态判定

- `blocked`：P00、P01、P02 任一真实门禁失败，或最新完成 evidence/manifest/冻结源码校验失败；P03 子命令不会启动。
- `completed` 且 `ok: true`：全部 P03 专项命令零退出，黄金与 Node/Go 相等，fixture 哈希不变，静态和运行时守卫通过，临时根已清理，并且 P00 基线规则接受项目 `check/test` 结果。
- `completed` 且 `ok: false`：已运行的验证存在真实失败；原始退出码和失败签名保留，不折叠为成功。
- `failed`：运行子命令前发现不安全黄金内容或只读安全前提不成立。

命令行不提供覆盖、绕过门禁、只跑部分场景或修改黄金的模式。P03 专项失败不可引用 P00 既有失败名单；只有最后两个项目级基线命令使用 P00 的精确签名比较。任何测试控制指令都会进入剩余风险并使 `ok` 为 false，P15 的清零或明确处置义务持续有效。

## 脱敏约束

证据只保留仓库相对路径和系统临时根占位符。repo、HOME、系统临时目录及常见本机绝对路径会被替换；Bearer、key、认证、会话和 cookie 形态先触发失败，再在落盘日志中替换。环境只继承剔除敏感名称后的变量，summary 仅记录移除数量，不记录名称或值。

run 中禁止放入数据库、数据库副本、请求/响应正文、环境变量值、可复用会话、Electron `userData`、真实 workspace 路径、provider 凭据或私钥。黄金 JSON 只以哈希引用；其字段和值安全性由生成器和 evidence 前置扫描验证。失败 diff 只保留 JSON Pointer 和差异类别，不保留敏感值。

## 审阅与保留

审阅时先核对 manifest 中每个 artifact 的 SHA-256，再看 `summary.ok`、`status`、`blocked`、`sourceHashesStable`、`databaseIntegrity.unchanged`、`nodeGoDiff.equal`、各命令 `evaluation` 和 `temporaryCleanup`。manifest 不递归包含自身；目录生成后不得手工编辑或补写“成功”结果。损坏或不完整的 run 保留为失败证据，重新验证应生成新的 run id。

验证器只清理其拥有且位于系统临时目录的受控前缀目录，不清理 evidence。需要移除 evidence 时应按审计保留策略处理整个 run，不能只删除失败日志或重写 summary。
