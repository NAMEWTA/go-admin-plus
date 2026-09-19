package accountlifecyclemigration

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

func TestMigrationPublishesEquivalentLifecycleTablesForBothDialects(t *testing.T) {
	for _, dialect := range []database.Dialect{database.DialectSQLite, database.DialectPostgres} {
		migrationFS, err := (Provider{}).Migrations(dialect)
		if err != nil {
			t.Fatal(err)
		}
		content, err := fs.ReadFile(migrationFS, "7120000000000_iam_account_lifecycle.sql")
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		for _, required := range []string{"lifecycle_state", "iam_account_deletions", "deletion-pending", "account-deletion"} {
			if !strings.Contains(text, required) {
				t.Fatalf("%s migration is missing %q", dialect, required)
			}
		}
	}
}
