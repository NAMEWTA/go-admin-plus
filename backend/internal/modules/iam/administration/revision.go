package administration

import (
	"context"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

type expectedRevision struct {
	table, id string
	revision  int64
}
type revisionKey struct{}

// checkExpectedRevision 必须在最终写事务中调用，和管理员约束共用顺序锁。
func checkExpectedRevision(ctx context.Context, tx database.Tx) error {
	expected, ok := ctx.Value(revisionKey{}).(expectedRevision)
	if !ok {
		return nil
	}
	if expected.table != "iam_accounts" && expected.table != "iam_roles" && expected.table != "iam_menus" {
		return ErrValidation
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM `+expected.table+` WHERE id=?`, expected.id).Scan(&revision); err != nil {
		return err
	}
	if revision != expected.revision {
		return ErrConflict
	}
	return nil
}
