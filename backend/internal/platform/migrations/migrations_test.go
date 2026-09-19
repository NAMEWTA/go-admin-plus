package migrations_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
)

type provider struct {
	module string
	files  fstest.MapFS
}

func (p provider) Module() string                             { return p.module }
func (p provider) Migrations(database.Dialect) (fs.FS, error) { return p.files, nil }

func TestRunnerMigratesEmptyDatabaseAndIsRepeatable(t *testing.T) {
	t.Parallel()

	db := openSQLite(t)
	runner, err := migrations.NewRunner(provider{module: "identity", files: fstest.MapFS{
		"00001_create_accounts.sql": {Data: []byte("-- +goose Up\nCREATE TABLE accounts (id INTEGER PRIMARY KEY, name TEXT NOT NULL);\n")},
	}})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	first, err := runner.Up(context.Background(), db)
	if err != nil {
		t.Fatalf("first Up() error = %v", err)
	}
	if first.Applied != 1 {
		t.Fatalf("first Up().Applied = %d, want 1", first.Applied)
	}
	if first.CurrentVersion != 1 {
		t.Fatalf("first Up().CurrentVersion = %d, want 1", first.CurrentVersion)
	}
	second, err := runner.Up(context.Background(), db)
	if err != nil {
		t.Fatalf("second Up() error = %v", err)
	}
	if second.Applied != 0 {
		t.Fatalf("second Up().Applied = %d, want 0", second.Applied)
	}
	var table string
	if err := db.SQL().QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='accounts'`).Scan(&table); err != nil {
		t.Fatalf("query migrated table: %v", err)
	}
}

func TestRunnerRejectsDuplicateModulesAndVersions(t *testing.T) {
	t.Parallel()

	one := provider{module: "one", files: fstest.MapFS{"00001_one.sql": {Data: []byte("-- +goose Up\nSELECT 1;")}}}
	two := provider{module: "two", files: fstest.MapFS{"00001_two.sql": {Data: []byte("-- +goose Up\nSELECT 2;")}}}
	if _, err := migrations.NewRunner(one, one); err == nil {
		t.Fatal("NewRunner() accepted duplicate module")
	}
	runner, err := migrations.NewRunner(one, two)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	if _, err := runner.Compose(database.DialectSQLite); err == nil {
		t.Fatal("Compose() accepted duplicate migration version")
	}
}

func TestFailedMigrationIsAtomicAndDiagnosticIsRedacted(t *testing.T) {
	t.Parallel()

	db := openSQLite(t)
	runner, err := migrations.NewRunner(provider{module: "broken", files: fstest.MapFS{
		"00002_broken.sql": {Data: []byte("-- +goose Up\nCREATE TABLE should_not_exist (id INTEGER);\nINSERT INTO private_secret_table VALUES (1);\n")},
	}})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	if _, err := runner.Up(context.Background(), db); err == nil {
		t.Fatal("Up() unexpectedly succeeded")
	} else if got := err.Error(); got != "migration execution failed" {
		t.Fatalf("Up() error = %q, want redacted diagnostic", got)
	}
	var count int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='should_not_exist'`).Scan(&count); err != nil {
		t.Fatalf("query rollback state: %v", err)
	}
	if count != 0 {
		t.Fatalf("failed migration left %d table(s), want 0", count)
	}
}

func TestRunnerUpgradesPreviousGreenfieldFixture(t *testing.T) {
	t.Parallel()

	db := openSQLite(t)
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	fixturePath := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../../../database/fixtures/previous-greenfield/sqlite.sql"))
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if _, err := db.SQL().ExecContext(context.Background(), string(fixture)); err != nil {
		t.Fatalf("apply fixture: %v", err)
	}
	runner, err := migrations.NewRunner(provider{module: "platform", files: fstest.MapFS{
		"00001_previous.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n")},
		"00002_current.sql":  {Data: []byte("-- +goose Up\nALTER TABLE architecture_marker ADD COLUMN upgraded INTEGER NOT NULL DEFAULT 1;\n")},
	}})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	result, err := runner.Up(context.Background(), db)
	if err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if result.Applied != 1 {
		t.Fatalf("Up().Applied = %d, want 1", result.Applied)
	}
	var generation string
	var upgraded int
	if err := db.SQL().QueryRow(`SELECT generation, upgraded FROM architecture_marker WHERE id = 1`).Scan(&generation, &upgraded); err != nil {
		t.Fatalf("query upgraded fixture: %v", err)
	}
	if generation != "previous-greenfield" || upgraded != 1 {
		t.Fatalf("upgraded fixture = (%q, %d)", generation, upgraded)
	}
}

func TestComposeRejectsNonForwardOrNonAtomicFiles(t *testing.T) {
	t.Parallel()

	for name, source := range map[string]string{
		"down":                      "-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;",
		"mixed-case-envsub":         "-- +goose eNvSuB oN\n-- +goose Up\nSELECT 1;",
		"mixed-case-no-transaction": "-- +goose no transaction\n-- +goose Up\nSELECT 1;",
		"missing-up":                "SELECT 1;",
		"sql-before-up":             "SELECT 1;\n-- +goose Up\nSELECT 2;",
	} {
		t.Run(name, func(t *testing.T) {
			runner, err := migrations.NewRunner(provider{module: "module", files: fstest.MapFS{
				"00001_policy.sql": {Data: []byte(source)},
			}})
			if err != nil {
				t.Fatalf("NewRunner() error = %v", err)
			}
			if _, err := runner.Compose(database.DialectSQLite); err == nil {
				t.Fatal("Compose() accepted policy violation")
			}
		})
	}
}

func TestComposeAcceptsStatementBlockAndIgnoresSQLAnnotationSubstring(t *testing.T) {
	t.Parallel()

	runner, err := migrations.NewRunner(provider{module: "module", files: fstest.MapFS{
		"00001_function.sql": {Data: []byte("-- +goose Up\n-- +goose StatementBegin\nSELECT '-- +goose Down';\nSELECT 1;\n-- +goose StatementEnd\n")},
	}})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	if _, err := runner.Compose(database.DialectPostgres); err != nil {
		t.Fatalf("Compose() rejected valid statement block: %v", err)
	}
}

func TestCanceledMigrationPreservesCancellation(t *testing.T) {
	t.Parallel()

	db := openSQLite(t)
	runner, err := migrations.NewRunner(provider{module: "module", files: fstest.MapFS{
		"00001_cancel.sql": {Data: []byte("-- +goose Up\nCREATE TABLE canceled_table (id INTEGER);\n")},
	}})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.Up(ctx, db); !errors.Is(err, context.Canceled) {
		t.Fatalf("Up() error = %v, want context.Canceled", err)
	}
}

func openSQLite(t *testing.T) *database.Database {
	t.Helper()
	db, err := database.NewProcess().Open(context.Background(), database.Config{
		Profile: config.ProfileServerSQLite, SQLitePath: t.TempDir() + "/migration.db",
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
