package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/account"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/bootstrap/secretinput"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

var (
	ErrAlreadyInitialized = errors.New("identity is already initialized")
	ErrInvalidArgument    = errors.New("bootstrap input is invalid")
	ErrInternal           = errors.New("bootstrap operation failed")
)

type Database interface {
	WithinTx(context.Context, func(context.Context, database.Tx) error) error
	Dialect() database.Dialect
}

type AuditPort interface {
	RecordBootstrap(context.Context, database.Tx, Fact) error
}

type Fact struct {
	AccountID  string
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
	Username    string
	DisplayName string
	Email       string
	Secret      Secret
}

func (Command) String() string   { return "bootstrap.Command{Secret:[redacted]}" }
func (Command) GoString() string { return "bootstrap.Command{Secret:[redacted]}" }

type Result struct{ AccountID string }

type Service struct {
	db      Database
	audit   AuditPort
	now     func() time.Time
	newID   func() (string, error)
	account *account.Repository
}

type Option func(*Service)

func WithClock(clock func() time.Time) Option { return func(service *Service) { service.now = clock } }
func WithIDGenerator(generator func() (string, error)) Option {
	return func(service *Service) { service.newID = generator }
}

func NewService(db Database, audit AuditPort, options ...Option) (*Service, error) {
	if db == nil || audit == nil {
		return nil, ErrInvalidArgument
	}
	service := &Service{db: db, audit: audit, now: time.Now, newID: func() (string, error) { return uuid.NewString(), nil }}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	if service.now == nil || service.newID == nil {
		return nil, ErrInvalidArgument
	}
	service.account = account.NewRepository(db.Dialect())
	return service, nil
}

func (service *Service) Bootstrap(ctx context.Context, command Command) (Result, error) {
	username := strings.ToLower(strings.TrimSpace(command.Username))
	displayName := strings.TrimSpace(command.DisplayName)
	email := strings.ToLower(strings.TrimSpace(command.Email))
	parsedEmail, emailErr := mail.ParseAddress(email)
	if len(username) < 3 || len(username) > 64 || displayName == "" || len(displayName) > 80 ||
		emailErr != nil || parsedEmail.Address != email || len(email) > 254 || !command.Secret.Valid() {
		return Result{}, ErrInvalidArgument
	}
	passwordHash, err := command.Secret.PasswordHash()
	if err != nil {
		return Result{}, ErrInvalidArgument
	}
	accountID, err := service.newID()
	if err != nil || len(accountID) < 16 || len(accountID) > 64 {
		return Result{}, ErrInternal
	}
	now := service.now().UTC()
	err = service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		var existing int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM iam_accounts`).Scan(&existing); err != nil {
			return err
		}
		if existing != 0 {
			return ErrAlreadyInitialized
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO iam_bootstrap_state(marker, account_id, initialized_at) VALUES (1, ?, ?) ON CONFLICT(marker) DO NOTHING`, accountID, now)
		if err != nil {
			return err
		}
		created, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if created != 1 {
			return ErrAlreadyInitialized
		}
		if err := service.account.Create(ctx, tx, account.Credential{Profile: account.Profile{
			ID: accountID, Username: username, DisplayName: displayName, Email: email,
		}, PasswordHash: passwordHash}, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO iam_account_roles(account_id, role_id) VALUES (?, 'role-system-admin') ON CONFLICT(account_id, role_id) DO NOTHING`, accountID); err != nil {
			return err
		}
		return service.audit.RecordBootstrap(ctx, tx, Fact{AccountID: accountID, OccurredAt: now})
	})
	if err != nil {
		if errors.Is(err, ErrAlreadyInitialized) {
			return Result{}, ErrAlreadyInitialized
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Result{}, errors.Join(ErrInternal, err)
		}
		return Result{}, ErrInternal
	}
	return Result{AccountID: accountID}, nil
}

func (result Result) String() string {
	return fmt.Sprintf("bootstrap.Result{AccountID:%q}", result.AccountID)
}
