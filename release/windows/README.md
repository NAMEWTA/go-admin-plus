# Windows x64 桌面发行

`identity.json` 定义 Windows x64 Tauri 2 NSIS 制品。安装器按用户安装，提供目录选择并包含 WebView2 离线安装包；当前发行未签名。

程序目录只保存安装文件。本地数据库、文件、备份与凭证保险库位于 `%LOCALAPPDATA%\com.goadmin.plus\data`，日志位于 `%LOCALAPPDATA%\com.goadmin.plus\logs`。卸载保留用户数据。

首次启动可选择本地 SQLite 或远程 HTTPS 服务。实际安装、登录、角色 CRUD、重启和卸载保留数据由 Windows runner 验收，不能由其他平台的编译结果替代。
