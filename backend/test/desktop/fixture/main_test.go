package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/app/product"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

func TestPreviousMigrationBaselineAdvancesOnlyAudit(t *testing.T) {
	db := openFixtureDatabase(t)
	runner, err := previousMigrationRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePreviousMigrationBaseline(runner); err != nil {
		t.Fatal(err)
	}
	previous, err := runner.Up(context.Background(), db)
	if err != nil || previous.Applied != 11 || previous.CurrentVersion != 8_000_000_000_000 {
		t.Fatalf("previous migration result = %#v, %v", previous, err)
	}
	current, err := product.NewMigrationRunner()
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := current.Up(context.Background(), db)
	if err != nil || upgraded.Applied != 1 || upgraded.CurrentVersion != 8_100_000_000_000 {
		t.Fatalf("current migration result = %#v, %v", upgraded, err)
	}
}

func TestMigrationFailureFixtureRetainsAuditConflict(t *testing.T) {
	db := openFixtureDatabase(t)
	runner, err := previousMigrationRunner()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Bun().ExecContext(context.Background(), `CREATE TABLE audit_facts (migration_fault TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	current, err := product.NewMigrationRunner()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.Up(context.Background(), db); err == nil || err.Error() != "migration execution failed" {
		t.Fatalf("audit conflict error = %v", err)
	}
}

func TestPreviousFixtureSeedsConsistentBootstrapState(t *testing.T) {
	db := openFixtureDatabase(t)
	runner, err := previousMigrationRunner()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	if err := seedPreviousAdmin(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	if err := seedPreviousAdmin(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	var accounts, markers, roles int
	err = db.Bun().NewRaw(`SELECT
		(SELECT COUNT(*) FROM iam_accounts WHERE id = ?),
		(SELECT COUNT(*) FROM iam_bootstrap_state WHERE marker = 1 AND account_id = ?),
		(SELECT COUNT(*) FROM iam_account_roles WHERE account_id = ? AND role_id = 'role-system-admin')`,
		fixtureAccountID, fixtureAccountID, fixtureAccountID).Scan(context.Background(), &accounts, &markers, &roles)
	if err != nil {
		t.Fatal(err)
	}
	if accounts != 1 || markers != 1 || roles != 1 {
		t.Fatalf("fixture bootstrap state = accounts %d, markers %d, roles %d", accounts, markers, roles)
	}
}

func openFixtureDatabase(t *testing.T) *database.Database {
	t.Helper()
	db, err := database.NewProcess().Open(context.Background(), database.Config{
		Profile:    config.ProfileDesktopSQLite,
		SQLitePath: filepath.Join(t.TempDir(), "fixture.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
