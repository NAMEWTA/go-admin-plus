# macOS 桌面发行

`identity.json` 定义产品标识、最低系统版本与两种目标架构：Apple Silicon arm64 和 Intel x64。对应 runner 分别构建 app/DMG。当前发行用于个人使用，不签名、不公证。

应用安装目录只保存程序；SQLite、文件、备份及凭证保险库位于 `~/Library/Application Support/com.goadmin.plus/data`，日志位于 `~/Library/Logs/com.goadmin.plus`。替换应用前停止程序并备份数据。

首次启动可选择本地 SQLite 或远程 HTTPS，两种数据独立。详细操作见 [INSTALL.md](INSTALL.md)。
