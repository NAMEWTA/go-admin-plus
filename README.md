# Go Admin Plus 0.0.3

Go Admin Plus 0.0.3 是一个单仓库管理系统产品，包含 Go backend、Vue frontend 和 Tauri 2 Desktop App。当前基座保留用户、角色、目录/页面/按钮权限、文件、审计和受控任务调度。

## 仓库结构

| 路径 | 职责 |
|---|---|
| `backend/` | Go Server、Desktop sidecar、业务模块和数据库迁移 |
| `frontend/` | pnpm workspace、Web App、Tauri 2 Desktop App 和共享领域包 |
| `scripts/` | 根任务调用的开发、质量、合同和发行脚本 |
| `deploy/` | Linux 容器部署定义 |
| `release/` | 三平台打包、签名、验证和制品策略 |
| `database/` | 数据库支持和迁移约束 |
| `docs/` | 当前架构、开发和发行文档 |
| `.agents/skills/` | 项目级后端、前端和垂直切片开发规范 |

## 配置与运行方式

- 后端：`backend/`；前端：`frontend/`。
- 统一 YAML 选择 SQLite/PostgreSQL、Redis 开关、本地/S3（兼容 MinIO）文件存储。
- Web 静态资源和 API 可分别部署；SQLite 单进程，PostgreSQL 支持 API/worker 分开运行。
- 桌面支持 Windows x64、macOS arm64/x64、Linux x64，提供本地 SQLite 与远程 HTTPS 连接设置。
- 服务启动前显式迁移；桌面本地迁移先备份。当前是新的数据库基线，不承诺读取旧安装数据库。

配置样例见 [统一配置](backend/config/README.md)，实现边界与验收见 [重构说明](docs/refactor-implementation.md)。

## 开发启动

安装 Go 1.26.8 或更高版本、Go Task 3.48.0、Node.js 22 或更高版本和 pnpm 11.1.3；CI 当前使用 Node.js 22.22.3。pnpm 也可由当前 Node 安装提供的 Corepack 启动，Workspace 的 `packageManager` 固定实际版本。Desktop 还需要 Rust 1.88 或更高版本和当前平台的 Tauri 2 系统依赖，CI 当前使用 Rust 1.96.0。完整安装与 PATH 说明见[开发指南](docs/development.md)。

新 Server SQLite 必须先迁移，再用权限受限的密码文件离线创建首个系统管理员。下面的
`$SECRET_FILE` 只保存文件路径；密码内容不得放入 argv、环境变量、日志或仓库：

```bash
task migrate PROFILE=server-sqlite

cd backend
go run ./cmd/server bootstrap --profile server-sqlite \
  --sqlite-path ../.data/server/go-admin-plus.sqlite3 --data-root ../.data/server \
  --username first.admin --display-name "First Administrator" \
  --email first.admin@example.test --secret-file "$SECRET_FILE"
cd ..

task doctor PROFILE=server-sqlite
task dev TARGET=server PROFILE=server-sqlite
```

另开终端运行 `task dev TARGET=web`，在 Web 登录后完成 IAM、审计、调度和文件管理。Server PostgreSQL 同样先运行
`GO_ADMIN_DATABASE_DSN_FILE="$DSN_FILE" task migrate PROFILE=server-postgres`，再用统一 CLI 的
`bootstrap --profile server-postgres --secret-file "$SECRET_FILE"` 初始化；API 与 worker
不会隐式迁移 PostgreSQL。完整命令见[开发指南](docs/development.md)。

运行 `task dev TARGET=desktop` 启动桌面开发。Desktop 首次启动选择本地或远程模式。本地使用 `desktop-sqlite` sidecar，在原生首次设置页创建管理员；远程模式直接连接 HTTPS 服务，可配置自定义 CA。本地与远程数据独立，不自动同步。系统凭据可用时恢复本地 Session，不可用时只保留当前进程会话。

## 根命令

`Taskfile.yml` 是产品命令的唯一入口。常用门禁为：

```bash
task test
task lint
task contract:lint
task generate:check
task governance:check
task architecture:check
task compatibility:zero
task docs:check

# 构建 Server、Web 和当前受支持宿主的 sidecar/Tauri 可执行文件
task build TARGET=all PROFILE=server-sqlite

# 生成当前宿主的本地制品
task package TARGET=server PROFILE=server-sqlite
task package TARGET=web
task package TARGET=desktop
```

详细说明见 [开发指南](docs/development.md)、[仓库架构](docs/repository-architecture.md) 和 [发行指南](docs/release.md)。
