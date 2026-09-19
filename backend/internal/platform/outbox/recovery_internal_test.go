package outbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

func TestClaimIsExclusiveAndCancellationKeepsIdentity(t *testing.T) {
	t.Parallel()
	db := openFaultDatabase(t)
	store := newFaultStore(t, db)
	now := time.Date(2026, 8, 26, 13, 30, 0, 0, time.UTC)
	enqueueFaultEvent(t, db, store, faultEvent("event-concurrent-1", "order:concurrent", now))

	start := make(chan struct{})
	winners := make(chan string, 8)
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(owner string) {
			defer workers.Done()
			<-start
			var records []Record
			err := db.WithinTx(context.Background(), func(ctx context.Context, tx database.Tx) error {
				var err error
				records, err = store.claim(ctx, tx, claimOptions{Owner: owner, Now: now, Lease: time.Minute, Limit: 1})
				return err
			})
			if err == nil && len(records) == 1 {
				winners <- owner
			}
		}(fmt.Sprintf("worker-%d", worker))
	}
	close(start)
	workers.Wait()
	close(winners)
	if len(winners) != 1 {
		t.Fatalf("claim winners = %d, want 1", len(winners))
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	err := db.WithinTx(context.Background(), func(_ context.Context, tx database.Tx) error {
		_, err := store.claim(canceled, tx, claimOptions{Owner: "worker-a", Now: now, Lease: time.Minute, Limit: 1})
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("claim(canceled) error = %v", err)
	}
}

func TestExpiredAndReusedOwnerClaimsCannotSettle(t *testing.T) {
	t.Parallel()
	db := openFaultDatabase(t)
	if _, err := db.SQL().Exec(`CREATE TABLE orders_effects (business_key TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create effects: %v", err)
	}
	store := newFaultStore(t, db)
	consumer, err := NewTransactionalConsumer("orders", "projector", []string{"orders_effects"}, Mutation{
		Operation: OperationInsert, Table: "orders_effects",
		Values: []ColumnBinding{{Column: "business_key", Field: FieldBusinessKey}}, ExpectExactly: 1,
	})
	if err != nil {
		t.Fatalf("NewTransactionalConsumer() error = %v", err)
	}
	now := time.Date(2026, 8, 26, 14, 30, 0, 0, time.UTC)
	event := faultEvent("event-fence-1", "order:fence", now)
	enqueueFaultEvent(t, db, store, event)
	stale := claimFaultEvents(t, db, store, claimOptions{Owner: "worker-a", Now: now, Lease: time.Minute, Limit: 1})[0]

	err = db.WithinTx(context.Background(), func(ctx context.Context, tx database.Tx) error {
		_, err := store.deliver(ctx, tx, stale, consumer, func() time.Time { return now.Add(time.Minute) })
		return err
	})
	if !errors.Is(err, ErrClaimLost) {
		t.Fatalf("deliver(expired) error = %v", err)
	}
	if effects := effectCount(t, db); effects != 0 {
		t.Fatalf("expired owner effects = %d", effects)
	}

	current := claimFaultEvents(t, db, store, claimOptions{Owner: "worker-a", Now: now.Add(2 * time.Minute), Lease: time.Minute, Limit: 1})[0]
	if current.Attempts != 2 || current.claimToken == stale.claimToken {
		t.Fatalf("same-owner reclaim did not rotate fence: stale %#v, current %#v", stale, current)
	}
	err = db.WithinTx(context.Background(), func(ctx context.Context, tx database.Tx) error {
		return store.retry(ctx, tx, stale, now.Add(2*time.Minute), now.Add(3*time.Minute), "consumer_failed")
	})
	if !errors.Is(err, ErrClaimLost) {
		t.Fatalf("retry(stale token) error = %v", err)
	}
}

func faultEvent(id, key string, at time.Time) Event {
	return Event{ID: id, Topic: "orders.changed", BusinessKey: key, Payload: []byte(`{"revision":1}`), OccurredAt: at}
}

func enqueueFaultEvent(t *testing.T, db *database.Database, store *Store, event Event) {
	t.Helper()
	if err := db.WithinTx(context.Background(), func(ctx context.Context, tx database.Tx) error {
		_, err := store.Enqueue(ctx, tx, event)
		return err
	}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
}

func claimFaultEvents(t *testing.T, db *database.Database, store *Store, options claimOptions) []Record {
	t.Helper()
	var records []Record
	if err := db.WithinTx(context.Background(), func(ctx context.Context, tx database.Tx) error {
		var err error
		records, err = store.claim(ctx, tx, options)
		return err
	}); err != nil {
		t.Fatalf("claim() error = %v", err)
	}
	return records
}

func effectCount(t *testing.T, db *database.Database) int {
	t.Helper()
	var count int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM orders_effects`).Scan(&count); err != nil {
		t.Fatalf("count effects: %v", err)
	}
	return count
}
