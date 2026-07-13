# P14 Terminal 验证门禁

状态：`default-off / evidence-required`。

P14 将 Terminal 的 REST 控制面、私有 WebSocket 数据面、PTY 生命周期和进程树清理作为一个验证域。`go_terminal_api` 仅在三平台真实打包冒烟证据完整且所有专项合约通过后才可由受控部署开启；它不改变 Chat、MCP、数据库 owner 或既有会话的 runtime 归属。

最终验证入口为：

```text
npm.cmd run migration:p14:verify
```

验证器不会读取真实 Electron userData，也不会接受未标记的目录。它只使用 `fixtures/migration/p14/terminal`，建立隔离的临时 HOME、APPDATA、缓存和 Go cache，并移除继承的凭据、数据库位置及 `AUTOPLAN_*` 环境变量。

验证按以下顺序 fail-closed：

1. 校验 P00、P11、P13 的已接受 evidence、终端 fixture、runtime owner 边界和测试未过滤策略。
2. 执行 renderer transport/hook 合约、Go PTY/Application/REST/WebSocket 安全测试，以及仓库的类型、语法和 Node 回归命令。
3. 只在当前系统运行真实打包应用；Windows、macOS 和 Linux 都必须分别产生真实 artifact、argv、退出码、脱敏输出、哈希和进程树清理结果。
4. 汇总三平台证据。任何缺失、跨平台代跑、源码测试替代打包冒烟、未脱敏输出或 source hash 漂移都会以非零退出并标记 `blocked` 或 `failed`。

终端原始输入、输出、环境、凭据、PID、真实路径和 userData 不得进入 fixture、日志、evidence summary 或 manifest。每次验证在 `docs/migration/p14/evidence/runs/<run-id>/` 生成不可变目录；未运行的验证绝不能伪造为通过。
