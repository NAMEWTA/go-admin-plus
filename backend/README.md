# Go 后端 0.0.3

本模块提供两个正式命令：

| 命令 | 用途 |
|---|---|
| `cmd/desktop-sidecar` | Tauri 2 管理的本地 SQLite 服务 |
| `cmd/server` | SQLite 或 PostgreSQL Server；通过 `serve`、`worker`、`migrate`、`bootstrap`、`recover-admin` 和 `doctor` 子命令提供运行、迁移和管理操作 |

从仓库根使用 Taskfile：

```bash
task dev TARGET=server PROFILE=server-sqlite
task migrate PROFILE=server-sqlite
task test
task lint
```

业务模块位于 `internal/modules/`，当前正式模块为 IAM、Audit、Scheduler、Files；产品唯一组合根位于
`internal/app/product/`。后端实施规范见 [backend-development skill](../.agents/skills/backend-development/SKILL.md)。
配置格式见 [配置说明](config/README.md)，数据库策略见 [数据库文档](../database/README.md)。
