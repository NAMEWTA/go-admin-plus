package recovery

import (
	"context"
	"sync"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

const recoveryAdvisoryLock int64 = 6300000000000

type DatabaseOfflineGuard struct {
	db    *database.Database
	local chan struct{}
}

func NewDatabaseOfflineGuard(db *database.Database) (*DatabaseOfflineGuard, error) {
	if db == nil {
		return nil, ErrInvalidArgument
	}
	guard := &DatabaseOfflineGuard{db: db, local: make(chan struct{}, 1)}
	guard.local <- struct{}{}
	return guard, nil
}

func (guard *DatabaseOfflineGuard) Acquire(ctx context.Context) (func() error, error) {
	if guard == nil || guard.db == nil {
		return nil, ErrOfflineRequired
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-guard.local:
	}
	releaseLocal := sync.OnceFunc(func() { guard.local <- struct{}{} })
	if guard.db.Dialect() == database.DialectSQLite {
		return func() error { releaseLocal(); return nil }, nil
	}
	connection, err := guard.db.SQL().Conn(ctx)
	if err != nil {
		releaseLocal()
		return nil, ErrOfflineRequired
	}
	var acquired bool
	if err := connection.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, recoveryAdvisoryLock).Scan(&acquired); err != nil || !acquired {
		_ = connection.Close()
		releaseLocal()
		return nil, ErrOfflineRequired
	}
	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() {
			defer releaseLocal()
			var released bool
			err := connection.QueryRowContext(context.Background(), `SELECT pg_advisory_unlock($1)`, recoveryAdvisoryLock).Scan(&released)
			closeErr := connection.Close()
			if err != nil || !released || closeErr != nil {
				releaseErr = ErrInternal
			}
		})
		return releaseErr
	}, nil
}

func AcquireRuntimePresence(ctx context.Context, db *database.Database) (func() error, error) {
	if db == nil {
		return nil, ErrInvalidArgument
	}
	if db.Dialect() == database.DialectSQLite {
		return func() error { return nil }, nil
	}
	connection, err := db.SQL().Conn(ctx)
	if err != nil {
		return nil, ErrInternal
	}
	if _, err := connection.ExecContext(ctx, `SELECT pg_advisory_lock_shared($1)`, recoveryAdvisoryLock); err != nil {
		_ = connection.Close()
		return nil, ErrInternal
	}
	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() {
			var released bool
			err := connection.QueryRowContext(context.Background(), `SELECT pg_advisory_unlock_shared($1)`, recoveryAdvisoryLock).Scan(&released)
			closeErr := connection.Close()
			if err != nil || !released || closeErr != nil {
				releaseErr = ErrInternal
			}
		})
		return releaseErr
	}, nil
}

var _ OfflineGuard = (*DatabaseOfflineGuard)(nil)
