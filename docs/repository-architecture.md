# 仓库架构

当前产品版本为 `0.0.3`。Go Server、Web App 和 Tauri 2 Desktop 共用同一产品组合，正式业务模块为
IAM、Audit、Scheduler、Files。

## 后端

`backend/` 采用组合根、应用层、模块和平台能力分离的结构：

```text
cmd/
  backend/       Server 进程入口
  desktop-sidecar/     Tauri 管理的本地进程入口
  config-check/        配置预检入口
  migrate/             向前迁移入口
internal/
  app/product/         唯一产品组合根
  application/         跨模块用例与应用协议
  contracts/           生成的传输合同
  host/                Server 宿主
  modules/             iam、audit、scheduler、files
  platform/            config、database、migrations、coordination、outbox 等技术能力
```

模块拥有自己的领域、存储、传输、权限声明和迁移。模块之间通过端口或产品组合根协作，不直接导入其他模块的私有实现。Server 支持 SQLite 与 PostgreSQL；Desktop profile 只映射 SQLite。

后端调用层级固定为 `CLI/host -> product composition root -> application lifecycle -> module service/transport -> repository/platform`。组合根负责构造模块、路由、迁移和 worker；应用层负责生命周期；模块 service 在事务内授权并通过 repository 访问存储；平台层只提供数据库、日志、outbox 和协调等技术能力。

Go module 固定为 `github.com/NAMEWTA/go-admin-plus/backend`，所有内部 import 使用该仓库路径，不保留重构前的短 module path。

## 前端

`frontend/` 是 pnpm workspace：

```text
apps/
  admin-web/           浏览器 App
  admin-desktop/       Tauri 2 App 与 Rust host
packages/
  app-shell/           产品装配与导航壳
  platform/            平台无关类型和端口
  api-client/          OpenAPI 生成客户端
  ui/                  共享 UI 与交互状态机
  adapters/            browser、desktop 运行时适配
  domains/             领域逻辑包
  web-domains/         领域 Vue 页面包
```

根 workspace 固定为 `@go-admin-plus/workspace`，内部私有包统一使用 `@go-admin-plus/*` scope；不保留历史别名或双 scope。

依赖方向为 App -> runtime adapter -> app-shell/router -> web-domain controller/client -> headless domain port -> generated API client；领域逻辑不依赖具体宿主。Web 与 Desktop 复用领域功能，但分别选择浏览器 HTTP 和 Tauri IPC 适配器。

## 根治理与交付

GitHub Actions、Hook、忽略规则、Taskfile、部署、数据库和发行资产只归仓库根管理。`scripts/quality/architecture-check.mjs` 校验目标目录与层级，`scripts/quality/compatibility-zero.mjs` 阻止已移除体系重新进入活动命令、CI 和文档。发布前必须通过 Go、pnpm、合同生成、架构、兼容性、文档和 release policy 门禁；Desktop 使用 Tauri 2 host 加 Go sidecar，Server PostgreSQL 迁移只能由离线 migrate 命令执行。
