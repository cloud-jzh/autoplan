# P14 Terminal 三平台打包冒烟 Runbook

本 Runbook 只接受实际打包应用的本机结果。不能在 Windows 声称 macOS/Linux 已通过，也不能以 `go test`、交叉编译或源码 Electron 替代打包产物。

## 前置条件

- 保持 `go_terminal_api` 默认关闭，直到本次验证开始。
- 仅使用仓库中的 `fixtures/migration/p14/terminal`；不得复制真实 userData、工作区、终端 transcript、环境或凭据。
- 每个平台使用对应的已签名或待签名打包应用，而不是安装器外的源码启动命令。
- 运行前确认 P00、P11 与 P13 的 accepted evidence 可被 P14 prerequisite 检查读取。

## 本机命令

先生成该平台 artifact，再将实际可执行文件路径显式提供给冒烟脚本：

```text
node scripts/migration-p14/smoke-packaged-terminal.js --platform win32 --artifact <release/win-unpacked/AutoPlan.exe> --fixture-root fixtures/migration/p14/terminal
node scripts/migration-p14/smoke-packaged-terminal.js --platform darwin --artifact <release/mac/AutoPlan.app/Contents/MacOS/AutoPlan> --fixture-root fixtures/migration/p14/terminal
node scripts/migration-p14/smoke-packaged-terminal.js --platform linux --artifact <release/linux-unpacked/autoplan> --fixture-root fixtures/migration/p14/terminal
```

仅在当前宿主平台执行对应行。脚本向打包应用传入受限的 `--autoplan-terminal-smoke` 与 fixture 参数，并只接受应用最终输出的结构化 smoke 结果。该结果必须覆盖 create、list、write、带 seq 的 output、resize、exit、kill、close、reconnect 和进程树清理；还必须覆盖 Origin、项目归属、回放边界和输出不持久化。

应用或脚本必须记录实际 artifact 哈希、命令 argv、退出码、脱敏 stdout/stderr、平台、签名能力和时间。没有 artifact、不能执行其他平台、没有结构化 smoke 结果、慢客户端/背压失败、残留进程树或泄露敏感内容均为 `blocked` 或 `failed`，并且不得启用 flag。

## 汇总与回滚

在三个原生平台均留下有效 evidence 后运行：

```text
npm.cmd run migration:p14:verify
```

验证器会拒绝 source hash 漂移、未经授权的 fixture、真实 userData、敏感日志和跨 runtime 接管。回滚只关闭新的 Go Terminal admission；它不将已创建的 Go 会话交给 Node IPC，也不影响 Chat/MCP 或 Go 数据库 owner。任何不完整证据都保持 default-off。
