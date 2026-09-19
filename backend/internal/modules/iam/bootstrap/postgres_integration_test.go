package bootstrap

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	administrationmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0020-administration-schema"
	bootstrapmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0030-bootstrap-recovery"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/recovery"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
)

const postgresDisposableDSNEnv = "GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN"

func TestPostgresConcurrentBootstrapAndRecoveryFence(t *testing.T) {
	dsn := os.Getenv(postgresDisposableDSNEnv)
	if dsn == "" {
		t.Skip(postgresDisposableDSNEnv + " is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	firstDB := openBootstrapPostgres(t, ctx, dsn)
	secondDB := openBootstrapPostgres(t, ctx, dsn)
	resetBootstrapPostgres(t, ctx, firstDB)
	runner, err := migrations.NewRunner(sessionmigration.Provider{}, administrationmigration.Provider{}, bootstrapmigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(ctx, firstDB); err != nil {
		t.Fatal(err)
	}
	if _, err := firstDB.SQL().ExecContext(ctx, `CREATE TABLE test_bootstrap_facts(account_id TEXT NOT NULL, occurred_at TIMESTAMPTZ NOT NULL)`); err != nil {
		t.Fatal(err)
	}

	secret, err := ReadSecret(strings.NewReader("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	services := make([]*Service, 2)
	for index, db := range []*database.Database{firstDB, secondDB} {
		accountID := []string{"account-postgres-0001", "account-postgres-0002"}[index]
		services[index], err = NewService(db, databaseBootstrapAudit{}, WithIDGenerator(func() (string, error) { return accountID, nil }))
		if err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for index := range services {
		go func(index int) {
			<-start
			_, callErr := services[index].Bootstrap(ctx, Command{
				Username: "postgres.admin." + string(rune('a'+index)), DisplayName: "Postgres Admin", Email: "postgres.admin." + string(rune('a'+index)) + "@example.test", Secret: secret,
			})
			results <- callErr
		}(index)
	}
	close(start)
	succeeded, conflicted := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrAlreadyInitialized):
			conflicted++
		default:
			t.Fatalf("Bootstrap() error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("outcomes = succeeded:%d conflicted:%d", succeeded, conflicted)
	}
	var accounts, markers int
	if err := firstDB.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM iam_accounts`).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if err := firstDB.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM iam_bootstrap_state`).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if accounts != 1 || markers != 1 {
		t.Fatalf("state = accounts:%d markers:%d", accounts, markers)
	}

	runtimeRelease, err := recovery.AcquireRuntimePresence(ctx, firstDB)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := recovery.NewDatabaseOfflineGuard(secondDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Acquire(ctx); !errors.Is(err, recovery.ErrOfflineRequired) {
		t.Fatalf("Acquire() with active runtime error = %v", err)
	}
	if err := runtimeRelease(); err != nil {
		t.Fatal(err)
	}
	exclusiveRelease, err := guard.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := exclusiveRelease(); err != nil {
		t.Fatal(err)
	}
}

func openBootstrapPostgres(t *testing.T, ctx context.Context, dsn string) *database.Database {
	t.Helper()
	db, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func resetBootstrapPostgres(t *testing.T, ctx context.Context, db *database.Database) {
	t.Helper()
	statements := []string{
		`DROP TABLE IF EXISTS test_bootstrap_facts CASCADE`,
		`DROP TABLE IF EXISTS iam_account_recovery_blocks CASCADE`,
		`DROP TABLE IF EXISTS iam_bootstrap_state CASCADE`,
		`DROP TABLE IF EXISTS iam_role_menus CASCADE`,
		`DROP TABLE IF EXISTS iam_role_permissions CASCADE`,
		`DROP TABLE IF EXISTS iam_account_roles CASCADE`,
		`DROP TABLE IF EXISTS iam_menus CASCADE`,
		`DROP TABLE IF EXISTS iam_roles CASCADE`,
		`DROP TABLE IF EXISTS iam_permissions CASCADE`,
		`DROP TABLE IF EXISTS iam_sessions CASCADE`,
		`DROP TABLE IF EXISTS iam_accounts CASCADE`,
		`DROP TABLE IF EXISTS goose_db_version CASCADE`,
	}
	for _, statement := range statements {
		if _, err := db.SQL().ExecContext(ctx, statement); err != nil {
			t.Fatalf("reset disposable postgres: %v", err)
		}
	}
}
