---
name: new-business-module
description: Scaffold a current Go Admin Plus single-table CRUD vertical module across dual-dialect migrations, modular OpenAPI, Go service and transport, IAM capability registration, pnpm domain and Web Domain packages, and product composition. Use when adding a new business table or CRUD module to this monorepo; do not use for cross-module workflows, external integrations, or aggregate-heavy business logic.
---

# 新增业务模块

为当前 Greenfield 架构新增一个单表 CRUD 垂直切片。先读根 `AGENTS.md`、当前 Speculo 状态、
[`backend-development`](../backend-development/SKILL.md) 和以下权威实现：

- 完整参考切片：`backend/internal/modules/scheduler/`
- 前端参考：`frontend/packages/domains/scheduler/` 与 `packages/web-domains/scheduler/`
- 产品组合：`backend/internal/app/product/registry.go`、`runtime.go`
- 双 App 组合：`frontend/packages/app-shell/src/product/`

模块文件需要在受控路径中手工创建并逐项接入产品组合，不会自动完成产品注册。当前正式模块为
`iam`、`audit`、`scheduler`、`files`；不要恢复已经从产品组合中移除的旧业务体系。

## 适用边界

仅用于一个聚合根、一个主表、标准查询/新增/修改/删除和稳定权限码的模块。涉及跨模块事务、可靠事件、文件系统、副作用调度或外部服务时，先建立独立 SpecDev change，并按消费者 Port 或 Integration Event 设计。

坚持当前产品边界：Server 使用 PostgreSQL 或 SQLite，Desktop 只使用 SQLite；数据库 Migration 是 schema 唯一来源；OpenAPI 3.1 是 HTTP transport 唯一来源；权限由 IAM capability registry 管理；模块不能查询其他模块私有表。

## 工作流

### 1. 冻结命名和合同

确定模块名、实体名、表名、路由前缀、三个点分隔权限码（`module.resource.read|write|delete`）、菜单 key/path 和数据范围。ID、revision、分页、排序、validation/not-found/conflict 语义必须在编码前明确。

### 2. 建立双方言 Migration

在 `backend/internal/modules/<module>/migrations/` 创建模块 Provider，并为 PostgreSQL、SQLite 分别提供同版本向前 Migration。使用当前 migration runner，不在服务启动或 repository 中创建/修改表。

实现模块前先在隔离的本地 profile 执行 Migration，并从受控路径审查新增文件；拒绝覆盖其他模块或共享文件。

### 3. 审查后端切片

模块至少应拥有 model/mapping、repository、service、IAM adapters、HTTP handler/operations、permissions、Migration 和测试。

- repository 只依赖 `internal/platform/database`，SQL 参数保持双方言可绑定。
- service 在同一事务内重新授权；写操作使用 revision 防止丢失更新。
- HTTP handler 使用生成的 strict transport、统一 Session/CSRF adapter 和稳定 Problem 分类。
- `permissions.go` 通过 `authorization.ModuleCapabilities` 注册权限和菜单。
- 数据范围由 IAM authorizer 返回并在 repository 查询中实施；不得由前端代替后端过滤。
- 不手改 `transport/openapi.gen.go`、`openapi.json`、manifest 或前端 generated client。

### 4. 接入合同和产品组合

完成以下显式接缝：

- 在 `contracts/openapi/product.yaml` 引用模块 fragment 的公共 paths。
- 运行根 `task generate`，提交规范生成物并保证 `task generate:check` 无漂移。
- 在 `internal/app/product/registry.go` 注册 Module ID、Migration Provider 和 capabilities。
- 在 `internal/app/product/runtime.go` 构造 service、request adapter、HTTP handler 和 route module。
- 为 runtime/registry 增加缺失依赖、失败启动和模块清单回归测试。

### 5. 接入 pnpm workspace

生成或完善：

- `packages/domains/<module>`：生成合同类型、领域校验、permission constants、client port；不得依赖 Vue 或 DOM。
- `packages/web-domains/<module>`：Web client mapping、controller、Vue 页面和单测。
- package manifests 与 workspace lock importer。
- `packages/app-shell/src/product/manifest.ts` 的菜单/路由和 `ProductWorkspace.vue` 的 controller/page 组合。

页面工作流使用本仓库的共享 list/controller 和管理弹窗合同。需要单独细化页面时使用 `new-list-page` skill。

### 6. 验证

至少运行：

```bash
task contract:lint
task generate:check
task test
task lint
task architecture:check
task compatibility:zero
pnpm --dir frontend check:workspace
pnpm --dir frontend build
```

另外运行模块 Go 测试、双方言 Migration/CRUD 测试以及两个前端 package 的 typecheck/test。E2E 是否执行由当前 Goal Plan 和用户授权决定，不能用单元测试替代其最终状态。

## 完成条件

只有当空库 Migration、双方言 repository、直接 API 权限拒绝、UI 权限隐藏、Web/Desktop 共享组合、生成无漂移和根门禁均有证据时，模块才算编码完成。菜单可见但 API 未注册、API 可用但未进入产品 manifest、或生成文件存在但仍位于受控输出目录，都不算完成。
