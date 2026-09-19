package session

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/account"
	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	sessionprotectionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0040-session-protection"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
)

const postgresDisposableDSNEnv = "GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN"

type postgresLoginFactNoop struct{}

func (postgresLoginFactNoop) RecordLoginFact(context.Context, database.Tx, LoginFact) error {
	return nil
}

// TestPostgresGenerationFencesConcurrentRenewalAndRevoke is intentionally gated.
// Lead runs it against an isolated real PostgreSQL database in candidate verification.
func TestPostgresGenerationFencesConcurrentRenewalAndRevoke(t *testing.T) {
	dsn := os.Getenv(postgresDisposableDSNEnv)
	if dsn == "" {
		t.Skip(postgresDisposableDSNEnv + " is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adminDB := openPostgres(t, ctx, dsn)
	schema := fmt.Sprintf("iam_session_%d", time.Now().UnixNano())
	if _, err := adminDB.SQL().ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create isolated schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.SQL().ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	isolatedDSN, err := isolatedSessionPostgresDSN(dsn, schema)
	if err != nil {
		t.Fatal("parse disposable PostgreSQL connection material")
	}
	db := openPostgres(t, ctx, isolatedDSN)
	runner, err := migrations.NewRunner(sessionmigration.Provider{}, sessionprotectionmigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(ctx, db); err != nil {
		t.Fatalf("migrate isolated schema: %v", err)
	}

	now := time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC)
	hash, err := account.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	repository := account.NewRepository(db.Dialect())
	if err := db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		return repository.Create(ctx, tx, account.Credential{
			Profile:      account.Profile{ID: "account-00000001", Username: "admin", DisplayName: "Administrator", Email: "admin@example.test"},
			PasswordHash: hash,
		}, now)
	}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	policy, err := config.NewSessionPolicy(2*time.Hour, 8*time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	clock := now
	service, err := NewService(db, policy, WithClock(func() time.Time { return clock }), WithLoginFactPort(postgresLoginFactNoop{}))
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Login(ctx, "admin", "correct horse battery")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	clock = clock.Add(61 * time.Minute)

	renewalLocked := make(chan struct{})
	releaseRenewal := make(chan struct{})
	revokeRequested := make(chan struct{})
	revokeLocked := make(chan struct{})
	var renewalOnce, releaseOnce, requestOnce, revokeOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRenewal) }) }
	t.Cleanup(release)
	service.lockProbe = func(point accountLockPoint) {
		switch point {
		case accountLockHeld:
			renewalOnce.Do(func() {
				close(renewalLocked)
				<-releaseRenewal
			})
		case accountRevokeLockRequested:
			requestOnce.Do(func() { close(revokeRequested) })
		case accountRevokeLockHeld:
			revokeOnce.Do(func() { close(revokeLocked) })
		}
	}

	renewed := make(chan Issued, 1)
	renewalErr := make(chan error, 1)
	go func() {
		value, currentErr := service.Renew(ctx, issued.Token, issued.CSRF)
		renewed <- value
		renewalErr <- currentErr
	}()
	select {
	case <-renewalLocked:
	case <-ctx.Done():
		t.Fatal("renewal did not enter the account-locked critical section")
	}

	revokeErr := make(chan error, 1)
	go func() { revokeErr <- service.RevokeAccount(ctx, issued.Profile.ID) }()
	select {
	case <-revokeRequested:
	case <-ctx.Done():
		t.Fatal("revoke did not request the account lock")
	}
	select {
	case <-revokeLocked:
		t.Fatal("revoke acquired the account lock while renewal still held it")
	default:
	}
	release()

	continued := <-renewed
	if err := <-renewalErr; err != nil {
		t.Fatalf("locked renewal failed: %v", err)
	}
	if err := <-revokeErr; err != nil {
		t.Fatalf("queued revoke failed: %v", err)
	}
	select {
	case <-revokeLocked:
	default:
		t.Fatal("revoke did not acquire the account lock after renewal committed")
	}
	if continued.Token != "" || continued.CSRF != issued.CSRF {
		t.Fatal("renewal changed stable session credentials")
	}
	if _, err := service.Current(ctx, issued.Token); !errors.Is(err, ErrAuthentication) {
		t.Fatal("generation fence allowed a token after concurrent PostgreSQL revoke")
	}
}

func isolatedSessionPostgresDSN(dsn, schema string) (string, error) {
	suffix := strings.TrimPrefix(schema, "iam_session_")
	if suffix == "" || strings.Trim(suffix, "0123456789") != "" {
		return "", fmt.Errorf("invalid IAM session schema")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Host == "" || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", fmt.Errorf("invalid PostgreSQL URL")
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func TestIsolatedSessionPostgresDSNPreservesParameters(t *testing.T) {
	value, err := isolatedSessionPostgresDSN("postgres://localhost/database?application_name=session+runner&sslmode=disable", "iam_session_1234")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Query().Get("search_path") != "iam_session_1234" || parsed.Query().Get("application_name") != "session runner" || parsed.Query().Get("sslmode") != "disable" {
		t.Fatal("isolated IAM session DSN lost its search path or existing parameters")
	}
	if _, err := isolatedSessionPostgresDSN("postgres://localhost/database", "public"); err == nil {
		t.Fatal("invalid IAM session schema was accepted")
	}
}

func openPostgres(t *testing.T, ctx context.Context, dsn string) *database.Database {
	t.Helper()
	db, err := database.NewProcess().Open(ctx, database.Config{
		Profile:            config.ProfileServerPostgres,
		PostgresDSN:        dsn,
		MaxOpenConnections: 4,
		MaxIdleConnections: 4,
	})
	if err != nil {
		t.Fatalf("open disposable PostgreSQL database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
