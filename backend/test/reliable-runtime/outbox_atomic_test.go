package reliableruntime_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
	reliablemigration "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations/reliable-runtime"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/outbox"
)

func TestDomainStateAndIntegrationEventCommitAtomically(t *testing.T) {
	t.Parallel()

	db := openReliableSQLite(t)
	if _, err := db.SQL().Exec(`CREATE TABLE command_state (business_key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatalf("create command state: %v", err)
	}
	store := newReliableStore(t, db)
	event := outbox.Event{
		ID:          "event-atomic-1",
		Topic:       "configuration.changed",
		BusinessKey: "configuration:site-name:1",
		Payload:     []byte(`{"value":"current"}`),
		OccurredAt:  time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC),
	}
	rollback := errors.New("rollback command")
	err := db.WithinTx(context.Background(), func(ctx context.Context, tx database.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO command_state (business_key, value) VALUES (?, ?)`, event.BusinessKey, "rolled-back"); err != nil {
			return err
		}
		if _, err := store.Enqueue(ctx, tx, event); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("transaction error = %v, want rollback", err)
	}
	assertCommandStateCount(t, db, 0)
	if _, found, err := store.Lookup(context.Background(), event.ID); err != nil || found {
		t.Fatalf("Lookup() after rollback = (found %v, err %v), want (false, nil)", found, err)
	}

	if err := db.WithinTx(context.Background(), func(ctx context.Context, tx database.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO command_state (business_key, value) VALUES (?, ?)`, event.BusinessKey, "committed"); err != nil {
			return err
		}
		created, err := store.Enqueue(ctx, tx, event)
		if err != nil {
			return err
		}
		if !created {
			t.Fatal("first Enqueue() did not create event")
		}
		return nil
	}); err != nil {
		t.Fatalf("committed transaction: %v", err)
	}
	assertCommandStateCount(t, db, 1)
	record, found, err := store.Lookup(context.Background(), event.ID)
	if err != nil || !found {
		t.Fatalf("Lookup() = (found %v, err %v), want (true, nil)", found, err)
	}
	if record.State != outbox.StatePending || record.Event.BusinessKey != event.BusinessKey {
		t.Fatalf("record = %#v", record)
	}
}

func openReliableSQLite(t *testing.T) *database.Database {
	t.Helper()
	db, err := database.NewProcess().Open(context.Background(), database.Config{
		Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "reliable.db"),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner, err := migrations.NewRunner(reliablemigration.Provider{})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	if _, err := runner.Up(context.Background(), db); err != nil {
		t.Fatalf("migrate reliable runtime: %v", err)
	}
	return db
}

func assertCommandStateCount(t *testing.T, db *database.Database, want int) {
	t.Helper()
	var count int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM command_state`).Scan(&count); err != nil {
		t.Fatalf("count command state: %v", err)
	}
	if count != want {
		t.Fatalf("command state count = %d, want %d", count, want)
	}
}
