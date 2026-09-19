# 运维与恢复

## Doctor 与日志

启动前运行 `task doctor PROFILE=server-sqlite`；PostgreSQL 使用
`GO_ADMIN_DATABASE_DSN_FILE="$DSN_FILE" task doctor PROFILE=server-postgres`。报告是机器可读 JSON；
`schema mismatch`、配置无效或依赖不可用会产生非零退出。`GO_ADMIN_LOG_LEVEL` 只接受
`debug`、`info`、`warn`、`error`。运行日志用于进程与请求诊断，Audit 用于业务事实；两者都
不得记录请求正文、密码、DSN、Session、CSRF 或 secret-file 内容。

## 管理员恢复

只有全部系统管理员不可用时才使用离线恢复。先停止 API 与 worker，确认目标是既有、未删除的
账号，再创建仅当前用户可读的密码文件：

```bash
cd backend
go run ./cmd/server recover-admin --profile server-sqlite \
  --sqlite-path ../.data/server/go-admin-plus.sqlite3 --data-root ../.data/server \
  --account-id "$ACCOUNT_ID" --reason lost-access --secret-file "$SECRET_FILE"
```

PostgreSQL 形式增加 `GO_ADMIN_DATABASE_DSN_FILE="$DSN_FILE"` 并改为
`--profile server-postgres`。允许的 reason 是 `lost-access`、`credential-compromise` 或
`disabled-administrator`。恢复会重置凭据、启用账号、恢复系统管理员角色并撤销旧 Session；
它不会创建账号或复活已删除账号。完成后先运行 Doctor，再启动服务并要求所有操作者重新登录。

## 容量与文件

Files 容量同时受逻辑配额和物理磁盘水位约束。低水位或 reconcile 未完成时停止新写入，保留
读取与诊断能力；不要直接修改容量计数。先释放明确可删除的临时空间或扩容，再运行产品的
reconcile/Doctor 路径确认数据库元数据与文件状态一致。不可逆 purge 只有在 worker claim 前可
取消，处理中的任务不得通过手工改库回退。

## Session 与授权

账号禁用、角色或范围变化、管理员恢复都会撤销相关 Session。遇到跨标签不一致时，以服务端
授权结果为准，关闭旧标签并重新登录；不要清除或伪造数据库中的 Session 行。403 表示当前
能力或数据范围不足，不应通过显示隐藏按钮来替代后端授权。

## 备份恢复与迁移故障

升级前备份数据库和产品文件根，并记录 source SHA、profile 与迁移版本。SQLite 备份前停止宿主；
PostgreSQL 备份必须覆盖产品 schema 与文件根。迁移失败或 schema mismatch 时保持 API/worker
停止，保存脱敏日志并恢复同一时点的完整备份。恢复后重新运行离线 migrate 与 Doctor；不执行
降级 SQL，不从较新 schema 启动较旧 binary，也不只恢复数据库而遗漏文件内容。
