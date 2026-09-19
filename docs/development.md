# 开发指南

当前产品版本为 `0.0.3`。后端与前端的目录边界、调用层级和编码规则分别以
[backend-development](../.agents/skills/backend-development/SKILL.md) 与
[frontend-development](../.agents/skills/frontend-development/SKILL.md) 为准；新增垂直切片时再读取对应的
`new-business-module` 或 `new-list-page` skill。

## 前置环境

| 工具 | 本地合同 | CI 验证基线 |
|---|---|---|
| Go | 1.26.8 或更高版本；最低版本由 `backend/go.mod` 管理 | 1.26.8 |
| Go Task | 3.48.0；根命令合同要求精确版本 | 3.48.0 |
| Node.js | 22 或更高版本；范围由 Workspace `engines` 管理 | Node.js 22.22.3 |
| pnpm | 11.1.3；由 Workspace `packageManager` 固定 | 11.1.3 |
| Rust/Cargo | Desktop 最低 Rust 1.88；由 `Cargo.toml` 的 `rust-version` 管理 | Rust 1.96.0 |

Desktop 还需要当前平台的 Tauri 2 系统依赖；PostgreSQL profile 需要可访问的 PostgreSQL 实例。先安装固定的根命令入口，并确保 Go 的 binary 安装目录在 `PATH` 中：

```bash
go install github.com/go-task/task/v3/cmd/task@v3.48.0

# GOBIN 为空时，Go 默认安装到 GOPATH/bin。
export PATH="$(go env GOPATH)/bin:$PATH"
task --version # 必须输出 3.48.0
```

首次安装前端依赖：

```bash
pnpm --dir frontend install --frozen-lockfile

# 仅安装了 Corepack 时仍显式选择仓库固定版本
corepack pnpm@11.1.3 --dir frontend install --frozen-lockfile
```

根 Task 优先使用 PATH 中精确匹配 11.1.3 的 pnpm。Node 工具管理器未把 pnpm shim 传给子进程时，命令面会在根 `.artifacts/tool-shims/` 中原子生成并使用固定 `pnpm@11.1.3` 的 Corepack shim；该目录不进入源码或发行制品。Desktop 开发建议使用 CI 基线 Rust 1.96.0；最低版本只表示 Cargo 允许编译，不替代 CI 基线验证。

## Server SQLite 与 Web

`server-sqlite` 数据库位于根目录 `.data/server/`。空库严格按 migrate、bootstrap、doctor、
serve 顺序启动。先用密码管理器或本地编辑器创建只允许当前用户读取的 `$SECRET_FILE`；
变量值是文件路径，文件内容不得进入 shell history、argv、环境变量或仓库。

```bash
task migrate PROFILE=server-sqlite

cd backend
go run ./cmd/server bootstrap --profile server-sqlite \
  --sqlite-path ../.data/server/go-admin-plus.sqlite3 --data-root ../.data/server \
  --username admin --display-name "System Administrator" \
  --email admin@example.test --secret-file "$SECRET_FILE"
cd ..

GO_ADMIN_LOG_LEVEL=info task doctor PROFILE=server-sqlite
task dev TARGET=server PROFILE=server-sqlite
```

另开终端执行 `task dev TARGET=web`。Web 默认连接 `127.0.0.2:8080`；使用刚创建的账号
登录后，可验证 IAM 账号与角色、审计、调度和文件管理。SQLite Server 用
`serve --with-worker` 单进程运行，且由实例锁阻止两个写入宿主同时打开同一数据库。

## Server PostgreSQL

`server-postgres` 的 DSN 只通过 `GO_ADMIN_DATABASE_DSN_FILE` 指向权限受限文件。生产拓扑
要求 migrate 独占运行并成功退出，之后才分别启动 API 与 worker；二者遇到过旧、过新或
未知 schema 都会不 ready 并退出，不会执行迁移。

```bash
GO_ADMIN_DATABASE_DSN_FILE="$DSN_FILE" task migrate PROFILE=server-postgres

cd backend
GO_ADMIN_DATABASE_DSN_FILE="$DSN_FILE" go run ./cmd/server bootstrap \
  --profile server-postgres --data-root ../.data/server \
  --username admin --display-name "System Administrator" \
  --email admin@example.test --secret-file "$SECRET_FILE"
cd ..

GO_ADMIN_DATABASE_DSN_FILE="$DSN_FILE" task doctor PROFILE=server-postgres
GO_ADMIN_DATABASE_DSN_FILE="$DSN_FILE" task dev TARGET=server PROFILE=server-postgres
GO_ADMIN_DATABASE_DSN_FILE="$DSN_FILE" task worker PROFILE=server-postgres
```

非敏感配置可通过 `GO_ADMIN_CONFIG_FILE` 指向统一 YAML 文件，示例见 `backend/config/config.example.yaml`。

## Desktop

Desktop 是 Tauri 2 App，运行 profile 为 `desktop-sqlite`。开发命令先为当前平台构建 Go
sidecar，再启动 Tauri。本地模式使用 App 数据目录中的 SQLite；远程模式不启动 sidecar，通过原生 HTTPS 代理连接服务端。
空状态由原生首次设置页收集管理员信息，密码通过 Tauri command 进入宿主，不进入 WebView
持久存储。宿主先备份已有库再自动执行 Desktop SQLite 向前迁移。

```bash
task dev TARGET=desktop
```

## 数据库迁移

迁移只向前执行，由模块分别拥有并由产品组合层统一排序：

```bash
task migrate PROFILE=server-sqlite
GO_ADMIN_DATABASE_DSN_FILE=/absolute/path/to/dsn task migrate PROFILE=server-postgres
```

已有系统全部管理员不可用时必须先停止 API 与 worker，再运行统一 CLI 的 `recover-admin`。
完整的 `--account-id`、`--reason` 与 `--secret-file` 流程见[运维与恢复](operations.md)。

## Doctor、日志与 readiness

`task doctor PROFILE=server-sqlite` 或带 DSN 文件的 PostgreSQL 形式输出机器可读 JSON。
`healthy` 与 `degraded` 可为零退出；无效配置、schema mismatch 或依赖失败非零。日志级别由
`GO_ADMIN_LOG_LEVEL=debug|info|warn|error` 或 YAML 配置 的 `log.level` 控制。日志包含服务、
版本、profile、trace/request、route/module、状态、延迟、数据库与错误分类，但不记录请求正文、
密码、Session 或 DSN。故障处理见[运维与恢复](operations.md)。

## 构建与本地打包

`build` 验证可编译目标。`TARGET=desktop` 会先构建当前宿主的 Go sidecar，再以
`custom-protocol` release 模式编译 Tauri 2 宿主，但不生成 bundle。Desktop 支持 macOS arm64/x64、Windows x64 和 Linux x64。`package` 生成当前宿主
可用的本地制品：

```bash
task build TARGET=all PROFILE=server-sqlite
task package TARGET=server PROFILE=server-sqlite
task package TARGET=web
task package TARGET=desktop
```

Server 和 Web 制品写入根 `.artifacts/packages/`。Desktop 使用 Tauri 2 的目标目录：macOS
生成 `.app`，Windows 生成 NSIS，Linux x64 生成 deb/AppImage。本地 Desktop package 只用于构建验证，
不构成已签名发行候选。

## 验证

```bash
task test
task lint
task contract:lint
task generate:check
task governance:check
task architecture:check
task compatibility:zero
task docs:check
```

前端单独验证可在 `frontend/` 运行 `pnpm lint`、`pnpm typecheck`、`pnpm test`、`pnpm check:workspace` 和 `pnpm build`。
