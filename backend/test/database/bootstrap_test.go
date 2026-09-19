package database_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	bootstrapmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0030-bootstrap-recovery"
	productdatabase "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

func TestBootstrapRecoveryMigrationDialectsStayAligned(t *testing.T) {
	provider := bootstrapmigration.Provider{}
	sqliteFS, err := provider.Migrations(productdatabase.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	postgresFS, err := provider.Migrations(productdatabase.DialectPostgres)
	if err != nil {
		t.Fatal(err)
	}
	sqliteSQL := readOnlyMigration(t, sqliteFS)
	postgresSQL := readOnlyMigration(t, postgresFS)
	for dialect, source := range map[string]string{"sqlite": sqliteSQL, "postgres": postgresSQL} {
		for _, contract := range []string{"iam_bootstrap_state", "marker", "account_id", "initialized_at", "iam_account_recovery_blocks", "blocked_at"} {
			if !strings.Contains(source, contract) {
				t.Fatalf("%s migration is missing %q", dialect, contract)
			}
		}
		for _, forbidden := range []string{"password_hash", "administrator password", "account-system-admin", "INSERT INTO iam_accounts"} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("%s migration contains credential material %q", dialect, forbidden)
			}
		}
	}
}

func TestRepositoryContainsNoStaticBootstrapSQL(t *testing.T) {
	root := repositoryRoot(t)
	for _, dialect := range []string{"sqlite", "postgres"} {
		matches, err := filepath.Glob(filepath.Join(root, "database", "bootstrap", dialect, "*.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 0 {
			t.Fatalf("%s static bootstrap SQL remains: %v", dialect, matches)
		}
	}
	readme, err := os.ReadFile(filepath.Join(root, "database", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"administrator password", "首次登录凭据", "001-system-admin.sql"} {
		if strings.Contains(string(readme), forbidden) {
			t.Fatalf("database documentation contains obsolete bootstrap material %q", forbidden)
		}
	}
}

func readOnlyMigration(t *testing.T, source fs.FS) string {
	t.Helper()
	entries, err := fs.ReadDir(source, ".")
	if err != nil || len(entries) != 1 {
		t.Fatalf("migration entries = %d, err=%v", len(entries), err)
	}
	payload, err := fs.ReadFile(source, entries[0].Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
}
