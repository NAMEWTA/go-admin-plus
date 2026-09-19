// Package auditing 定义模块共享的审计写入契约；存储实现由产品装配层注入。
package auditing

import (
	"context"
	"errors"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"time"
)

// Operation 只接受稳定动作与资源标识，不接收请求体、口令或任意对象快照。
type Operation struct {
	Action, ResourceType, ResourceID, ActorID, Code string
	Failed                                          bool
}
type OperationPort interface {
	RecordOperation(context.Context, database.Tx, Operation) error
}

func Write(recorder OperationPort, ctx context.Context, tx database.Tx, fact Operation) error {
	if recorder == nil {
		return nil
	}
	return recorder.RecordOperation(ctx, tx, fact)
}

// RecordFailure 在失败事务之外记录结果；记录失败不覆盖原始业务错误。
func RecordFailure(recorder OperationPort, ctx context.Context, db interface {
	WithinTx(context.Context, func(context.Context, database.Tx) error) error
}, fact Operation, cause error) {
	if recorder == nil || cause == nil || errors.Is(cause, context.Canceled) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	fact.Failed = true
	_ = db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error { return recorder.RecordOperation(ctx, tx, fact) })
}
