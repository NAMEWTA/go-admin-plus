package capacitymigration

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

func TestCapacityMigrationsOwnEquivalentCountersReservationsAndBackfill(t *testing.T) {
	for _, dialect := range []database.Dialect{database.DialectSQLite, database.DialectPostgres} {
		migration, err := (Provider{}).Migrations(dialect)
		if err != nil {
			t.Fatal(err)
		}
		content, err := fs.ReadFile(migration, "7510000000000_files_capacity.sql")
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ToLower(string(content))
		for _, required := range []string{"files_capacity_counters", "files_capacity_reservations", "reserved_bytes", "reserved_objects", "files_objects", "group by owner_account_id"} {
			if !strings.Contains(text, required) {
				t.Fatalf("%s migration misses %q", dialect, required)
			}
		}
		for _, forbidden := range []string{"tenant", "physical_path", "absolute_path"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s migration contains forbidden state %q", dialect, forbidden)
			}
		}
	}
}
