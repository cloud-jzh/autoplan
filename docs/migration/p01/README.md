# P01 Renderer 客户端边界迁移

P01 将 renderer 的业务调用集中到 `AutoplanClient`，将 Electron 原生能力集中到 `DesktopBridge`。本阶段不改变 preload、主进程 handler、数据库、Node 业务实现、DTO、错误传播或页面交互。

## 边界与传输

- `AutoplanClient` 承载 P00 矩阵中归属 go-api 的全部业务方法，以及 snapshot/patch、Terminal、Chat 和配置事件。
- `DesktopBridge` 只承载目录/文件选择、打开本地路径或外链、文件 URL、更新管理等 Electron 原生能力。
- renderer 唯一传输配置为 `VITE_AUTOPLAN_TRANSPORT`。未设置、值为 `ipc` 或值无效时均回退到 IPC；P01 不启用 HTTP、SSE 或 WebSocket transport。
- IPC 适配器继续转发既有 `window.autoplan` 方法，不改参数、返回值、Promise rejection、snake_case DTO 或事件顺序。

回退时只需删除 `VITE_AUTOPLAN_TRANSPORT`，或显式设为 `ipc`，再重新启动 renderer 开发/构建进程；Provider 会重新创建 IPC 客户端与桌面桥。若配置为当前阶段不支持的值，解析器同样返回 IPC，不需要第二个开关或修改调用方。

静态守卫只批准以下两个文件直接访问 `window.autoplan`：

- `src/renderer/lib/api/ipcClient.ts`
- `src/renderer/lib/desktop/ipcBridge.ts`

组件、Hook、页面和其他新文件中的点访问、可选链、括号访问、全局别名、解构或动态属性绕过都会使守卫非零退出。测试文件可以构造 mock，但生产源码不能扩大白名单。

## 能力矩阵

P00 inventory 仍以 `docs/migration/p00/capability-matrix.json` 为所有权来源，并双向比较：

- `AutoplanApi`、preload 暴露项与 invoke channel；
- `ipcMain` handler 与主进程事件生产者；
- `AUTOPLAN_CLIENT_OPERATION_KEYS`、`AUTOPLAN_CLIENT_EVENT_KEYS`、`DESKTOP_BRIDGE_OPERATION_KEYS` 和桌面事件；
- 尚存的 renderer 直连成员是否已在矩阵中分类。

`reorderPlans` 和 `runTaskBatches` 是既有 IPC 能力，矩阵中虽没有旧 `api` 字段，仍按 preload 名计入客户端契约。`fileAccess` 在客户端键集合中保持命名空间，在 inventory 中展开为 `fileAccess.get` 与 `fileAccess.save`。新增、删除、改名、重复归属、缺 handler 或缺 preload 映射均会失败。

## 验证入口

最终入口：

```text
npm.cmd run migration:p01:verify
```

执行顺序如下：

1. 执行真实 `migration:p00:verify`。P00 非零时记录 `blocked` 并停止，不跨阶段运行 P01 命令。
2. 运行 P00 IPC inventory 和 renderer 边界守卫。
3. 运行 IpcAutoplanClient、DesktopBridge、订阅生命周期、renderer/契约专项测试；现有 `.test.ts/.test.tsx` 会转译到独立系统临时目录后由 Node 执行，并在命令结束时清理。
4. 执行 `npm.cmd run check` 与 `npm.cmd test`，按 P00 的退出码及失败签名精确比较；已知 check 文件长度红灯只能原样继承。
5. 执行 `npm.cmd run build`，必须成功。

任何新增或变化的失败、无法取得退出码、未分类失败、`skip`/`only`、边界违规或专项失败都会使 P01 失败。子命令 stdout、stderr 和真实退出码不被吞掉。

## 影响范围与风险

受影响代码限于 renderer 客户端/桌面桥契约、两个 IPC 适配器、迁移后的调用方和订阅、对应测试、P00 inventory 扩展、P01 守卫/验证脚本、package scripts 与本说明。没有引入第二套 transport 配置，也没有读取或修改真实 Electron `userData`。

边界守卫不会为未完成迁移放宽规则：只要 P004-P006 范围仍有组件或 Hook 直连，验证就会明确失败并列出文件及行号。后续阶段的主要风险仍是重复挂载/销毁、迟到响应、项目切换和最后消费者释放时的监听器生命周期；这些由专项测试与最终 P01 验收共同覆盖。
