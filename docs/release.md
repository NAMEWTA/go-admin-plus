# 发行指南

发布目标如下，版本由精确的 `major.minor.patch` 标签确定。

| 目标 | 制品 |
| --- | --- |
| Linux 服务 | amd64/arm64 压缩包、systemd unit、配置与部署说明 |
| macOS 桌面 | arm64/x64 的 Tauri 2 app 和 DMG |
| Windows 桌面 | x64 Tauri 2 NSIS 安装包 |
| Linux 桌面 | x64 deb 和 AppImage |

[发行工作流](../.github/workflows/release.yml)构建各平台制品，完成相应检查后创建 GitHub Release。当前为个人使用的未签名发行，不配置签名证书或公证密钥。工作流配置存在并不代表该版本已通过所有平台安装验收；应以具体运行记录和制品证据为准。

## Linux 服务

选择对应架构的服务压缩包，按其中的 `SERVER-INSTALL.md` 操作。先编辑 YAML，选择 SQLite 或 PostgreSQL、Redis 开关和文件 provider。DSN 使用权限受限的文件注入。首次部署依次运行 `migrate`、`bootstrap`、`doctor`，再启动服务。

SQLite 使用单个包含 API 与 worker 的进程；PostgreSQL 可拆分 API 与 worker。更新数据库之前先停下全部实例，再执行一次显式迁移；正常重启不应触发竞争迁移。容器部署使用仓库 [deploy/compose](../deploy/compose/)，服务压缩包不含容器镜像。

## 桌面安装与数据

首次启动选择本地 SQLite 或远程 HTTPS 服务。本地模式启动随包的 Go sidecar；远程模式不启动本地后端，两种数据集不自动同步。

安装目录只保存程序。`com.goadmin.plus` 下的 `data/` 保存本地数据库、文件、备份和凭证保险库：

| 平台 | 数据根目录 | 日志目录 |
| --- | --- | --- |
| Windows | `%LOCALAPPDATA%\com.goadmin.plus\data` | `%LOCALAPPDATA%\com.goadmin.plus\logs` |
| macOS | `~/Library/Application Support/com.goadmin.plus/data` | `~/Library/Logs/com.goadmin.plus` |
| Linux | `${XDG_DATA_HOME:-~/.local/share}/com.goadmin.plus/data` | 系统应用数据目录中的 `com.goadmin.plus/logs` |

连接设置保存在系统应用配置目录的 `connection.json`。远程凭据按服务 origin 隔离；系统凭据存储不可用时只在当前进程保留会话。升级前应停止应用并备份数据目录；卸载程序不应删除用户数据。当前采用新的数据库基线，已有旧安装需要另行迁移数据。

Windows 安装器按用户安装并提供目录选择；macOS 选择匹配架构的 DMG；Linux 使用 deb 或 AppImage。未签名制品按操作系统正常提示确认。

## 本地检查

三配置隔离验收（three-profile clean-room）覆盖 `server-sqlite`、`server-postgres`、`desktop-sqlite`。
当前个人发行的签名与公证标记均为 `not-required`。

```bash
task release VERSION=0.0.3
task release:verify
```

这些命令执行本地预检和发行策略验证，不推送标签、不发布、不部署生产服务。
