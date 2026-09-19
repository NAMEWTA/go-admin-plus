package reliableruntime_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/outbox"
)

func TestOutboxBusinessKeyIsIdempotentAndImmutable(t *testing.T) {
	t.Parallel()
	db := openReliableSQLite(t)
	store := newReliableStore(t, db)
	event := reliableEvent("event-idempotent-1", "order:42", time.Date(2026, 8, 26, 13, 0, 0, 0, time.UTC))

	for attempt := 0; attempt < 2; attempt++ {
		created, err := enqueueResult(db, store, event)
		if err != nil || created != (attempt == 0) {
			t.Fatalf("Enqueue attempt %d = (%v, %v)", attempt, created, err)
		}
	}
	conflict := event
	conflict.ID = "event-idempotent-2"
	conflict.Payload = []byte(`{"revision":2}`)
	if err := enqueueError(db, store, conflict); !errors.Is(err, outbox.ErrBusinessKeyConflict) {
		t.Fatalf("conflicting Enqueue() error = %v", err)
	}
}

func TestOccurredAtUsesPostgresStableMicrosecondPrecision(t *testing.T) {
	t.Parallel()
	db := openReliableSQLite(t)
	store := newReliableStore(t, db)
	invalid := reliableEvent("event-nanos-1", "order:nanos", time.Date(2026, 8, 26, 14, 45, 0, 123456789, time.UTC))
	if err := enqueueError(db, store, invalid); !errors.Is(err, outbox.ErrInvalidEvent) {
		t.Fatalf("Enqueue(nanoseconds) error = %v", err)
	}
	valid := reliableEvent("event-micros-1", "order:micros", time.Date(2026, 8, 26, 14, 45, 0, 123456000, time.UTC))
	enqueueEvent(t, db, store, valid)
	if created, err := enqueueResult(db, store, valid); err != nil || created {
		t.Fatalf("Enqueue(microsecond replay) = (%v, %v)", created, err)
	}
}

func newReliableStore(t *testing.T, db *database.Database) *outbox.Store {
	t.Helper()
	store, err := outbox.NewStore(db,
		outbox.TopicSchema{
			Topic: "orders.changed",
			Payload: []outbox.PayloadFieldSchema{
				{Name: "revision", Kind: outbox.PayloadNumber, Required: true},
				{Name: "sessionTimeout", Kind: outbox.PayloadNumber},
				{Name: "tokenCount", Kind: outbox.PayloadNumber},
			},
			BusinessKey: outbox.BusinessKeySchema{Prefix: "order", MinParts: 1, MaxParts: 3},
		},
		outbox.TopicSchema{
			Topic:       "orders.replayed",
			Payload:     []outbox.PayloadFieldSchema{{Name: "revision", Kind: outbox.PayloadNumber, Required: true}},
			BusinessKey: outbox.BusinessKeySchema{Prefix: "order", MinParts: 1, MaxParts: 3},
		},
		outbox.TopicSchema{
			Topic: "configuration.changed",
			Payload: []outbox.PayloadFieldSchema{{
				Name: "value", Kind: outbox.PayloadString, Required: true, AllowedStrings: []string{"current"},
			}},
			BusinessKey: outbox.BusinessKeySchema{Prefix: "configuration", MinParts: 2, MaxParts: 2},
		},
	)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return store
}

func reliableEvent(id, businessKey string, at time.Time) outbox.Event {
	return outbox.Event{ID: id, Topic: "orders.changed", BusinessKey: businessKey, Payload: []byte(`{"revision":1}`), OccurredAt: at}
}

func enqueueEvent(t *testing.T, db *database.Database, store *outbox.Store, event outbox.Event) {
	t.Helper()
	if _, err := enqueueResult(db, store, event); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
}

func enqueueResult(db *database.Database, store *outbox.Store, event outbox.Event) (bool, error) {
	var created bool
	err := db.WithinTx(context.Background(), func(ctx context.Context, tx database.Tx) error {
		var err error
		created, err = store.Enqueue(ctx, tx, event)
		return err
	})
	return created, err
}

func enqueueError(db *database.Database, store *outbox.Store, event outbox.Event) error {
	_, err := enqueueResult(db, store, event)
	return err
}

func newEffectConsumer(t *testing.T, fail bool) outbox.TransactionalConsumer {
	t.Helper()
	allowed := []string{"orders_effects"}
	mutations := []outbox.Mutation{{
		Operation: outbox.OperationInsert, Table: "orders_effects",
		Values: []outbox.ColumnBinding{
			{Column: "business_key", Field: outbox.FieldBusinessKey},
			{Column: "payload", Field: outbox.FieldPayload},
		},
		ExpectExactly: 1,
	}}
	if fail {
		allowed = append(allowed, "orders_missing_effects")
		mutations = append(mutations, outbox.Mutation{
			Operation: outbox.OperationInsert, Table: "orders_missing_effects",
			Values:        []outbox.ColumnBinding{{Column: "business_key", Field: outbox.FieldBusinessKey}},
			ExpectExactly: 1,
		})
	}
	consumer, err := outbox.NewTransactionalConsumer("orders", "projector", allowed, mutations...)
	if err != nil {
		t.Fatalf("NewTransactionalConsumer() error = %v", err)
	}
	return consumer
}

func createEffectsTable(t *testing.T, db *database.Database) {
	t.Helper()
	if _, err := db.SQL().Exec(`CREATE TABLE orders_effects (business_key TEXT PRIMARY KEY, payload BLOB NOT NULL)`); err != nil {
		t.Fatalf("create effects: %v", err)
	}
}

func assertEffectCalls(t *testing.T, db *database.Database, key string, want int) {
	t.Helper()
	var got int
	err := db.SQL().QueryRow(`SELECT COUNT(*) FROM orders_effects WHERE business_key = ?`, key).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		got = 0
		err = nil
	}
	if err != nil || got != want {
		t.Fatalf("effect count = %d, err = %v, want %d", got, err, want)
	}
}
