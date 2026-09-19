# 数据库

Go Admin Plus 的 Server 支持 SQLite 与 PostgreSQL，Desktop 只支持 SQLite。

迁移 SQL 由各业务模块拥有，`internal/app/product` 将 provider 组合成一个全局唯一、只向前的版本序列。统一的 `cmd/server migrate` 子命令与运行时 schema 检查复用同一迁移 registry。Server 的 `serve` 与 `worker` 遇到任一数据库 schema 不匹配时只会不 ready 并退出，不执行迁移；只有离线 `migrate` 子命令拥有 Server 迁移权。

- SQLite 使用单进程文件锁、单连接和 WAL；适合 Desktop 与单实例 Server。
- PostgreSQL 使用连接池和迁移会话锁；适合多实例 Server。
- 迁移必须同时提供两种方言版本，并保持相同业务语义。
- 不提供运行时回滚命令；失败后修复输入或提交新的向前迁移。

```bash
task migrate PROFILE=server-sqlite
GO_ADMIN_DATABASE_DSN_FILE=/absolute/path/to/dsn task migrate PROFILE=server-postgres
```

## 系统管理员初始化与恢复

数据库迁移只建立 schema、受保护的 `system-admin` 角色和能力，不隐式创建可登录账号，也不包含静态账号或密码。空库初始化由受信任宿主调用 IAM 的一次性 Bootstrap 用例：Server 使用产品 CLI，Desktop 使用原生首次设置流程。密码只允许从交互式终端或权限受限的 secret file 读取，不得进入 argv、环境变量、日志或审计 payload。

Bootstrap 只在账号表为空且初始化 marker 尚未提交时成功。账号、系统管理员授权、marker 和脱敏审计事实位于同一事务；并发调用最多一个提交。失败后应修复输入并重试，不得手工插入默认账号。

Server 空库迁移完成后，用统一 CLI 和权限受限的密码文件初始化。SQLite 还需传入与运行时
一致的数据库路径；PostgreSQL 通过 `GO_ADMIN_DATABASE_DSN_FILE` 取得 DSN：

```bash
cd backend
go run ./cmd/server bootstrap --profile server-sqlite \
  --sqlite-path ../.data/server/go-admin-plus.sqlite3 --data-root ../.data/server \
  --username first.admin --display-name "First Administrator" \
  --email first.admin@example.test --secret-file "$SECRET_FILE"

GO_ADMIN_DATABASE_DSN_FILE="$DSN_FILE" go run ./cmd/server bootstrap \
  --profile server-postgres --data-root ../.data/server \
  --username first.admin --display-name "First Administrator" \
  --email first.admin@example.test --secret-file "$SECRET_FILE"
```

已有系统全部管理员不可用时，停止 API 与 worker，再通过 `recover-admin` 离线流程选择一个既有、未删除账号。恢复会重置密码、重新启用账号、授予系统管理员角色并撤销旧 Session。恢复不能创建账号或复活已进入删除生命周期的账号；没有可恢复账号时只能恢复数据库备份。

升级前必须同时备份数据库和产品文件根。SQLite 复制前停止唯一宿主并保存数据库及同目录的
WAL/SHM 状态；PostgreSQL 使用与部署策略一致的逻辑或物理备份。迁移失败不运行降级 SQL：
保留失败日志，恢复完整备份，修复输入或提交新的向前迁移后重试。
