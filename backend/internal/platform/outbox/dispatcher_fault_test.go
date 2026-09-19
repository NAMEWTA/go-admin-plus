package outbox

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/coordination"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
	reliablemigration "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations/reliable-runtime"
)

func TestLeaseLossAfterTransactionalConsumerRollsBackAndStops(t *testing.T) {
	t.Parallel()
	db := openFaultDatabase(t)
	if _, err := db.SQL().Exec(`CREATE TABLE orders_effects (business_key TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create effects: %v", err)
	}
	store := newFaultStore(t, db)
	now := time.Date(2026, 8, 26, 17, 30, 0, 0, time.UTC)
	event := Event{
		ID: "event-lease-loss-1", Topic: "orders.changed", BusinessKey: "order:lease-loss",
		Payload: []byte(`{"revision":1}`), OccurredAt: now,
	}
	if err := db.WithinTx(context.Background(), func(ctx context.Context, tx database.Tx) error {
		_, err := store.Enqueue(ctx, tx, event)
		return err
	}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	consumer, err := NewTransactionalConsumer("orders", "projector", []string{"orders_effects"}, Mutation{
		Operation: OperationInsert, Table: "orders_effects",
		Values: []ColumnBinding{{Column: "business_key", Field: FieldBusinessKey}}, ExpectExactly: 1,
	})
	if err != nil {
		t.Fatalf("NewTransactionalConsumer() error = %v", err)
	}
	dispatcher, err := NewDispatcher(store, DispatcherConfig{
		Owner: "worker-a", LeaseDuration: time.Minute, RetryDelay: 5 * time.Minute, BatchSize: 1,
		Now: func() time.Time { return now.Add(30 * time.Second) },
	}, map[string]TransactionalConsumer{event.Topic: consumer})
	if err != nil {
		t.Fatalf("NewDispatcher() error = %v", err)
	}
	executor := &loseAfterCallbackExecutor{db: db, loseOnCall: 2}
	result, err := dispatcher.runOnce(context.Background(), executor, now)
	if !errors.Is(err, coordination.ErrLeaseLost) || result.Claimed != 1 || executor.calls != 2 {
		t.Fatalf("runOnce() = %#v, err %v, calls %d", result, err, executor.calls)
	}
	var effects int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM orders_effects`).Scan(&effects); err != nil || effects != 0 {
		t.Fatalf("effects after lease loss = %d, err %v", effects, err)
	}
	record, found, err := store.Lookup(context.Background(), event.ID)
	if err != nil || !found || record.State != StateClaimed || record.LastErrorCode != "" {
		t.Fatalf("record after lease loss = %#v, found %v, err %v", record, found, err)
	}
	if _, err := dispatcher.RunOnce(context.Background(), nil, now); err == nil {
		t.Fatal("production RunOnce accepted a missing coordination lease")
	}
}

func TestLoopObservesDatabaseFailureAndBacksOff(t *testing.T) {
	t.Parallel()
	db := openFaultDatabase(t)
	store := newFaultStore(t, db)
	dispatcher, err := NewDispatcher(store, DispatcherConfig{
		Owner: "worker-a", LeaseDuration: time.Minute, RetryDelay: time.Minute, BatchSize: 1,
	}, nil)
	if err != nil {
		t.Fatalf("NewDispatcher() error = %v", err)
	}
	executor := failingExecutor{err: errors.New("database unavailable with password=hidden")}
	stop := errors.New("stop")
	var observations []LoopObservation
	err = dispatcher.run(context.Background(), executor, LoopOptions{
		PollInterval: time.Second, FailureBackoff: 30 * time.Second,
		Wait:     func(context.Context, time.Duration) error { return stop },
		Observer: ObserveFunc(func(value LoopObservation) { observations = append(observations, value) }),
	})
	if !errors.Is(err, stop) || len(observations) != 1 {
		t.Fatalf("run() error = %v, observations = %#v", err, observations)
	}
	got := observations[0]
	if got.Outcome != LoopDependencyFailure || got.Delay != 30*time.Second || !got.ActiveExecutor || got.LostLock {
		t.Fatalf("observation = %#v", got)
	}
}

type failingExecutor struct{ err error }

func (f failingExecutor) WithinTx(context.Context, func(context.Context, database.Tx) error) error {
	return f.err
}

type loseAfterCallbackExecutor struct {
	db         *database.Database
	loseOnCall int
	calls      int
}

func (e *loseAfterCallbackExecutor) WithinTx(ctx context.Context, fn func(context.Context, database.Tx) error) error {
	e.calls++
	call := e.calls
	return e.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		if call == e.loseOnCall {
			return coordination.ErrLeaseLost
		}
		return nil
	})
}

func openFaultDatabase(t *testing.T) *database.Database {
	t.Helper()
	db, err := database.NewProcess().Open(context.Background(), database.Config{
		Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "fault.db"),
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

func newFaultStore(t *testing.T, db *database.Database) *Store {
	t.Helper()
	store, err := NewStore(db, TopicSchema{
		Topic:       "orders.changed",
		Payload:     []PayloadFieldSchema{{Name: "revision", Kind: PayloadNumber, Required: true}},
		BusinessKey: BusinessKeySchema{Prefix: "order", MinParts: 1, MaxParts: 3},
	})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return store
}
