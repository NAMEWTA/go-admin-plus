package product

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

func TestMigrateAppliesSQLiteProductSchemaIdempotently(t *testing.T) {
	snapshot, err := config.Load(config.Input{
		Profile: config.ProfileServerSQLite,
		CLI:     map[string]string{"database.path": filepath.Join(t.TempDir(), "product.sqlite3")},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := Migrate(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("first migration: %v", err)
	}
	if first.Applied == 0 || first.CurrentVersion == 0 {
		t.Fatalf("first migration result = %#v", first)
	}
	second, err := Migrate(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("second migration: %v", err)
	}
	if second.Applied != 0 || second.CurrentVersion != first.CurrentVersion {
		t.Fatalf("second migration result = %#v, first = %#v", second, first)
	}
}

func TestPrepareRuntimeSchemaRequiresExactExternallyMigratedVersion(t *testing.T) {
	ctx := context.Background()
	db, err := database.NewProcess().Open(ctx, database.Config{
		Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "schema.sqlite3"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := PrepareRuntimeSchema(ctx, db, false); !errors.Is(err, ErrSchemaIncompatible) {
		t.Fatalf("empty schema error = %v", err)
	}
	if err := PrepareRuntimeSchema(ctx, db, true); err != nil {
		t.Fatal(err)
	}
	if err := PrepareRuntimeSchema(ctx, db, false); err != nil {
		t.Fatalf("current schema rejected: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO goose_db_version(version_id, is_applied, tstamp) VALUES (?, TRUE, CURRENT_TIMESTAMP)`, int64(9_000_000_000_000)); err != nil {
		t.Fatal(err)
	}
	if err := PrepareRuntimeSchema(ctx, db, false); !errors.Is(err, ErrSchemaIncompatible) {
		t.Fatalf("unknown future schema error = %v", err)
	}
}

func TestMigrateRejectsNonServerProfileAndNilContext(t *testing.T) {
	desktop, err := config.Load(config.Input{Profile: config.ProfileDesktopSQLite, Desktop: config.DesktopMaterial{
		DataDirectory: filepath.Join(t.TempDir(), "data"), LogDirectory: filepath.Join(t.TempDir(), "logs"),
		LoopbackPort: 4321, StartupToken: "0123456789abcdef0123456789abcdef",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(context.Background(), desktop); err == nil {
		t.Fatal("Migrate accepted a Desktop profile")
	}
	if _, err := Migrate(nil, config.Snapshot{}); err == nil {
		t.Fatal("Migrate accepted a nil context")
	}
}
