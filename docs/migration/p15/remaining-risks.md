# P15 剩余风险

当前状态为 `blocked`，风险未被掩盖：

- 旧业务 IPC handler/注册尚在，renderer 薄壳迁移没有完整的通过证据。
- Node/sql.js、Loop、Chat、MCP、Scripts、Executors、Terminal 与相关依赖仍保留，P006 未获删除授权。
- 唯一 writer、Electron+Go E2E、安装升级、迁移中断恢复、三平台 package smoke 尚未产生真实脱敏证据。
- Windows 签名、macOS notarization/staple 与 Linux 包权限/启动验证未完成；缺凭据时只能为 `blocked`。
- 发布工作流文件的写入环境曾被拒绝，自动发布仍禁止。
- 当前 runtime/fixture/文档只定义失败关闭契约，不能代替真实运行结果。

风险降低顺序：先完成 P005 的业务 IPC 实际移除与回归，再采集唯一 writer/E2E/恢复/三平台 evidence，复核 P006 删除门禁，最后由人工批准发布。任何顺序倒置均保持 blocked。
