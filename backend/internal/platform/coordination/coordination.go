// Package coordination owns the database-backed exclusive worker execution lease.
package coordination

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"

	"github.com/uptrace/bun"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

// workerAdvisoryKey is intentionally not configurable: every deployment must contend for the
// same scheduler/outbox execution right.
const workerAdvisoryKey int64 = 0x676f61646d696e

var (
	ErrInvalidConfig = errors.New("executor coordination config is invalid")
	ErrNotLeader     = errors.New("executor coordination lease is held elsewhere")
	ErrLeaseLost     = errors.New("executor coordination lease was lost")
	ErrLeaseMismatch = errors.New("executor coordination lease does not own this runtime")
	ownerPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,254}$`)
	sqliteOwners     = struct {
		sync.Mutex
		active map[*database.Database]struct{}
	}{active: make(map[*database.Database]struct{})}
)

type Config struct {
	Owner string
}

type Lease struct {
	mu          sync.Mutex
	db          *database.Database
	conn        bun.Conn
	postgres    bool
	closed      bool
	advisoryKey int64
	owner       string
}

func Acquire(ctx context.Context, db *database.Database, config Config) (*Lease, error) {
	if db == nil || !ownerPattern.MatchString(config.Owner) {
		return nil, ErrInvalidConfig
	}
	switch db.Dialect() {
	case database.DialectSQLite:
		sqliteOwners.Lock()
		defer sqliteOwners.Unlock()
		if _, exists := sqliteOwners.active[db]; exists {
			return nil, ErrNotLeader
		}
		sqliteOwners.active[db] = struct{}{}
		return &Lease{db: db, owner: config.Owner}, nil
	case database.DialectPostgres:
		conn, err := db.Bun().Conn(ctx)
		if err != nil {
			return nil, coordinationError(ctx, "executor connection failed", err)
		}
		var acquired bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(?)`, workerAdvisoryKey).Scan(&acquired); err != nil {
			_ = conn.Close()
			return nil, coordinationError(ctx, "executor lease acquisition failed", err)
		}
		if !acquired {
			_ = conn.Close()
			return nil, ErrNotLeader
		}
		return &Lease{db: db, conn: conn, postgres: true, advisoryKey: workerAdvisoryKey, owner: config.Owner}, nil
	default:
		return nil, ErrInvalidConfig
	}
}

// Authorize binds an execution entry to the exact database and owner that acquired this lease.
func (l *Lease) Authorize(db *database.Database, owner string) error {
	if l == nil || db == nil || !ownerPattern.MatchString(owner) {
		return ErrLeaseMismatch
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrLeaseLost
	}
	if l.db != db || l.owner != owner {
		return ErrLeaseMismatch
	}
	return nil
}

// WithinTx ensures PostgreSQL work uses the same physical connection that owns the advisory lock.
func (l *Lease) WithinTx(ctx context.Context, fn func(context.Context, database.Tx) error) error {
	if l == nil || fn == nil {
		return ErrInvalidConfig
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrLeaseLost
	}
	if !l.postgres {
		if err := l.db.SQL().PingContext(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			l.loseSQLite()
			return ErrLeaseLost
		}
		err := l.db.WithinTx(ctx, fn)
		if err != nil {
			if probeErr := l.db.SQL().PingContext(ctx); probeErr != nil && ctx.Err() == nil {
				l.loseSQLite()
				return ErrLeaseLost
			}
		}
		return err
	}
	if _, err := l.conn.ExecContext(ctx, `SELECT 1`); err != nil {
		l.lose()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrLeaseLost
	}
	err := l.conn.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return fn(ctx, tx)
	})
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if _, probeErr := l.conn.ExecContext(ctx, `SELECT 1`); probeErr != nil {
		l.lose()
		return ErrLeaseLost
	}
	return err
}

func (l *Lease) Close(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if !l.postgres {
		sqliteOwners.Lock()
		delete(sqliteOwners.active, l.db)
		sqliteOwners.Unlock()
		return nil
	}
	var unlocked bool
	err := l.conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock(?)`, l.advisoryKey).Scan(&unlocked)
	closeErr := l.conn.Close()
	if err != nil {
		return coordinationError(ctx, "executor lease release failed", err)
	}
	if !unlocked || closeErr != nil {
		return ErrLeaseLost
	}
	return nil
}

func (l *Lease) lose() {
	l.closed = true
	_ = l.conn.Close()
}

func (l *Lease) loseSQLite() {
	l.closed = true
	sqliteOwners.Lock()
	delete(sqliteOwners.active, l.db)
	sqliteOwners.Unlock()
}

func coordinationError(ctx context.Context, stage string, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("%s: %w", stage, contextErr)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", stage, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", stage, context.DeadlineExceeded)
	}
	return errors.New(stage)
}
