# P04 显式副本迁移、切换与恢复手册

本手册只适用于调用方明确授权的脱敏数据库副本。`<authorized-root>`、`<database-copy>`、`<backup-dir>` 和 `<manifest>` 都是占位符，不代表真实用户目录。不得把 `autoplan.sqlite`、Electron `userData` 或自动发现的路径代入本流程。

## 1. 前置条件

1. P02、P03 的统一验证与最新完成 evidence 均通过，冻结输入哈希未漂移。
2. 数据库必须是授权根内的普通文件，使用 `.copy`、`.backup` 或 `.bak` 后缀；授权根和备份目录必须预先存在且不是符号链接/重解析点。
3. 停止 Electron/Node writer、Go server 和其它迁移器，确认没有 `-wal`、`-shm`、`-journal` 或 owner/migrate 活跃 sidecar。
4. 备份目录可用空间至少满足 preflight 报告的预算；建议另留一次完整副本及临时验证副本的余量。
5. 全程仅处理副本。任何条件不满足时记录 blocked 并停止，不尝试修复、删除孤儿或跨阶段开放写入。

## 2. Preflight

使用显式绝对路径运行：

    autoplan-migrate preflight \
      --database <authorized-root>/<database-copy>.sqlite.copy \
      --allow-root <authorized-root> \
      --backup-dir <backup-dir> \
      --sanitized-copy

只接受 `status=ok`、`code=preflight_ok`。核对稳定 database ID、源 SHA-256、from version、目标 migration checksum 和待执行版本。输出不得包含数据库内容或真实路径。空间不足、活跃 sidecar、Owner 锁冲突、未知版本、损坏文件、前序 evidence 或路径授权失败均应 blocked。

## 3. Dry-run

    autoplan-migrate dry-run \
      --database <authorized-root>/<database-copy>.sqlite.copy \
      --allow-root <authorized-root> \
      --backup-dir <backup-dir> \
      --sanitized-copy

dry-run 只在隔离临时副本中执行迁移和迁后验证。核对 `from_version`、`to_version`、`applied_versions`、migration checksum、迁前/迁后表计数与 `write_performed=false`。结束后重新计算源文件及既有 sidecar 的哈希和元数据；源必须完全不变。

## 4. 正式迁移

再次确认 Node writer 已关闭，然后执行：

    autoplan-migrate migrate \
      --database <authorized-root>/<database-copy>.sqlite.copy \
      --allow-root <authorized-root> \
      --backup-dir <backup-dir> \
      --sanitized-copy

迁移器必须先取得与 server 相同的数据库 Owner 锁，再以 `O_EXCL` 创建 UTC 时间戳备份及 manifest。记录 manifest ID/SHA-256、数据库备份大小/SHA-256、源版本和 migration checksum。备份目标已存在、校验失败或源在 preflight 后变化时必须失败，不能覆盖。

成功只以事务 migration、`schema_migrations`、`user_version=1`、连接 PRAGMA、schema 校验、完整性/外键/关系/路径/聚合审计全部通过为准。故障或取消不能留下可被视为完成的半迁移 ledger。

## 5. Verify、no-op 与启动门禁

    autoplan-migrate verify \
      --database <authorized-root>/<database-copy>.sqlite.copy \
      --allow-root <authorized-root> \
      --backup-dir <backup-dir> \
      --sanitized-copy

随后再次执行 `migrate`。已验证的 schema v1 必须返回 `migration_noop`、`no_op=true`，不创建新备份、不重放默认值。启动 Go server 时，只有 Owner 锁、migration checksum、schema/audit 和 repository 装配完成后 `/readyz` 才能成功；关闭开始或任一步失败时始终 non-ready。

在副本上启动旧 Node/sql.js 兼容检查。旧 Node 必须在 CREATE/ALTER/backfill/persist 前以稳定兼容错误拒绝 schema v1，且副本哈希不变。禁止将旧 Node 作为迁移后热回退 writer。

## 6. 恢复

恢复前停止所有 writer 并取得同一 Owner 锁。只使用正式迁移产生且重新校验通过的显式 manifest：

    autoplan-migrate restore \
      --database <authorized-root>/<database-copy>.sqlite.copy \
      --allow-root <authorized-root> \
      --backup-dir <backup-dir> \
      --manifest <manifest> \
      --sanitized-copy

恢复器先把不可变备份复制到新的暂存文件，校验 artifact SHA-256、SQLite header/user version 和完整性，再通过原子替换恢复目标并复核最终哈希。不得执行 down migration，不得编辑或覆盖备份。失败恢复必须保留当前目标和备份；暂存/previous 冲突、校验和损坏、权限错误或取消都应非零失败。

恢复完成后再次执行 preflight/verify 与只读审计，核对：

- 恢复 SHA-256 等于 manifest 中迁移前源 SHA-256；
- schema version、逐表行数、主键范围、关系/路径/聚合审计等于迁移前证据；
- 备份 artifact 与 manifest 哈希未改变；
- 没有遗留 WAL/SHM/journal 或 restore 暂存文件。

## 7. 失败处理与禁止项

保留该次 evidence run 和 manifest，按稳定阶段/错误码诊断；不要通过删除记录、置空外键、重排 ID、修改 checksum 或清理测试产物制造成功。磁盘不足、短写、权限拒绝、中断、panic、Owner 冲突、审计异常或恢复失败均应先保持副本/备份不变，再在新的显式副本上重新演练。

本阶段禁止自动触碰真实 `userData`、自动扫描产品数据库、Node/Go 双写、通过 UI/HTTP/MCP 暴露迁移、使用 down migration 代替恢复，以及在未满足 readiness 时切换业务流量。
