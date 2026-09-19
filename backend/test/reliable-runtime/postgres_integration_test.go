package reliableruntime_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/coordination"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
	reliablemigration "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations/reliable-runtime"
)

func TestPostgresExecutorExclusionAndTakeover(t *testing.T) {
	dsn := os.Getenv("GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN")
	if dsn == "" {
		t.Skip("set GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN to run the PostgreSQL process integration")
	}
	ctx := context.Background()
	firstDB := openReliablePostgres(t, ctx, dsn)
	secondDB := openReliablePostgres(t, ctx, dsn)
	first, err := coordination.Acquire(ctx, firstDB, coordination.Config{Owner: "worker-a"})
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	if _, err := coordination.Acquire(ctx, secondDB, coordination.Config{Owner: "worker-b"}); !errors.Is(err, coordination.ErrNotLeader) {
		t.Fatalf("contending Acquire() error = %v", err)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	takeover, err := coordination.Acquire(ctx, secondDB, coordination.Config{Owner: "worker-b"})
	if err != nil {
		t.Fatalf("takeover Acquire() error = %v", err)
	}
	if err := takeover.Close(ctx); err != nil {
		t.Fatalf("takeover Close() error = %v", err)
	}
}

func openReliablePostgres(t *testing.T, ctx context.Context, dsn string) *database.Database {
	t.Helper()
	db, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: dsn})
	if err != nil {
		t.Fatalf("Open(PostgreSQL) error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner, err := migrations.NewRunner(reliablemigration.Provider{})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	if _, err := runner.Up(ctx, db); err != nil {
		t.Fatalf("migrate reliable runtime: %v", err)
	}
	return db
}
