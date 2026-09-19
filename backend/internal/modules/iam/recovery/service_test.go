package recovery

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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

func TestRecoverAdminReenablesExistingAccountAndRevokesSessions(t *testing.T) {
	db := openRecoveryDatabase(t)
	now := time.Date(2026, 8, 31, 11, 0, 0, 0, time.UTC)
	oldHash, err := account.HashPassword("old administrator password")
	if err != nil {
		t.Fatal(err)
	}
	insertRecoveryAccount(t, db, "account-recovery-0001", oldHash, now, true)
	insertRecoverySession(t, db, "account-recovery-0001", now)
	service, err := NewService(db, allowGuard{}, databaseRecoveryAudit{}, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	secret, err := ReadSecret(strings.NewReader("new administrator password"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RecoverAdmin(context.Background(), Command{
		AccountID: "account-recovery-0001", Secret: secret, Reason: ReasonLostAccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AccountID != "account-recovery-0001" {
		t.Fatalf("account id = %q", result.AccountID)
	}
	var passwordHash string
	var disabledAt any
	var generation int64
	if err := db.SQL().QueryRow(`SELECT password_hash, disabled_at, session_generation FROM iam_accounts WHERE id = ?`, result.AccountID).Scan(&passwordHash, &disabledAt, &generation); err != nil {
		t.Fatal(err)
	}
	if !account.VerifyPassword(passwordHash, "new administrator password") || disabledAt != nil || generation != 1 {
		t.Fatalf("recovered credential = disabled:%v generation:%d", disabledAt, generation)
	}
	var roles, active, revoked, facts int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM iam_account_roles WHERE account_id = ? AND role_id = 'role-system-admin'`, result.AccountID).Scan(&roles); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM iam_sessions WHERE account_id = ? AND state = 'active'`, result.AccountID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM iam_sessions WHERE account_id = ? AND state = 'revoked'`, result.AccountID).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM test_recovery_facts WHERE account_id = ? AND reason = ?`, result.AccountID, ReasonLostAccess).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if roles != 1 || active != 0 || revoked != 1 || facts != 1 {
		t.Fatalf("state = roles:%d active:%d revoked:%d facts:%d", roles, active, revoked, facts)
	}
}

func TestRecoverAdminRejectsMissingAndBlockedAccounts(t *testing.T) {
	db := openRecoveryDatabase(t)
	service, err := NewService(db, allowGuard{}, databaseRecoveryAudit{})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := ReadSecret(strings.NewReader("new administrator password"))
	if err != nil {
		t.Fatal(err)
	}
	command := Command{AccountID: "account-recovery-0001", Secret: secret, Reason: ReasonLostAccess}
	if _, err := service.RecoverAdmin(context.Background(), command); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing RecoverAdmin() error = %v", err)
	}
	hash, err := account.HashPassword("old administrator password")
	if err != nil {
		t.Fatal(err)
	}
	insertRecoveryAccount(t, db, command.AccountID, hash, time.Now().UTC(), false)
	if err := db.WithinTx(context.Background(), func(ctx context.Context, tx database.Tx) error {
		return BlockAccount(ctx, tx, command.AccountID, time.Now().UTC())
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecoverAdmin(context.Background(), command); !errors.Is(err, ErrNotRecoverable) {
		t.Fatalf("blocked RecoverAdmin() error = %v", err)
	}
}

func TestRecoverAdminGuardAndAuditFailuresLeaveCredentialUntouched(t *testing.T) {
	for name, test := range map[string]struct {
		guard OfflineGuard
		audit AuditPort
	}{
		"guard": {guard: denyGuard{}, audit: databaseRecoveryAudit{}},
		"audit": {guard: allowGuard{}, audit: databaseRecoveryAudit{fail: true}},
	} {
		t.Run(name, func(t *testing.T) {
			db := openRecoveryDatabase(t)
			oldHash, err := account.HashPassword("old administrator password")
			if err != nil {
				t.Fatal(err)
			}
			insertRecoveryAccount(t, db, "account-recovery-0001", oldHash, time.Now().UTC(), true)
			service, err := NewService(db, test.guard, test.audit)
			if err != nil {
				t.Fatal(err)
			}
			secret, err := ReadSecret(strings.NewReader("new administrator password"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.RecoverAdmin(context.Background(), Command{AccountID: "account-recovery-0001", Secret: secret, Reason: ReasonLostAccess})
			if !errors.Is(err, ErrOfflineRequired) && !errors.Is(err, ErrInternal) {
				t.Fatalf("RecoverAdmin() error = %v", err)
			}
			var gotHash string
			var generation int64
			if err := db.SQL().QueryRow(`SELECT password_hash, session_generation FROM iam_accounts WHERE id = 'account-recovery-0001'`).Scan(&gotHash, &generation); err != nil {
				t.Fatal(err)
			}
			if gotHash != oldHash || generation != 0 {
				t.Fatal("failed recovery changed credential")
			}
		})
	}
}

func TestDatabaseOfflineGuardSerializesSQLiteRecoveryAndReleasesIdempotently(t *testing.T) {
	db := openRecoveryDatabase(t)
	guard, err := NewDatabaseOfflineGuard(db)
	if err != nil {
		t.Fatal(err)
	}
	release, err := guard.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	blockedContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := guard.Acquire(blockedContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended Acquire() error = %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatalf("second release = %v", err)
	}
	secondRelease, err := guard.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := secondRelease(); err != nil {
		t.Fatal(err)
	}
}

type allowGuard struct{}

func (allowGuard) Acquire(context.Context) (func() error, error) {
	return func() error { return nil }, nil
}

type denyGuard struct{}

func (denyGuard) Acquire(context.Context) (func() error, error) {
	return nil, errors.New("runtime active")
}

type databaseRecoveryAudit struct{ fail bool }

func (port databaseRecoveryAudit) RecordRecovery(ctx context.Context, tx database.Tx, fact Fact) error {
	if port.fail {
		return errors.New("injected audit failure")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO test_recovery_facts(account_id, reason, occurred_at) VALUES (?, ?, ?)`, fact.AccountID, fact.Reason, fact.OccurredAt)
	return err
}

func openRecoveryDatabase(t *testing.T) *database.Database {
	t.Helper()
	db, err := database.NewProcess().Open(context.Background(), database.Config{
		Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "recovery.db"),
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
	if _, err := db.SQL().Exec(`CREATE TABLE test_recovery_facts(account_id TEXT NOT NULL, reason TEXT NOT NULL, occurred_at TIMESTAMP NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func insertRecoveryAccount(t *testing.T, db *database.Database, id, passwordHash string, now time.Time, disabled bool) {
	t.Helper()
	var disabledAt any
	if disabled {
		disabledAt = now
	}
	_, err := db.SQL().Exec(`INSERT INTO iam_accounts(id, username, display_name, email, password_hash, password_changed_at, created_at, updated_at, disabled_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, "recovery.admin", "Recovery Administrator", "recovery@example.test", passwordHash, now, now, now, disabledAt)
	if err != nil {
		t.Fatal(err)
	}
}

func insertRecoverySession(t *testing.T, db *database.Database, accountID string, now time.Time) {
	t.Helper()
	_, err := db.SQL().Exec(`INSERT INTO iam_sessions(id, account_id, token_hash, generation, csrf_hash, state, created_at, last_seen_at, idle_expires_at, absolute_expires_at, rotate_at) VALUES (?, ?, ?, 0, ?, 'active', ?, ?, ?, ?, ?)`,
		strings.Repeat("s", 43), accountID, strings.Repeat("a", 64), strings.Repeat("b", 64), now, now, now.Add(time.Hour), now.Add(2*time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
}
