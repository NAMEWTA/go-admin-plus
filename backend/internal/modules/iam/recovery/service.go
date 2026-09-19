package recovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/bootstrap/secretinput"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

var (
	ErrInvalidArgument = errors.New("recovery input is invalid")
	ErrOfflineRequired = errors.New("offline recovery lock is unavailable")
	ErrNotFound        = errors.New("recovery account was not found")
	ErrNotRecoverable  = errors.New("account cannot be recovered")
	ErrInternal        = errors.New("recovery operation failed")
)

type Reason string

const (
	ReasonLostAccess            Reason = "lost-access"
	ReasonCredentialCompromise  Reason = "credential-compromise"
	ReasonDisabledAdministrator Reason = "disabled-administrator"
)

type Database interface {
	WithinTx(context.Context, func(context.Context, database.Tx) error) error
	Dialect() database.Dialect
}

type OfflineGuard interface {
	Acquire(context.Context) (release func() error, err error)
}

type AuditPort interface {
	RecordRecovery(context.Context, database.Tx, Fact) error
}

type Fact struct {
	AccountID  string
	Reason     Reason
	OccurredAt time.Time
}

type Secret = secretinput.Value

func ReadSecret(reader io.Reader) (Secret, error) {
	value, err := secretinput.Read(reader)
	if err != nil {
		return Secret{}, ErrInvalidArgument
	}
	return value, nil
}

type Command struct {
	AccountID string
	Secret    Secret
	Reason    Reason
}

func (Command) String() string   { return "recovery.Command{Secret:[redacted]}" }
func (Command) GoString() string { return "recovery.Command{Secret:[redacted]}" }

type Result struct{ AccountID string }

type Service struct {
	db    Database
	guard OfflineGuard
	audit AuditPort
	now   func() time.Time
}

type Option func(*Service)

func WithClock(clock func() time.Time) Option { return func(service *Service) { service.now = clock } }

func NewService(db Database, guard OfflineGuard, audit AuditPort, options ...Option) (*Service, error) {
	if db == nil || guard == nil || audit == nil {
		return nil, ErrInvalidArgument
	}
	service := &Service{db: db, guard: guard, audit: audit, now: time.Now}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	if service.now == nil {
		return nil, ErrInvalidArgument
	}
	return service, nil
}

func (service *Service) RecoverAdmin(ctx context.Context, command Command) (result Result, resultErr error) {
	if len(command.AccountID) < 16 || len(command.AccountID) > 64 || !command.Secret.Valid() || !validReason(command.Reason) {
		return Result{}, ErrInvalidArgument
	}
	release, err := service.guard.Acquire(ctx)
	if err != nil || release == nil {
		return Result{}, ErrOfflineRequired
	}
	defer func() {
		if releaseErr := release(); releaseErr != nil && resultErr == nil {
			result = Result{}
			resultErr = ErrInternal
		}
	}()
	passwordHash, err := command.Secret.PasswordHash()
	if err != nil {
		return Result{}, ErrInvalidArgument
	}
	now := service.now().UTC()
	err = service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		var blocked int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM iam_account_recovery_blocks WHERE account_id = ?`, command.AccountID).Scan(&blocked); err != nil {
			return err
		}
		if blocked != 0 {
			return ErrNotRecoverable
		}
		query := `SELECT id FROM iam_accounts WHERE id = ?`
		if service.db.Dialect() == database.DialectPostgres {
			query += ` FOR UPDATE`
		}
		var accountID string
		if err := tx.QueryRowContext(ctx, query, command.AccountID).Scan(&accountID); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		updated, err := tx.ExecContext(ctx, `UPDATE iam_accounts SET password_hash = ?, password_changed_at = ?, session_generation = session_generation + 1, disabled_at = NULL, updated_at = ? WHERE id = ?`, passwordHash, now, now, accountID)
		if err != nil {
			return err
		}
		count, err := updated.RowsAffected()
		if err != nil || count != 1 {
			return ErrInternal
		}
		if _, err := tx.ExecContext(ctx, `UPDATE iam_sessions SET state = 'revoked', revoked_at = ? WHERE account_id = ? AND state IN ('active', 'rotated')`, now, accountID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO iam_account_roles(account_id, role_id) VALUES (?, 'role-system-admin') ON CONFLICT(account_id, role_id) DO NOTHING`, accountID); err != nil {
			return err
		}
		return service.audit.RecordRecovery(ctx, tx, Fact{AccountID: accountID, Reason: command.Reason, OccurredAt: now})
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			return Result{}, ErrNotFound
		case errors.Is(err, ErrNotRecoverable):
			return Result{}, ErrNotRecoverable
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return Result{}, errors.Join(ErrInternal, err)
		default:
			return Result{}, ErrInternal
		}
	}
	return Result{AccountID: command.AccountID}, nil
}

func BlockAccount(ctx context.Context, tx database.Tx, accountID string, blockedAt time.Time) error {
	if tx == nil || len(accountID) < 16 || len(accountID) > 64 || blockedAt.IsZero() {
		return ErrInvalidArgument
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO iam_account_recovery_blocks(account_id, blocked_at) VALUES (?, ?) ON CONFLICT(account_id) DO NOTHING`, accountID, blockedAt.UTC())
	if err != nil {
		return ErrInternal
	}
	return nil
}

func validReason(reason Reason) bool {
	return reason == ReasonLostAccess || reason == ReasonCredentialCompromise || reason == ReasonDisabledAdministrator
}

func (result Result) String() string {
	return fmt.Sprintf("recovery.Result{AccountID:%q}", result.AccountID)
}
