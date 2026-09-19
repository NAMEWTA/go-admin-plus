package administration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	administrationmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0020-administration-schema"
	bootstraprecoverymigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0030-bootstrap-recovery"
	deletionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0060-account-lifecycle"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
	reliablemigration "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations/reliable-runtime"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/outbox"
)

const (
	deletionAdminID  = "admin-account-0001"
	deletionTargetID = "target-account-01"
	deletionTransfer = "transfer-account1"
)

func TestDeletionLifecycleTombstonesRevokesCancelsAndCompletes(t *testing.T) {
	db, store := openDeletionDatabase(t)
	now := time.Date(2026, 9, 1, 1, 2, 3, 456000000, time.UTC)
	service, err := NewDeletionService(db, store, WithDeletionClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	seedDeletionAccount(t, db, deletionTargetID, "target", false)
	seedDeletionAccount(t, db, deletionTransfer, "transfer", true)
	seedActiveSession(t, db, deletionTargetID)

	started, err := service.StartDeletion(context.Background(), deletionAdminID, StartDeletion{
		AccountID: deletionTargetID, Strategy: DeletionStrategyTransfer, TransferTargetID: deletionTransfer,
	})
	if err != nil || started.Status != DeletionStatusQueued || started.AuditReference == "" {
		t.Fatalf("StartDeletion() = %#v, %v", started, err)
	}
	var lifecycle, sessionState string
	if err := db.Bun().QueryRow(`SELECT lifecycle_state FROM iam_accounts WHERE id = ?`, deletionTargetID).Scan(&lifecycle); err != nil || lifecycle != "deletion-pending" {
		t.Fatalf("account lifecycle = %q, %v", lifecycle, err)
	}
	if err := db.Bun().QueryRow(`SELECT state FROM iam_sessions WHERE account_id = ?`, deletionTargetID).Scan(&sessionState); err != nil || sessionState != "revoked" {
		t.Fatalf("session state = %q, %v", sessionState, err)
	}
	var recoveryBlocks int
	if err := db.Bun().QueryRow(`SELECT COUNT(*) FROM iam_account_recovery_blocks WHERE account_id = ?`, deletionTargetID).Scan(&recoveryBlocks); err != nil || recoveryBlocks != 1 {
		t.Fatalf("recovery block = %d, %v", recoveryBlocks, err)
	}
	if record, found, err := store.Lookup(context.Background(), started.EventID); err != nil || !found || record.State != outbox.StatePending {
		t.Fatalf("outbox record = %#v, found %v, err %v", record, found, err)
	}

	claim, err := service.ClaimDeletion(context.Background(), started.ID)
	if err != nil || claim.AccountID != deletionTargetID || claim.TransferTargetID != deletionTransfer {
		t.Fatalf("ClaimAccountDeletion() = %#v, %v", claim, err)
	}
	if err := service.CancelDeletion(context.Background(), deletionAdminID, deletionTargetID); !errors.Is(err, ErrConflict) {
		t.Fatalf("CancelDeletion() after claim = %v", err)
	}
	if err := service.CompleteAccountDeletion(context.Background(), started.ID); err != nil {
		t.Fatal(err)
	}
	completed, err := service.GetDeletion(context.Background(), deletionAdminID, deletionTargetID)
	if err != nil || completed.Status != DeletionStatusCompleted {
		t.Fatalf("GetDeletion() = %#v, %v", completed, err)
	}
	var username, displayName, email, auditReference string
	if err := db.Bun().QueryRow(`SELECT username, display_name, email, audit_ref FROM iam_accounts WHERE id = ?`, deletionTargetID).Scan(&username, &displayName, &email, &auditReference); err != nil {
		t.Fatal(err)
	}
	if username == "target" || displayName == "Target" || email == "target@example.test" || auditReference != started.AuditReference {
		t.Fatalf("anonymized identity = %q %q %q %q", username, displayName, email, auditReference)
	}
	if _, err := service.ClaimDeletion(context.Background(), started.ID); err != nil {
		t.Fatalf("completed replay must be idempotent: %v", err)
	}
}

func TestPurgeCanCancelOnlyBeforeClaimAndRestoresDisabled(t *testing.T) {
	db, store := openDeletionDatabase(t)
	service, err := NewDeletionService(db, store)
	if err != nil {
		t.Fatal(err)
	}
	seedDeletionAccount(t, db, deletionTargetID, "target", false)
	started, err := service.StartDeletion(context.Background(), deletionAdminID, StartDeletion{
		AccountID: deletionTargetID, Strategy: DeletionStrategyPurge, PurgeConfirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CancelDeletion(context.Background(), deletionAdminID, deletionTargetID); err != nil {
		t.Fatal(err)
	}
	var lifecycle string
	var disabled any
	if err := db.Bun().QueryRow(`SELECT lifecycle_state, disabled_at FROM iam_accounts WHERE id = ?`, deletionTargetID).Scan(&lifecycle, &disabled); err != nil || lifecycle != "disabled" || disabled == nil {
		t.Fatalf("canceled account = %q %#v, %v", lifecycle, disabled, err)
	}
	var recoveryBlocks int
	if err := db.Bun().QueryRow(`SELECT COUNT(*) FROM iam_account_recovery_blocks WHERE account_id = ?`, deletionTargetID).Scan(&recoveryBlocks); err != nil || recoveryBlocks != 0 {
		t.Fatalf("canceled recovery block = %d, %v", recoveryBlocks, err)
	}
	claim, err := service.ClaimDeletion(context.Background(), started.ID)
	if err != nil || claim.AccountID != "" {
		t.Fatalf("canceled claim = %#v, %v", claim, err)
	}
}

func TestDeletionRejectsMissingPolicyInvalidTransferAndLastAdministrator(t *testing.T) {
	db, store := openDeletionDatabase(t)
	service, err := NewDeletionService(db, store)
	if err != nil {
		t.Fatal(err)
	}
	seedDeletionAccount(t, db, deletionTargetID, "target", false)
	for _, input := range []StartDeletion{
		{AccountID: deletionTargetID},
		{AccountID: deletionTargetID, Strategy: DeletionStrategyTransfer, TransferTargetID: deletionTargetID},
		{AccountID: deletionTargetID, Strategy: DeletionStrategyPurge},
	} {
		if _, err := service.StartDeletion(context.Background(), deletionAdminID, input); !errors.Is(err, ErrValidation) && !errors.Is(err, ErrConflict) {
			t.Fatalf("invalid deletion %#v = %v", input, err)
		}
	}
	if _, err := service.StartDeletion(context.Background(), deletionAdminID, StartDeletion{
		AccountID: deletionAdminID, Strategy: DeletionStrategyPurge, PurgeConfirmed: true,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("last administrator deletion = %v", err)
	}
}

func TestDeletionProtectsActiveTransferTarget(t *testing.T) {
	db, store := openDeletionDatabase(t)
	service, err := NewDeletionService(db, store)
	if err != nil {
		t.Fatal(err)
	}
	seedDeletionAccount(t, db, deletionTargetID, "source", false)
	seedDeletionAccount(t, db, deletionTransfer, "transfer", false)
	if _, err := service.StartDeletion(context.Background(), deletionAdminID, StartDeletion{
		AccountID: deletionTargetID, Strategy: DeletionStrategyTransfer, TransferTargetID: deletionTransfer,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartDeletion(context.Background(), deletionAdminID, StartDeletion{
		AccountID: deletionTransfer, Strategy: DeletionStrategyPurge, PurgeConfirmed: true,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("active transfer target deletion = %v", err)
	}
	var lifecycle string
	if err := db.Bun().QueryRow(`SELECT lifecycle_state FROM iam_accounts WHERE id = ?`, deletionTransfer).Scan(&lifecycle); err != nil || lifecycle != "active" {
		t.Fatalf("transfer target lifecycle = %q, %v", lifecycle, err)
	}
}

func openDeletionDatabase(t *testing.T) (*database.Database, *outbox.Store) {
	t.Helper()
	db, err := database.NewProcess().Open(context.Background(), database.Config{Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "deletion.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner, err := migrations.NewRunner(reliablemigration.Provider{}, sessionmigration.Provider{}, administrationmigration.Provider{}, bootstraprecoverymigration.Provider{}, deletionmigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store, err := outbox.NewStore(db, AccountDeletionRequestedTopicSchema())
	if err != nil {
		t.Fatal(err)
	}
	seedDeletionAccount(t, db, deletionAdminID, "admin", false)
	if _, err := db.Bun().Exec(`INSERT INTO iam_account_roles(account_id, role_id) VALUES (?, ?)`, deletionAdminID, "role-system-admin"); err != nil {
		t.Fatal(err)
	}
	return db, store
}

func seedDeletionAccount(t *testing.T, db *database.Database, id, username string, disabled bool) {
	t.Helper()
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var disabledAt any
	lifecycle := "active"
	if disabled {
		disabledAt, lifecycle = now, "disabled"
	}
	_, err := db.Bun().Exec(`INSERT INTO iam_accounts(id, username, display_name, email, password_hash, password_changed_at, created_at, updated_at, disabled_at, lifecycle_state)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, username, "Target", username+"@example.test", "$argon2id$v=19$m=65536,t=3,p=2$abcdefghijklmnop$abcdefghijklmnopqrstuvwxyz0123456789ABCDEF", now, now, now, disabledAt, lifecycle)
	if err != nil {
		t.Fatal(err)
	}
}

func seedActiveSession(t *testing.T, db *database.Database, accountID string) {
	t.Helper()
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	_, err := db.Bun().Exec(`INSERT INTO iam_sessions(id, account_id, token_hash, generation, csrf_hash, state, created_at, last_seen_at, idle_expires_at, absolute_expires_at, rotate_at)
		VALUES (?, ?, ?, 0, ?, 'active', ?, ?, ?, ?, ?)`, "s123456789012345678901234567890123456789012", accountID,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		now, now, now.Add(time.Hour), now.Add(24*time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
}
