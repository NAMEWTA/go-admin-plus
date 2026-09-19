# 基座重构实施记录

本次按已确认方案实施：统一 `backend/`、`frontend/`；保留 IAM、Files、Audit、Scheduler，删除 Demo。全部修改与验收由同一执行者完成，未指派 subagent。

## 已完成的调整

| 范围 | 结果 |
|---|---|
| 目录与依赖 | 统一后端、前端、脚本、Go module 引用及开发规范；移除 Demo、Gin 和未使用的本地缓存实现 |
| 配置 | 严格 YAML、默认值/文件/环境/CLI 优先级、密钥文件与脱敏；SQLite/PostgreSQL、Redis 开关、local/S3 可配置 |
| 运行模式 | SQLite 单进程；PostgreSQL 支持组合进程或独立 API/worker；服务端显式迁移，桌面本地迁移前备份 |
| IAM | 修复账户路由、可信代理与登录来源、最后管理员保护、数据范围；写事务执行最终授权和 revision 冲突检测 |
| 菜单 | 目录/页面/按钮、层级树、注册路由、内置页面展示编辑；禁止循环与超过 16 层的子树移动 |
| 文件 | 按记录保存 provider，本地/S3/MinIO、大小与内容验证、暂存认领及崩溃恢复；桌面主要文件路径流式传输 |
| 审计 | 稳定动作/资源/操作者/来源/trace；成功事实同事务，失败记录有界；受控保留期清理 |
| 调度 | 五段 Cron、时区、注册 Go handler、手动排队、历史、租约与最多三次崩溃恢复；后台循环独立监督 |
| 前端 | 菜单树与路由一致，切换身份清理页面状态并中止旧请求；表单 revision、加载互斥、对话框焦点约束 |
| 桌面 | 本地/远程连接设置、HTTPS 与自定义 CA、服务版本探测、按 origin 隔离凭据；断网保留设置入口 |
| 部署与发行 | YAML/Compose/systemd；Linux 服务 amd64/arm64；Windows x64、macOS arm64/x64、Linux x64 桌面构建脚本和 CI |
| 工具链 | Go 升至 1.26.8，容器基础镜像同步固定版本及 digest；前端和 Rust 依赖使用锁文件 |

Go 工具链升级源于本次 `govulncheck` 检出的标准库调用路径；修复版本依据 [Go 官方发布记录](https://go.dev/doc/devel/release#go1.26.8)。

## 验收范围与结果

以下记录对应实际执行结果，不把测试跳过算作通过。当前工作区的日志与截图保存在 `artifacts/refactor-validation/`，安全报告和 SBOM 位于 `artifacts/security/`；这些生成目录不纳入源码。

| 检查 | 已完成结果 |
|---|---|
| Go | `go test ./...`、`go test -tags sqlite ./...`、`go test -race ./...`、`go vet ./...` 在 Go 1.26.8 下均通过 |
| PostgreSQL | `scripts/ci/required-postgres.mjs` 的 14 个必需套件通过，0 跳过 |
| 真实依赖组合 | SQLite/PostgreSQL × Redis 开/关 × local/S3 共 8 个组合通过，使用隔离 PostgreSQL、Redis、MinIO |
| 组合内行为 | 登录、导航缓存、账户、文件上传/读取/删除、权限拒绝、角色版本冲突、审计读取 |
| 浏览器 | 登录会话、IAM 管理、Files、Audit、Scheduler、Web 壳层均通过 SQLite 和 PostgreSQL 实际浏览器验收；壳层包含移动视口 |
| 前端 | 187 项 Vitest、49 项 Node 测试通过；类型检查、lint、Web/Desktop 生产构建通过 |
| Rust | 26 项测试通过；生产和 native-e2e 配置的 Clippy 检查通过；覆盖真实 HTTP 流式传输、截断下载、If-Match、退出后的令牌隔离、服务兼容性、系统凭据读取超时、远程退出不关闭服务 |
| Linux 构建与运行 | 服务端 amd64/arm64 静态交叉编译通过；桌面生产构建、制品隔离检查、本地首次设置与远程不可用窗口验证通过 |
| Linux 安装包 | `.deb` 与 AppImage 均已生成；AppImage 在解包运行模式下通过本地与远程失败场景；解包确认主程序、sidecar、桌面入口与图标，包内主程序和 sidecar 不含测试控制入口 |
| 契约与规范 | 生成一致性、OpenAPI、架构、兼容性清理、文档、governance、Task 契约及发行策略检查通过 |
| 工作区密钥 | 扫描包含新增/未提交文件的 6.51 MB 工作树，0 泄漏；不依赖 Git 历史扫描覆盖未提交代码 |
| Go 漏洞扫描 | 当前调用路径受影响 0 项；仍提示依赖模块中 4 项不可达问题，未作为“整个依赖树无漏洞”宣称 |
| Rust 漏洞扫描 | 未报告阻断漏洞；保留 9 条上游提示（8 条未维护、glib 的 1 条 unsound 提示），未添加忽略规则 |
| 依赖扫描 | `scripts/security/required-security.mjs` 全部 7 步通过，包含 Go/pnpm/Rust 扫描、依赖来源约束、Git 密钥扫描、CycloneDX SBOM 和生成一致性 |

复验入口：

```bash
cd backend
go test ./...
go test -tags sqlite ./...
go test -race ./...
go vet ./...
cd ../frontend
corepack pnpm test
corepack pnpm typecheck
corepack pnpm lint
corepack pnpm build
cd ..
task governance:check
task architecture:check
task compatibility:zero
task docs:check
task task:contract
task contract:lint
task generate:check
task release:verify
```

真实依赖验收需要一次性 PostgreSQL DSN、Redis 地址和 S3 测试桶权限；变量和隔离约束见相应测试与 CI，不在文档中保存临时密码。八种组合入口为后端 `internal/app/product` 的 `TestFoundationStorageCacheMatrix`。

## 交付边界

- 本次建立新数据库基线，不提供旧数据库、旧 API、旧安装目录的兼容迁移。已有 `.data` 和用户文件未删除；采用新数据目录部署。
- 本地与远程是独立数据集，不进行双向同步。桌面本地任务只在应用运行时执行。
- 系统凭据读取最多等待三秒；超时使用内存会话，迟到结果不会写入会话保险库。
- 原生文件下载使用临时文件完成后再替换；Windows 上目标已存在且不能原子替换时保留原文件并返回失败。
- 当前宿主是 Linux。Windows/macOS 安装与实际运行仍需对应平台 CI 验收；当前发行按未签名制品处理，未执行签名、公证、外部发布或生产部署。
- Linux 原生运行使用独立 XDG 目录与 Xvfb。已验证本地首次设置、远程连接失败及设置入口；`.deb` 只解包检查，未安装进当前操作系统。
- 本次创建的 PostgreSQL、Redis、MinIO 三个临时容器已移除。
- 根 `AGENTS.md`、Speculo 的既有修改保持原状，不纳入 0.0.3 发行提交。
