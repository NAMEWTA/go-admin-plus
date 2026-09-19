package filesmigration

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

func TestFilesMigrationsOwnEquivalentPrivateRecoveryState(t *testing.T) {
	for _, dialect := range []database.Dialect{database.DialectSQLite, database.DialectPostgres} {
		migration, err := (Provider{}).Migrations(dialect)
		if err != nil {
			t.Fatal(err)
		}
		content, err := fs.ReadFile(migration, "7500000000000_files.sql")
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ToLower(string(content))
		for _, required := range []string{"files_objects", "pending", "ready", "deleting", "temporary_key", "claim_token", "claim_expires_at"} {
			if !strings.Contains(text, required) {
				t.Fatalf("%s migration misses %q", dialect, required)
			}
		}
		for _, forbidden := range []string{"tenant", "iam_accounts", "iam_roles", "physical_path", "absolute_path"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s migration contains forbidden state %q", dialect, forbidden)
			}
		}
	}
}
