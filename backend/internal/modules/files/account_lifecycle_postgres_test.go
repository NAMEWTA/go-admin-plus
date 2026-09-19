package files

import (
	"context"
	"os"
	"testing"

	lifecyclemigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/account_lifecycle_migration"
	filesmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/migrations/0010-files"
	capacitymigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/migrations/0020-capacity"
	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	administrationmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0020-administration-schema"
	deletionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0060-account-lifecycle"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
)

func TestPostgresAccountLifecycleTransferAndPurgeContract(t *testing.T) {
	dsn := os.Getenv("GO_ADMIN_TEST_POSTGRES_FILES_LIFECYCLE_DSN")
	if dsn == "" {
		t.Skip("GO_ADMIN_TEST_POSTGRES_FILES_LIFECYCLE_DSN is required")
	}
	db, err := database.NewProcess().Open(context.Background(), database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner, err := migrations.NewRunner(sessionmigration.Provider{}, administrationmigration.Provider{}, deletionmigration.Provider{}, filesmigration.Provider{}, capacitymigration.Provider{}, lifecyclemigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	transferPort := &lifecyclePortStub{claim: AccountDeletionClaim{AccountID: "source-account-01", Strategy: AccountDeletionTransfer, TransferTargetID: "target-account-01"}}
	transfer, err := NewAccountLifecycle(db, &lifecycleStorageStub{}, transferPort)
	if err != nil {
		t.Fatal(err)
	}
	seedLifecycleFile(t, db, "source-account-01", "object-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 10)
	seedLifecycleInbox(t, db, "44444444-4444-4444-8444-444444444444", "source-account-01", "target-account-01", "transfer")
	if err := transfer.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	purgePort := &lifecyclePortStub{claim: AccountDeletionClaim{AccountID: "purge-account-001", Strategy: AccountDeletionPurge}}
	purge, err := NewAccountLifecycle(db, &lifecycleStorageStub{}, purgePort)
	if err != nil {
		t.Fatal(err)
	}
	seedLifecycleFile(t, db, "purge-account-001", "object-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 20)
	seedLifecycleInbox(t, db, "55555555-5555-4555-8555-555555555555", "purge-account-001", "none", "purge")
	if err := purge.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.Bun().QueryRow(`SELECT COUNT(*) FROM files_objects WHERE owner_account_id IN (?, ?)`, "source-account-01", "purge-account-001").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("remaining source objects = %d, %v", remaining, err)
	}
}
