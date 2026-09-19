package administration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

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

func TestPostgresAccountDeletionLifecycleContract(t *testing.T) {
	dsn := os.Getenv("GO_ADMIN_TEST_POSTGRES_IAM_DELETION_DSN")
	if dsn == "" {
		t.Skip("GO_ADMIN_TEST_POSTGRES_IAM_DELETION_DSN is required")
	}
	db, err := database.NewProcess().Open(context.Background(), database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: dsn})
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
	seedDeletionAccount(t, db, deletionTargetID, "target", false)
	if _, err := db.Bun().Exec(`INSERT INTO iam_account_roles(account_id, role_id) VALUES (?, ?)`, deletionAdminID, "role-system-admin"); err != nil {
		t.Fatal(err)
	}
	service, err := NewDeletionService(db, store)
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.StartDeletion(context.Background(), deletionAdminID, StartDeletion{
		AccountID: deletionTargetID, Strategy: DeletionStrategyPurge, PurgeConfirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ClaimDeletion(context.Background(), started.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteAccountDeletion(context.Background(), started.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Bun().Exec(`UPDATE iam_accounts SET disabled_at = NULL WHERE id = ?`, deletionTargetID); err == nil {
		t.Fatal("PostgreSQL tombstone trigger allowed account recovery")
	}

	for iteration := 0; iteration < 20; iteration++ {
		raceTargetID := fmt.Sprintf("claim-race-target-%02d", iteration)
		seedDeletionAccount(t, db, raceTargetID, fmt.Sprintf("claim-race-%02d", iteration), false)
		raceDeletion, err := service.StartDeletion(context.Background(), deletionAdminID, StartDeletion{
			AccountID: raceTargetID, Strategy: DeletionStrategyPurge, PurgeConfirmed: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var claim AccountDeletionClaim
		var claimErr, cancelErr error
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			claim, claimErr = service.ClaimDeletion(context.Background(), raceDeletion.ID)
		}()
		go func() {
			defer wait.Done()
			<-start
			cancelErr = service.CancelDeletion(context.Background(), deletionAdminID, raceTargetID)
		}()
		close(start)
		wait.Wait()
		claimWon := claimErr == nil && claim.AccountID == raceTargetID && errors.Is(cancelErr, ErrConflict)
		cancelWon := cancelErr == nil && claimErr == nil && claim.AccountID == ""
		if !claimWon && !cancelWon {
			t.Fatalf("claim/cancel fence %d: claim=%#v claimErr=%v cancelErr=%v", iteration, claim, claimErr, cancelErr)
		}
	}
}
