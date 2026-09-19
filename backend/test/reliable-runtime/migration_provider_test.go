package reliableruntime_test

import (
	"testing"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
	reliablemigration "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations/reliable-runtime"
)

func TestReliableRuntimeMigrationComposesForBothDialects(t *testing.T) {
	t.Parallel()
	runner, err := migrations.NewRunner(reliablemigration.Provider{})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	for _, dialect := range []database.Dialect{database.DialectPostgres, database.DialectSQLite} {
		if _, err := runner.Compose(dialect); err != nil {
			t.Fatalf("Compose(%s) error = %v", dialect, err)
		}
	}
}
