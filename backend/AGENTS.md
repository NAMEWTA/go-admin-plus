# 后端开发约定

- 进程入口位于 `cmd/server`、`cmd/desktop-sidecar`，唯一产品装配根为 `internal/app/product`。
- 业务行为、HTTP、权限和迁移归属 `internal/modules/<module>`；跨模块通过端口注入，不直接访问其他模块私有表。
- 技术设施放在 `internal/platform`。注释用中文解释约束、事务边界、失败恢复与幂等语义。
- 每个数据库变更提供 SQLite、PostgreSQL 两套迁移。当前交付为新的初始基线，今后的已发布迁移只向前追加。
- 服务端通过统一 YAML 选择 SQLite/PostgreSQL、Redis 和存储 provider；Redis 不能成为会话、授权、锁或队列的事实来源。
- Server 显式执行 migrate 后启动；桌面本地 SQLite 先备份再迁移。桌面远程模式通过原生 HTTPS 代理访问服务端，不启动本地 sidecar。
- 最终授权、数据范围、版本冲突必须在写事务中验证。业务失败返回稳定错误，日志和审计不记录密钥或请求体。
- 交付前运行 `go test ./...`、`go test -tags sqlite ./...`、`go vet ./...`、`go mod tidy`。涉及真实依赖时运行隔离的 PostgreSQL/Redis/S3 验收。
