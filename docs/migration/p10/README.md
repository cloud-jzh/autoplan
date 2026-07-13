# P10 Operation、Outbox 与可恢复 SSE 验证

P10 只在调用方明确提供的脱敏 fixture 或副本上验证。验证器绝不自动发现、读取、复制或修改 Electron `userData`、生产 SQLite、真实 workspace、用户目录、环境凭据或现有进程的数据库文件。

## 唯一入口

```powershell
npm.cmd run migration:p10:verify -- --fixture-root <authorized-p10-fixture>
```

`<authorized-p10-fixture>` 必须是绝对路径，位于临时目录或名称明确包含 fixture/copy 的目录，并同时包含：

- `.autoplan-p10-authorized-copy`；
- `p10-fixture-manifest.json`，内容声明 `kind: "p10-authorized-fixture"`、`schema_version: 1` 与 `authorized_copy: true`；
- 不存在 owner lock、WAL/SHM/journal sidecar 或其他活动写入标记。

入口首先验证 P00 冻结红灯签名和 P09 不可变完成证据；任何门禁失败均以退出码 `2` 结束，并且不会执行 P10 Go、renderer、SSE 或数据库相关命令。验证期间的 HOME、APPDATA、LOCALAPPDATA、XDG、Go cache 和工作目录均重定向到验证器创建的系统临时目录。

## 覆盖范围

- Operation 六态、幂等、取消竞争、迟到完成、启动恢复与事务 outbox；
- outbox 分发、重放/live 交界、保留水位、Last-Event-ID、背压和 resync；
- 项目/Operation SSE 的鉴权、归属、cursor、关闭与无泄露响应；
- renderer 的 reconnect、event_id 去重、revision 缺口、合批、单飞 snapshot resync 与清理；
- P10 fixture 合同、敏感内容扫描、P00 `check`/`test` 冻结表面。

实际运行证据只写入 [evidence](evidence/README.md) 的新 run 目录。根目录 `manifest.json` 是格式定义，不能替代一份成功证据。
