# AutoPlan

Electron 桌面应用：基于 issue/需求驱动的自主规划与开发循环。收集需求与反馈，自动生成开发计划，调用 AI CLI 后端逐个执行任务，并通过验收命令闭环。

## CLI 后端

AutoPlan 的计划生成、任务执行、修复与验收通过外部 AI CLI 完成。支持**项目级**选择后端，不同项目可使用不同 CLI：

| 后端 | 默认命令 | 说明 |
| --- | --- | --- |
| **Codex CLI**（默认） | `codex` | 通过 `codex exec` 非交互执行，支持会话（session）复用，失败任务重试时延续上下文 |
| **Claude CLI** | `claude` | 通过 `claude -p` 非交互 print 模式执行，prompt 经 stdin 传入，不使用 Codex 会话机制 |

- **默认仍是 Codex**：未配置过的历史项目升级后继续使用 Codex，无需重新配置。
- **切换方式**：进入项目 →「任务与计划」→「循环控制」表单，选择「CLI 后端」并保存；也可在创建/编辑项目时选择。命令路径留空时使用上表默认命令名。
- **Claude 前置条件**：需在本机已安装 `claude` CLI 并完成认证（`claude` 已可正常登录调用）。若命令不在 PATH，可在「CLI 命令路径」填写完整路径。
- **会话隔离**：Codex 的会话复用仅在 Codex 后端启用；切换到 Claude 不会把历史 `codex_session_id` 传给 Claude，两类上下文物理隔离。
- **可观测性**：运行中日志、事件流、任务卡片和概览会显示当前使用的 CLI 后端，CLI 缺失、认证失败、命令失败等问题有可读提示和日志可追踪。

## 开发

```bash
npm install
npm run dev        # 启动开发模式（Vite + Electron）
npm run check      # TypeScript + 主进程 JS 静态检查
npm run smoke      # 核心流程冒烟测试（含多后端 stub 覆盖）
npm run package:win  # 打包 Windows 安装包
```

> 真实的 Claude CLI 调用需要本机安装并认证 `claude`，`npm run smoke` 通过 stub 覆盖后端路由与会话隔离核心分支，不依赖真实二进制。
