package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/app/product"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files"
	filesaccountlifecyclemigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/account_lifecycle_migration"
	filesmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/migrations/0010-files"
	capacitymigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/migrations/0020-capacity"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/account"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	administrationmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0020-administration-schema"
	bootstraprecoverymigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0030-bootstrap-recovery"
	sessionprotectionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0040-session-protection"
	accountlifecyclemigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0060-account-lifecycle"
	schedulermigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/scheduler/migrations"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
	reliablemigration "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations/reliable-runtime"
)

const fixturePassword = "administrator password"
const fixtureAccountID = "account-desktop-e2e"
const pendingAuditMigration = "8100000000000_audit.sql"

func main() {
	root := flag.String("root", "", "isolated native E2E root")
	mode := flag.String("mode", "previous", "previous, migration-failure, or verify")
	expectedRole := flag.String("expected-role", "", "role name required by verify mode")
	flag.Parse()
	if flag.NArg() != 0 || *root == "" || !filepath.IsAbs(*root) {
		fail()
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(*root))
	if err != nil || canonical != filepath.Clean(*root) {
		fail()
	}
	data := filepath.Join(canonical, "data")
	logs := filepath.Join(canonical, "logs")
	if err := os.MkdirAll(data, 0o700); err != nil {
		fail()
	}
	if err := os.MkdirAll(logs, 0o700); err != nil {
		fail()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	process := database.NewProcess()
	db, err := process.Open(ctx, database.Config{Profile: config.ProfileDesktopSQLite, SQLitePath: filepath.Join(data, "go-admin-plus.db")})
	if err != nil {
		fail()
	}
	defer db.Close()
	if *mode == "verify" {
		verifyFixture(ctx, db, data, *expectedRole)
		return
	}
	if *mode != "previous" && *mode != "migration-failure" {
		fail()
	}
	runner, err := previousMigrationRunner()
	if err != nil || validatePreviousMigrationBaseline(runner) != nil {
		fail()
	}
	if _, err := runner.Up(ctx, db); err != nil {
		fail()
	}
	registry, err := authorization.NewCapabilityRegistry(db)
	if err != nil || files.RegisterCapabilities(ctx, registry) != nil {
		fail()
	}
	if err := seedPreviousAdmin(ctx, db, time.Now().UTC()); err != nil {
		fail()
	}
	if *mode == "migration-failure" {
		if _, err := db.Bun().ExecContext(ctx, `CREATE TABLE audit_facts (migration_fault TEXT NOT NULL)`); err != nil {
			fail()
		}
	}
	_, _ = fmt.Fprintln(os.Stdout, `{"state":"ready"}`)
}

func seedPreviousAdmin(ctx context.Context, db *database.Database, now time.Time) error {
	hash, err := account.HashPassword(fixturePassword)
	if err != nil {
		return err
	}
	repository := account.NewRepository(db.Dialect())
	return db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		err := repository.Create(ctx, tx, account.Credential{
			Profile:      account.Profile{ID: fixtureAccountID, Username: "admin", DisplayName: "Administrator", Email: "admin@example.test"},
			PasswordHash: hash,
		}, now)
		if err != nil && !errors.Is(err, account.ErrConflict) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO iam_bootstrap_state(marker, account_id, initialized_at) VALUES (1, ?, ?) ON CONFLICT(marker) DO NOTHING`, fixtureAccountID, now); err != nil {
			return err
		}
		var markerAccountID string
		if err := tx.QueryRowContext(ctx, `SELECT account_id FROM iam_bootstrap_state WHERE marker = 1`).Scan(&markerAccountID); err != nil {
			return err
		}
		if markerAccountID != fixtureAccountID {
			return errors.New("desktop fixture bootstrap marker invalid")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO iam_account_roles(account_id, role_id) VALUES (?, ?) ON CONFLICT(account_id, role_id) DO NOTHING`, fixtureAccountID, "role-system-admin")
		return err
	})
}

func previousMigrationRunner() (*migrations.Runner, error) {
	return migrations.NewRunner(
		sessionmigration.Provider{},
		administrationmigration.Provider{},
		bootstraprecoverymigration.Provider{},
		sessionprotectionmigration.Provider{},
		accountlifecyclemigration.Provider{},
		schedulermigration.Provider{},
		filesmigration.Provider{},
		capacitymigration.Provider{},
		filesaccountlifecyclemigration.Provider{},
		reliablemigration.Provider{},
	)
}

func validatePreviousMigrationBaseline(previous *migrations.Runner) error {
	current, err := product.NewMigrationRunner()
	if err != nil {
		return err
	}
	previousNames, err := composedMigrationNames(previous)
	if err != nil {
		return err
	}
	currentNames, err := composedMigrationNames(current)
	if err != nil {
		return err
	}
	for name := range previousNames {
		if _, exists := currentNames[name]; !exists {
			return errors.New("desktop fixture migration baseline invalid")
		}
		delete(currentNames, name)
	}
	if len(currentNames) != 1 {
		return errors.New("desktop fixture migration baseline invalid")
	}
	if _, exists := currentNames[pendingAuditMigration]; !exists {
		return errors.New("desktop fixture migration baseline invalid")
	}
	return nil
}

func composedMigrationNames(runner *migrations.Runner) (map[string]struct{}, error) {
	if runner == nil {
		return nil, errors.New("desktop fixture migration baseline invalid")
	}
	composed, err := runner.Compose(database.DialectSQLite)
	if err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(composed, ".")
	if err != nil {
		return nil, err
	}
	names := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, errors.New("desktop fixture migration baseline invalid")
		}
		names[entry.Name()] = struct{}{}
	}
	return names, nil
}

func verifyFixture(ctx context.Context, db *database.Database, data, expectedRole string) {
	var version int64
	if err := db.Bun().NewRaw(`SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied = 1`).Scan(ctx, &version); err != nil || version != 8100000000000 {
		fail()
	}
	if expectedRole != "" {
		var count int
		if err := db.Bun().NewRaw(`SELECT COUNT(*) FROM iam_roles WHERE name = ?`, expectedRole).Scan(ctx, &count); err != nil || count != 1 {
			fail()
		}
	}
	entries, err := os.ReadDir(filepath.Join(data, "backups"))
	if err != nil || len(entries) == 0 {
		fail()
	}
	_, _ = fmt.Fprintln(os.Stdout, `{"state":"verified","version":8100000000000}`)
}

func fail() {
	_, _ = fmt.Fprintln(os.Stderr, "desktop fixture failed")
	os.Exit(1)
}
