package migrations_test

import (
	"context"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
)

const postgresDisposableDSNEnv = "GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN"

// TestPostgresConcurrentProvidersConverge is intentionally gated because it drops objects in the
// supplied database. Lead runs it against an isolated real PostgreSQL process in candidate E2E.
func TestPostgresConcurrentProvidersConverge(t *testing.T) {
	dsn := os.Getenv(postgresDisposableDSNEnv)
	if dsn == "" {
		t.Skip(postgresDisposableDSNEnv + " is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	firstDB := openPostgres(t, ctx, dsn)
	secondDB := openPostgres(t, ctx, dsn)
	for _, statement := range []string{`DROP TABLE IF EXISTS concurrent_migration_probe`, `DROP TABLE IF EXISTS goose_db_version`} {
		if _, err := firstDB.SQL().ExecContext(ctx, statement); err != nil {
			t.Fatalf("reset disposable database: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = firstDB.SQL().ExecContext(context.Background(), `DROP TABLE IF EXISTS concurrent_migration_probe`)
		_, _ = firstDB.SQL().ExecContext(context.Background(), `DROP TABLE IF EXISTS goose_db_version`)
	})

	newRunner := func() *migrations.Runner {
		t.Helper()
		runner, err := migrations.NewRunner(postgresProvider{files: fstest.MapFS{
			"00001_concurrent.sql": {Data: []byte("-- +goose Up\nCREATE TABLE concurrent_migration_probe (id bigint PRIMARY KEY);\nSELECT pg_sleep(0.25);\n")},
		}})
		if err != nil {
			t.Fatalf("NewRunner() error = %v", err)
		}
		return runner
	}
	runners := []*migrations.Runner{newRunner(), newRunner()}
	databases := []*database.Database{firstDB, secondDB}
	start := make(chan struct{})
	results := make(chan struct {
		result migrations.Result
		err    error
	}, 2)
	for index := range 2 {
		go func(index int) {
			<-start
			result, err := runners[index].Up(ctx, databases[index])
			results <- struct {
				result migrations.Result
				err    error
			}{result, err}
		}(index)
	}
	close(start)

	totalApplied := 0
	for range 2 {
		outcome := <-results
		if outcome.err != nil {
			t.Fatalf("concurrent Up() error = %v", outcome.err)
		}
		totalApplied += outcome.result.Applied
		if outcome.result.CurrentVersion != 1 {
			t.Fatalf("CurrentVersion = %d, want 1", outcome.result.CurrentVersion)
		}
	}
	if totalApplied != 1 {
		t.Fatalf("total applied migrations = %d, want 1", totalApplied)
	}
}

type postgresProvider struct{ files fstest.MapFS }

func (postgresProvider) Module() string { return "concurrency" }
func (p postgresProvider) Migrations(dialect database.Dialect) (fs.FS, error) {
	if dialect != database.DialectPostgres {
		return nil, fs.ErrInvalid
	}
	return p.files, nil
}

func openPostgres(t *testing.T, ctx context.Context, dsn string) *database.Database {
	t.Helper()
	db, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: dsn})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
