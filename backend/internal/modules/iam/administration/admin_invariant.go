package administration

import (
	"context"
	"database/sql"
	"errors"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

// lockAdministrator 串行化管理员集合的变更，避免两个请求分别移除最后两名管理员。
// 必须在授权读取之前获取，保持 PostgreSQL 的锁顺序一致。
func lockAdministrator(ctx context.Context, tx database.Tx, dialect database.Dialect) error {
	if dialect != database.DialectPostgres {
		return nil
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM iam_roles WHERE role_key = ? FOR UPDATE`, systemAdministratorKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	}
	return err
}

func isAdministrator(ctx context.Context, tx database.Tx, accountID string) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM iam_accounts a
		JOIN iam_account_roles ar ON ar.account_id = a.id JOIN iam_roles r ON r.id = ar.role_id
		WHERE a.id = ? AND a.disabled_at IS NULL AND r.role_key = ? AND r.enabled = ?`, accountID, systemAdministratorKey, true).Scan(&count)
	return count > 0, err
}

func protectLastAdministrator(ctx context.Context, tx database.Tx, accountID string) error {
	admin, err := isAdministrator(ctx, tx, accountID)
	if err != nil || !admin {
		return err
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT a.id) FROM iam_accounts a
		JOIN iam_account_roles ar ON ar.account_id = a.id JOIN iam_roles r ON r.id = ar.role_id
		WHERE a.disabled_at IS NULL AND r.role_key = ? AND r.enabled = ?`, systemAdministratorKey, true).Scan(&count)
	if err != nil {
		return err
	}
	if count <= 1 {
		return ErrConflict
	}
	return nil
}
