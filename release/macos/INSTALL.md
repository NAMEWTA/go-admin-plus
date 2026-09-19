# macOS 安装

下载匹配处理器的 arm64 或 x64 DMG，在制品目录校验：

```bash
shasum -a 256 -c SHA256SUMS
```

打开 DMG，将 `Go Admin Plus.app` 拖入安装目录。当前制品未签名，首次运行按系统正常提示确认。

首次启动选择本地或远程模式。本地模式创建自己的 SQLite 和管理员；远程模式连接现有 HTTPS 后端。连接设置可随时打开调整，切换模式不会同步数据。

本地数据保存在 `~/Library/Application Support/com.goadmin.plus/data`，日志位于 `~/Library/Logs/com.goadmin.plus`。程序包内不写运行数据。升级前停止应用并备份数据目录。
