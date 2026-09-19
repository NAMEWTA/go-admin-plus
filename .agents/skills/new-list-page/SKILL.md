---
name: new-list-page
description: Scaffold or extend a current Go Admin Plus Vue 3 CRUD list page across a headless domain package, generated OpenAPI client adapter, shared list controller, admin search/table/management dialog UI, permissions, product composition, and tests. Use for a standard business list and form workflow in the frontend pnpm workspace.
---

# 新增列表页

为当前 Web 与 Desktop 共用的前端产品实现标准 CRUD 页面。先读根 `AGENTS.md`、当前 Speculo 状态、
[`frontend-development`](../frontend-development/SKILL.md) 和以下权威实现：

- Headless Domain：`frontend/packages/domains/scheduler/`
- Web Domain：`frontend/packages/web-domains/scheduler/`
- 共享列表状态机：`frontend/packages/ui/src/list.ts`
- 页面视觉合同：`frontend/packages/ui/src/theme.scss` 与 `packages/ui/src/components/`
- 产品组合：`frontend/packages/app-shell/src/product/`

使用真实实现作为细节权威。当前产品页面属于 IAM、Audit、Scheduler、Files；保持现有管理页面
的 UI/CSS、中文文案、搜索区、工具栏、表格和管理弹窗结构，只按业务需要增减字段与操作。

## 1. 确认合同与权限

先确认模块 OpenAPI fragment 已进入 `contracts/openapi/product.yaml`，并已通过根生成任务产生当前 TypeScript client。冻结列表查询、详情、新增、修改、删除的 operation、分页/排序、validation/not-found/conflict 语义以及点分隔权限码。

页面不是授权边界。按钮和路由按 capability 隐藏，服务端仍必须在事务内重新授权。不要在页面中构造私有 HTTP 路径、响应信封或权限别名。

## 2. 实现 Headless Domain

在 `packages/domains/<module>` 中维护：

- 从生成合同显式映射的领域类型；
- 查询与表单的同步 normalize/validate；
- permission constants；
- 与框架无关的 client port 和错误分类。

Domain 不得依赖 Vue、DOM、组件库或 App Shell。不要直接把 generated transport 类型传播到页面，也不要使用 `any` 绕过边界。

## 3. 实现 Web Domain

在 `packages/web-domains/<module>` 中维护生成 client adapter、controller、Vue 页面与测试。

Controller 应复用 `createListController`，并保持这些状态语义：

- 本地校验失败不覆盖最后一次成功查询和投影；
- 过期请求不能覆盖较新的成功结果；
- loading/submitting/delete busy 状态在成功、失败和取消后都能恢复；
- 修改冲突保留用户输入并进入可修复状态；
- 删除在确认后再次检查 capability，并在成功后刷新列表；
- 错误由统一 Problem 分类投影为稳定中文反馈，不重复弹出同一错误。

只在模块需要额外业务状态时封装共享 controller；不要复制第二套列表请求状态机。

## 4. 组装页面

沿用当前管理页面结构：

1. 搜索区提交调用 controller search，重置恢复默认查询。
2. 工具栏显式提供新增和真实存在的批量操作。
3. 表格列使用稳定宽度约束；文字列优先 `min-width`，操作列保持现有固定布局。
4. 新增、编辑和详情使用现有 management dialog；关闭时重置 model、错误和 busy 状态。
5. 删除使用明确确认流程；用户取消不显示错误。
6. visible enum、状态、日期和反馈统一显示中文，不把 transport 原始值直接暴露给用户。
7. 使用当前 capability 指令或组件控制路由、工具栏和行操作可见性。

使用 `<script setup lang="ts">` 和 Composition API。保持已有键盘焦点、label、aria 属性、稳定测试标识与响应式布局。不要为了新增页面引入另一套路由、状态管理、组件库或 CSS 主题。

## 5. 接入产品组合

完成以下接缝：

- 两个 package manifest 的 exports、scripts 和直接依赖；
- workspace lock importer；
- `packages/app-shell/src/product/manifest.ts` 中的菜单与路由声明；
- `ProductWorkspace.vue` 中 controller 与页面的显式组合；
- Web 与 Desktop 共用同一页面，由 runtime adapter 决定 transport，不复制页面。

如果后端模块、权限注册或产品路由尚未存在，先使用 `new-business-module` skill 完成整个垂直切片，不能以静态假数据冒充产品接入。

## 6. 验证

至少运行：

```bash
pnpm --dir frontend lint
pnpm --dir frontend typecheck
pnpm --dir frontend test
pnpm --dir frontend check:workspace
pnpm --dir frontend build
task architecture:check
task compatibility:zero
```

同时运行该 Domain/Web Domain 的定向 typecheck 和单测，覆盖查询验证、过期请求、权限隐藏、弹窗重置、提交失败、冲突修复、删除取消和删除成功。若当前 Goal Plan 暂停 E2E，仍需保证对应 browser/native driver 静态编译通过，但不要把静态编译记录成 E2E 证据。

## 完成条件

只有当页面保持当前视觉合同、真实 API 和权限均已接入、Web/Desktop 共享产品组合、所有状态路径有测试且根门禁通过时，才算编码完成。只有页面文件、只有菜单、只有生成 client 或只通过单元测试都不构成完整交付。
