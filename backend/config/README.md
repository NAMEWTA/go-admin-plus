# 统一配置

服务端使用 YAML，参考 [config.example.yaml](config.example.yaml)。通过 `--config` 或 `GO_ADMIN_CONFIG_FILE` 指定文件；优先级是默认值 < 配置文件 < 环境变量 < 显式 CLI 参数。未知 YAML 字段、重复文档、非法数据库/provider、冲突的密钥来源会在打开依赖前被拒绝。

`database.driver` 选择 `sqlite` 或 `postgres`；`redis.enabled` 控制可选展示缓存。
SQLite 使用 `runtime.role: all` 单进程运行；PostgreSQL 可选择 `all`，或独立启动 `api`、`worker`。

`runtime.dataDir` 相对于进程工作目录；SQLite 路径和本地文件目录相对于 dataDir。数据库 DSN、Redis 密码、S3 secret 支持直接配置或 `*File`，推荐生产用密钥文件。配置对象格式化输出自动脱敏。

从 `backend/` 目录使用配置文件启动：

```bash
go run ./cmd/server migrate --config config/config.example.yaml
go run ./cmd/server bootstrap --config config/config.example.yaml \
  --username first.admin --display-name "系统管理员" \
  --email first.admin@example.test --secret-file "$SECRET_FILE"
go run ./cmd/server serve --config config/config.example.yaml
```

`bootstrap` 仅用于新库首次创建管理员；`SECRET_FILE` 是保存初始密码的受限权限文件路径。迁移、初始化和运行使用同一份配置。远程桌面直接连接这里启动的服务，不执行服务端数据库迁移或管理员初始化。

常用环境覆盖：`GO_ADMIN_DATABASE_DRIVER`、`GO_ADMIN_DATABASE_DSN_FILE`、`GO_ADMIN_SQLITE_PATH`、`GO_ADMIN_HTTP_LISTEN`、`GO_ADMIN_LOG_LEVEL`、`GO_ADMIN_DATA_DIR`、`GO_ADMIN_RUNTIME_ROLE`、`GO_ADMIN_REDIS_ENABLED`、`GO_ADMIN_REDIS_ADDRESS`、`GO_ADMIN_STORAGE_PROVIDER`、`GO_ADMIN_S3_ACCESS_KEY`、`GO_ADMIN_S3_SECRET_KEY_FILE`。完整字段和限制以示例及 `internal/platform/config/runtime.go` 为准。

`schema/` 中旧 profile 描述仅对应内部宿主构造测试，不是服务端 YAML 配置格式。桌面连接设置由原生设置页维护：本地模式启动 SQLite sidecar；远程模式保存 HTTPS 服务地址和可选自定义 CA，不在本地启动数据库。
