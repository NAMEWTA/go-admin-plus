# Linux x64 桌面安装

校验 `SHA256SUMS`，使用系统包管理器安装 deb，或赋予 AppImage 执行权限后启动。deb 适用于具有 WebKitGTK 4.1 运行库的发行版；AppImage 需要发行版具备相应的桌面环境与 FUSE 支持。

首次启动选择本地 SQLite 或远程 HTTPS 服务。本地模式创建独立数据库与管理员，远程模式不会同步本地数据。

程序目录只保存应用文件。数据位于 `${XDG_DATA_HOME:-~/.local/share}/com.goadmin.plus/data`；配置位于 `${XDG_CONFIG_HOME:-~/.config}/com.goadmin.plus/connection.json`。升级前关闭应用并备份数据目录；卸载不删除用户数据。
