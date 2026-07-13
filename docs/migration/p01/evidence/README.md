# P01 验证证据

`npm.cmd run migration:p01:verify` 每次运行会创建不可覆盖的目录：

```text
docs/migration/p01/evidence/runs/<UTC>-pid-<pid>/
```

目录只接收验证过程产生的真实证据：

- 每条命令、起止时间、真实退出码、signal 与判定结果；
- 脱敏后的 stdout/stderr、字节数和 SHA-256；
- P01 关键源码运行前后哈希及稳定性；
- 开始/结束 git status、受影响文件和剩余风险；
- `summary.json` 与证据清单。

P00 门禁失败时，summary 的状态为 `blocked`，且命令列表只有 P00；不得伪造后续步骤或通过结果。证据日志会替换仓库、用户目录和系统临时目录，并遮蔽凭据形态内容；不记录环境变量值、API key、token、auth 数据或未授权绝对路径。

验证不启动 Electron，不访问真实 `userData` 或生产数据库。renderer TypeScript 源码测试只在独立系统临时目录生成短期转译文件并立即清理；P00 自身的安全 smoke 仍只使用独立系统临时目录、临时数据库、stub Agent 子进程和剔除凭据变量的环境。

`runs/` 下的目录视为一次性、不可变记录。需要重跑时必须产生新的 run id；不得修改旧证据、补写不存在的退出码或用手工文件替代实际命令结果。
