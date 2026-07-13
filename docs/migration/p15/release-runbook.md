# P15 发布准备手册

默认策略为 `publish never`。本手册只定义人工发布前的检查顺序，不会创建 Release、上传安装包、切换真实 userData 或写入用户数据库。

## 停止条件

任一项为真即停止并记录 `blocked`：P005 薄壳门禁未通过、P006 删除未授权、缺少任一平台、缺少签名/notarization 凭据、sidecar manifest 校验失败、包内路径/权限不一致、E2E/恢复证据缺失或日志含秘密/绝对路径。

## 三平台候选产物

1. 在对应原生 runner 的隔离工作目录运行既有 `package:win`、`package:mac` 或 `package:linux`。构建脚本只接受 `backend` Go module 和受支持目标。
2. 记录 sidecar 的 Go 版本、GOOS/GOARCH、源码提交、源码树哈希、二进制 SHA-256 与资源 manifest；拒绝空、错平台、陈旧或来源不明文件。
3. 验证包内资源路径为 `sidecar/<platform>/<arch>/autoplan-server[.exe]`，并验证 Windows 安装/portable、macOS app bundle、Linux unpacked 路径。
4. Windows 仅在签名时验证 Authenticode；macOS 必须验证 hardened runtime、sidecar codesign、notarization、staple 和 Gatekeeper；Linux 必须验证 Unix 可执行权限。
5. 对每个平台在临时数据目录执行 fresh-install、launch/readiness、正常退出、崩溃退出、更新重启与包冒烟。不得引用真实 userData。

## 发布门禁

只有在三个平台的包验证均为 `passed`、`evidence/manifest.json` 为完成、恢复演练已通过，且人工批准已在外部变更流程中记录后，人工才能使用受控发布工具。仓库脚本和工作流不自动调用发布 API。

缺少 macOS 签名或 notarization 凭据时，候选仅可标记为 `unsigned-test`/`blocked` 并保留脱敏本地测试证据；不得标记为已公证或可发布。
