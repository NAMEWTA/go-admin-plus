package files_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	filesmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/migrations/0010-files"
	capacitymigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/migrations/0020-capacity"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
)

func TestFilesCapacitySQLiteDialectContract(t *testing.T) {
	for _, profile := range []config.Profile{config.ProfileServerSQLite, config.ProfileDesktopSQLite} {
		t.Run(string(profile), func(t *testing.T) {
			db, cleanup := openSQLite(t, profile)
			defer cleanup()
			runCapacityDialectContract(t, db)
		})
	}
}

func TestFilesCapacityPostgresDialectContract(t *testing.T) {
	if testing.Short() {
		t.Skip("real PostgreSQL capacity contract is not a short test")
	}
	if value := os.Getenv(postgresDSNEnvironment); value == "" {
		t.Skip(postgresDSNEnvironment + " is not configured")
	}
	db, cleanup, _ := openPostgres(t)
	defer cleanup()
	runCapacityDialectContract(t, db)
}

func runCapacityDialectContract(t *testing.T, db *database.Database) {
	t.Helper()
	ctx := context.Background()
	base, err := migrations.NewRunner(filesmigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	for index, fixture := range []struct {
		owner string
		size  int64
	}{{"account-a", 3}, {"account-a", 5}, {"account-b", 7}} {
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", index+1)
		storageKey := fmt.Sprintf("object-%032x", index+1)
		_, err := db.Bun().ExecContext(ctx, `INSERT INTO files_objects
			(id, owner_account_id, original_name, name_key, media_type, size_bytes, sha256, storage_key, temporary_key, state, revision, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, 'ready', 1, ?, ?)`, id, fixture.owner, "fixture.txt", "fixture.txt", "text/plain", fixture.size,
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", storageKey, now, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	runner, err := migrations.NewRunner(filesmigration.Provider{}, capacitymigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Up(ctx, db)
	if err != nil || result.Applied != 1 {
		t.Fatalf("capacity migration=%#v err=%v", result, err)
	}
	assertCounter(t, db, "global", "global", 15, 3)
	assertCounter(t, db, "account", "account-a", 8, 2)
	assertCounter(t, db, "account", "account-b", 7, 1)

	var wait sync.WaitGroup
	start := make(chan struct{})
	succeeded := make(chan bool, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			err := db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
				result, err := tx.ExecContext(ctx, `UPDATE files_capacity_counters SET reserved_objects = reserved_objects + 1
					WHERE scope_kind = ? AND scope_id = ? AND reserved_objects + 1 <= ?`, "account", "account-b", 2)
				if err != nil {
					return err
				}
				count, err := result.RowsAffected()
				if err == nil {
					succeeded <- count == 1
				}
				return err
			})
			if err != nil {
				t.Errorf("conditional capacity update: %v", err)
			}
		}()
	}
	close(start)
	wait.Wait()
	close(succeeded)
	wins := 0
	for success := range succeeded {
		if success {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("conditional capacity winners=%d", wins)
	}
}

func assertCounter(t *testing.T, db *database.Database, kind, id string, wantBytes, wantObjects int64) {
	t.Helper()
	var bytes, objects int64
	err := db.Bun().QueryRowContext(context.Background(), `SELECT reserved_bytes, reserved_objects FROM files_capacity_counters WHERE scope_kind = ? AND scope_id = ?`, kind, id).Scan(&bytes, &objects)
	if err != nil || bytes != wantBytes || objects != wantObjects {
		t.Fatalf("counter %s/%s=%d/%d err=%v want=%d/%d", kind, id, bytes, objects, err, wantBytes, wantObjects)
	}
}
