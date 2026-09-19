package accountlifecyclemigration

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

func TestMigrationPublishesEquivalentInboxForBothDialects(t *testing.T) {
	for _, dialect := range []database.Dialect{database.DialectSQLite, database.DialectPostgres} {
		migrationFS, err := (Provider{}).Migrations(dialect)
		if err != nil {
			t.Fatal(err)
		}
		content, err := fs.ReadFile(migrationFS, "7520000000000_files_account_lifecycle.sql")
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		for _, required := range []string{"files_account_lifecycle_events", "account-deletion", "claimed", "failed", "canceled"} {
			if !strings.Contains(text, required) {
				t.Fatalf("%s migration is missing %q", dialect, required)
			}
		}
	}
}
