package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/account"
	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	administrationmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0020-administration-schema"
	bootstrapmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0030-bootstrap-recovery"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
)

func TestBootstrapCreatesExactlyOneSystemAdministrator(t *testing.T) {
	db := openBootstrapDatabase(t)
	audit := databaseBootstrapAudit{fail: false}
	service, err := NewService(db, audit,
		WithClock(func() time.Time { return time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC) }),
		WithIDGenerator(func() (string, error) { return "account-bootstrap-0001", nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := ReadSecret(strings.NewReader("correct horse battery staple\n"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Bootstrap(context.Background(), Command{
		Username: " First.Admin ", DisplayName: "First Administrator", Email: "ADMIN@example.test", Secret: secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AccountID != "account-bootstrap-0001" {
		t.Fatalf("account id = %q", result.AccountID)
	}
	if _, err := service.Bootstrap(context.Background(), Command{
		Username: "second.admin", DisplayName: "Second", Email: "second@example.test", Secret: secret,
	}); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("second Bootstrap() error = %v", err)
	}

	var accounts, roles, facts, markers int
	var passwordHash string
	var disabledAt any
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM iam_accounts`).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT password_hash, disabled_at FROM iam_accounts WHERE id = ?`, result.AccountID).Scan(&passwordHash, &disabledAt); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM iam_account_roles WHERE account_id = ? AND role_id = 'role-system-admin'`, result.AccountID).Scan(&roles); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM test_bootstrap_facts WHERE account_id = ?`, result.AccountID).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM iam_bootstrap_state`).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if accounts != 1 || roles != 1 || facts != 1 || markers != 1 {
		t.Fatalf("state = accounts:%d roles:%d facts:%d markers:%d", accounts, roles, facts, markers)
	}
	if disabledAt != nil || !account.VerifyPassword(passwordHash, "correct horse battery staple") {
		t.Fatal("bootstrap account is not an enabled login credential")
	}
}

func TestConcurrentBootstrapCommitsOnlyOneIdentity(t *testing.T) {
	db := openBootstrapDatabase(t)
	var idMu sync.Mutex
	nextID := 0
	service, err := NewService(db, databaseBootstrapAudit{}, WithIDGenerator(func() (string, error) {
		idMu.Lock()
		defer idMu.Unlock()
		nextID++
		return "account-concurrent-000" + string(rune('0'+nextID)), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	secret, err := ReadSecret(strings.NewReader("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, username := range []string{"first.admin", "second.admin"} {
		go func(username string) {
			<-start
			_, callErr := service.Bootstrap(context.Background(), Command{
				Username: username, DisplayName: username, Email: username + "@example.test", Secret: secret,
			})
			results <- callErr
		}(username)
	}
	close(start)
	succeeded, rejected := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrAlreadyInitialized):
			rejected++
		default:
			t.Fatalf("Bootstrap() error = %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("outcomes = succeeded:%d rejected:%d", succeeded, rejected)
	}
}

func TestBootstrapAuditFailureRollsBackIdentityAndMarker(t *testing.T) {
	db := openBootstrapDatabase(t)
	service, err := NewService(db, databaseBootstrapAudit{fail: true})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := ReadSecret(strings.NewReader("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Bootstrap(context.Background(), Command{
		Username: "first.admin", DisplayName: "First", Email: "first@example.test", Secret: secret,
	})
	if !errors.Is(err, ErrInternal) {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	for _, table := range []string{"iam_accounts", "iam_bootstrap_state"} {
		var count int
		if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s count = %d", table, count)
		}
	}
}

func TestBootstrapSecretNeverRendersOrSerializes(t *testing.T) {
	const raw = "correct horse battery staple"
	secret, err := ReadSecret(strings.NewReader(raw + "\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	command := Command{Username: "first.admin", Secret: secret}
	for name, rendered := range map[string]string{
		"secret": secret.String(), "secret-go": secret.GoString(), "command": command.String(), "command-go": command.GoString(),
	} {
		if strings.Contains(rendered, raw) || !strings.Contains(rendered, "[redacted]") {
			t.Fatalf("%s rendering = %q", name, rendered)
		}
	}
	payload, err := secret.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), raw) {
		t.Fatalf("secret JSON = %s", payload)
	}
}

type databaseBootstrapAudit struct{ fail bool }

func (port databaseBootstrapAudit) RecordBootstrap(ctx context.Context, tx database.Tx, fact Fact) error {
	if port.fail {
		return errors.New("injected audit failure")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO test_bootstrap_facts(account_id, occurred_at) VALUES (?, ?)`, fact.AccountID, fact.OccurredAt)
	return err
}

func openBootstrapDatabase(t *testing.T) *database.Database {
	t.Helper()
	db, err := database.NewProcess().Open(context.Background(), database.Config{
		Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "bootstrap.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner, err := migrations.NewRunner(sessionmigration.Provider{}, administrationmigration.Provider{}, bootstrapmigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`CREATE TABLE test_bootstrap_facts(account_id TEXT NOT NULL, occurred_at TIMESTAMP NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return db
}
