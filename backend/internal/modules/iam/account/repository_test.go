package account

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

type sqliteTestError int

func (err sqliteTestError) Error() string { return "private SQLite detail" }
func (err sqliteTestError) Code() int     { return int(err) }

type postgresTestError string

func (err postgresTestError) Error() string    { return "private PostgreSQL detail" }
func (err postgresTestError) SQLState() string { return string(err) }

type createFailureTx struct {
	database.Tx
	err error
}

func (tx createFailureTx) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, tx.err
}

func createWithFailure(dialect database.Dialect, err error) error {
	repository := NewRepository(dialect)
	return repository.Create(context.Background(), createFailureTx{err: err}, Credential{}, time.Unix(0, 0))
}

func TestNormalizeCreateErrorMapsOnlyDialectUniqueViolations(t *testing.T) {
	tests := []struct {
		name    string
		dialect database.Dialect
		err     error
		want    error
	}{
		{name: "SQLite unique", dialect: database.DialectSQLite, err: fmt.Errorf("wrapped: %w", sqliteTestError(2067)), want: ErrConflict},
		{name: "SQLite primary key", dialect: database.DialectSQLite, err: sqliteTestError(1555), want: ErrConflict},
		{name: "PostgreSQL unique", dialect: database.DialectPostgres, err: fmt.Errorf("wrapped: %w", postgresTestError("23505")), want: ErrConflict},
		{name: "SQLite code under PostgreSQL", dialect: database.DialectPostgres, err: sqliteTestError(2067)},
		{name: "PostgreSQL code under SQLite", dialect: database.DialectSQLite, err: postgresTestError("23505")},
		{name: "SQLite foreign key", dialect: database.DialectSQLite, err: sqliteTestError(787)},
		{name: "PostgreSQL check", dialect: database.DialectPostgres, err: postgresTestError("23514")},
		{name: "ordinary failure", dialect: database.DialectSQLite, err: errors.New("private database path")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := createWithFailure(test.dialect, test.err)
			if test.want != nil {
				if !errors.Is(got, test.want) {
					t.Fatalf("normalizeCreateError() = %v, want %v", got, test.want)
				}
				return
			}
			if got == nil || errors.Is(got, ErrConflict) || got.Error() != "account creation failed" {
				t.Fatalf("ordinary database failure was not reduced to the internal fallback: %v", got)
			}
		})
	}
}

func TestNormalizeCreateErrorPreservesContextTermination(t *testing.T) {
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		if got := createWithFailure(database.DialectSQLite, fmt.Errorf("wrapped: %w", sentinel)); !errors.Is(got, sentinel) {
			t.Fatalf("normalizeCreateError() lost %v", sentinel)
		}
	}
}
