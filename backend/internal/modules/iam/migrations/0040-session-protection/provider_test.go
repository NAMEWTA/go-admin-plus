package sessionprotectionmigration

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

func TestMigrationPublishesEquivalentSessionProtectionForBothDialects(t *testing.T) {
	for _, dialect := range []database.Dialect{database.DialectSQLite, database.DialectPostgres} {
		migrationFS, err := (Provider{}).Migrations(dialect)
		if err != nil {
			t.Fatal(err)
		}
		content, err := fs.ReadFile(migrationFS, "6220000000000_iam_session_protection.sql")
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		for _, required := range []string{"family_id", "renewed_at", "renew_after_at", "iam_login_buckets", "account','source", "state = 'revoked'"} {
			if !strings.Contains(text, required) {
				t.Fatalf("%s migration is missing %q", dialect, required)
			}
		}
		stableCSRF, err := fs.ReadFile(migrationFS, "6221000000000_iam_stable_csrf.sql")
		if err != nil || !strings.Contains(string(stableCSRF), "csrf_token") || !strings.Contains(string(stableCSRF), "state = 'revoked'") {
			t.Fatalf("%s stable CSRF migration is incomplete: %v", dialect, err)
		}
	}
}
