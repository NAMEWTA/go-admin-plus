# Windows 安装

下载 x64 NSIS 安装包并校验 SHA-256：

```powershell
Get-FileHash -Algorithm SHA256 .\go-admin-plus-0.0.3-windows-x64-setup.exe
```

运行安装器，选择安装目录。当前制品未签名，按 Windows 正常提示确认。首次启动选择本地 SQLite 或远程 HTTPS 服务；本地首次使用需创建管理员。

用户数据独立于安装目录：`%LOCALAPPDATA%\com.goadmin.plus\data` 保存 SQLite、文件、备份和凭证保险库，`%LOCALAPPDATA%\com.goadmin.plus\logs` 保存日志。升级前先退出应用并备份数据。卸载只移除程序，保留运行数据。

开发及发行验证脚本需要 POSIX shell，可使用 Git Bash 或 MSYS2；必要时设置 `GO_ADMIN_POSIX_SHELL` 为 `sh.exe` 的绝对路径。
