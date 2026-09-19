package reliableruntime_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/coordination"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/outbox"
)

func TestStoreExecutionMethodsAreNotPublic(t *testing.T) {
	t.Parallel()
	storeType := reflect.TypeFor[*outbox.Store]()
	for _, method := range []string{"Claim", "Deliver", "Retry"} {
		if _, exposed := storeType.MethodByName(method); exposed {
			t.Fatalf("Store exposes execution method %s", method)
		}
	}
}

func TestTopicSchemaRejectsUnknownSensitiveAndUnstructuredPayloads(t *testing.T) {
	t.Parallel()
	db := openReliableSQLite(t)
	store := newReliableStore(t, db)
	now := time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC)
	valid := reliableEvent("event-schema-1", "order:42", now)
	valid.Payload = []byte(" { \"tokenCount\" : 2, \"revision\" : 1, \"sessionTimeout\" : 30 } ")
	enqueueEvent(t, db, store, valid)
	record, found, err := store.Lookup(context.Background(), valid.ID)
	if err != nil || !found || string(record.Event.Payload) != `{"revision":1,"sessionTimeout":30,"tokenCount":2}` {
		t.Fatalf("normalized persisted payload = %q, found %v, err %v", record.Event.Payload, found, err)
	}

	invalid := []struct {
		key     string
		payload string
	}{
		{key: "plain-key", payload: `{"revision":1}`},
		{key: "order:password", payload: `{"revision":1}`},
		{key: "order:42", payload: `[]`},
		{key: "order:42", payload: `{"revision":1,"unknown":true}`},
		{key: "order:42", payload: `{"revision":1,"profile":{"secret":"value"}}`},
		{key: "order:42", payload: `{"revision":{"secret":"raw"},"revision":1}`},
	}
	for index, item := range invalid {
		event := reliableEvent("event-schema-invalid-"+string(rune('a'+index)), item.key, now)
		event.Payload = []byte(item.payload)
		err := enqueueError(db, store, event)
		if !errors.Is(err, outbox.ErrInvalidEvent) {
			t.Fatalf("Enqueue(%q, %s) error = %v", item.key, item.payload, err)
		}
	}
	stringEvent := outbox.Event{
		ID: "event-schema-sensitive-string", Topic: "configuration.changed", BusinessKey: "configuration:site-name:1",
		Payload: []byte(`{"value":"raw-session-secret"}`), OccurredAt: now,
	}
	if err := enqueueError(db, store, stringEvent); !errors.Is(err, outbox.ErrInvalidEvent) {
		t.Fatalf("Enqueue(unconstrained string payload) error = %v", err)
	}
}

func TestTopicSchemaRegistrationRejectsSensitiveFieldsButAllowsAggregateMetadata(t *testing.T) {
	t.Parallel()
	db := openReliableSQLite(t)
	for _, name := range []string{"password", "clientSecret", "rawSession", "accessToken", "credentialBundle"} {
		_, err := outbox.NewStore(db, outbox.TopicSchema{
			Topic: "orders.changed",
			Payload: []outbox.PayloadFieldSchema{{
				Name: name, Kind: outbox.PayloadString, AllowedStrings: []string{"enabled"},
			}},
			BusinessKey: outbox.BusinessKeySchema{Prefix: "order", MinParts: 1, MaxParts: 1},
		})
		if err == nil {
			t.Fatalf("sensitive schema field %q was accepted", name)
		}
	}
	if _, err := outbox.NewStore(db, outbox.TopicSchema{
		Topic:       "display.changed",
		Payload:     []outbox.PayloadFieldSchema{{Name: "displayMode", Kind: outbox.PayloadString}},
		BusinessKey: outbox.BusinessKeySchema{Prefix: "display", MinParts: 1, MaxParts: 1},
	}); err == nil {
		t.Fatal("string schema without explicit allowed values was accepted")
	}
	if _, err := outbox.NewStore(db, outbox.TopicSchema{
		Topic: "display.changed",
		Payload: []outbox.PayloadFieldSchema{{
			Name: "displayMode", Kind: outbox.PayloadString,
			AllowedStrings: []string{"compact", "read-only", "sessionless", "tokenized-mode"},
		}},
		BusinessKey: outbox.BusinessKeySchema{Prefix: "display", MinParts: 1, MaxParts: 1},
	}); err != nil {
		t.Fatalf("explicit string enum schema rejected: %v", err)
	}
	for _, value := range []string{"raw-session-secret", "access-token", "credential", "password-reset"} {
		_, err := outbox.NewStore(db, outbox.TopicSchema{
			Topic: "display.changed",
			Payload: []outbox.PayloadFieldSchema{{
				Name: "displayMode", Kind: outbox.PayloadString, AllowedStrings: []string{value},
			}},
			BusinessKey: outbox.BusinessKeySchema{Prefix: "display", MinParts: 1, MaxParts: 1},
		})
		if err == nil {
			t.Fatalf("sensitive enum label %q was accepted", value)
		}
	}
	if _, err := outbox.NewStore(db, outbox.TopicSchema{
		Topic: "orders.changed",
		Payload: []outbox.PayloadFieldSchema{
			{Name: "sessionTimeout", Kind: outbox.PayloadNumber},
			{Name: "tokenCount", Kind: outbox.PayloadNumber},
		},
		BusinessKey: outbox.BusinessKeySchema{Prefix: "order", MinParts: 1, MaxParts: 1},
	}); err != nil {
		t.Fatalf("aggregate metadata schema rejected: %v", err)
	}
}

func TestMutationDSLRejectsOwnershipAndUnboundedChanges(t *testing.T) {
	t.Parallel()
	mutationType := reflect.TypeFor[outbox.Mutation]()
	for _, escape := range []string{"Query", "Expression", "Callback", "Arguments"} {
		if _, exposed := mutationType.FieldByName(escape); exposed {
			t.Fatalf("Mutation exposes arbitrary execution field %s", escape)
		}
	}
	valid := outbox.Mutation{
		Operation: outbox.OperationInsert,
		Table:     "orders_effects",
		Values: []outbox.ColumnBinding{
			{Column: "business_key", Field: outbox.FieldBusinessKey},
			{Column: "payload", Field: outbox.FieldPayload},
		},
		ExpectExactly: 1,
	}
	if _, err := outbox.NewTransactionalConsumer("orders", "projector", []string{"orders_effects"}, valid); err != nil {
		t.Fatalf("valid mutation rejected: %v", err)
	}
	invalid := []outbox.Mutation{
		{Operation: outbox.OperationInsert, Table: "reliable_outbox", Values: valid.Values, ExpectExactly: 1},
		{Operation: outbox.OperationInsert, Table: "audit_effects", Values: valid.Values, ExpectExactly: 1},
		{Operation: outbox.OperationInsert, Table: `orders_"effects"`, Values: valid.Values, ExpectExactly: 1},
		{Operation: outbox.OperationInsert, Table: "orders_effects()", Values: valid.Values, ExpectExactly: 1},
		{Operation: outbox.OperationInsert, Table: "orders_\u6548\u679c", Values: valid.Values, ExpectExactly: 1},
		{Operation: outbox.OperationUpdate, Table: "orders_effects", Values: valid.Values, ExpectExactly: 1},
		{Operation: outbox.OperationDelete, Table: "orders_effects", Keys: []outbox.ColumnBinding{{Column: "id", Field: outbox.FieldEventID}}, ExpectExactly: 1},
		{Operation: outbox.OperationInsert, Table: "orders_effects", Values: valid.Values, ExpectExactly: 0},
	}
	for _, mutation := range invalid {
		if _, err := outbox.NewTransactionalConsumer("orders", "projector", []string{mutation.Table}, mutation); err == nil {
			t.Fatalf("unsafe mutation accepted: %#v", mutation)
		}
	}
}

func TestDispatcherRejectsLeaseFromAnotherDatabaseOrOwner(t *testing.T) {
	t.Parallel()
	firstDB := openReliableSQLite(t)
	secondDB := openReliableSQLite(t)
	store := newReliableStore(t, firstDB)
	dispatcher, err := outbox.NewDispatcher(store, outbox.DispatcherConfig{
		Owner: "worker-a", LeaseDuration: time.Minute, RetryDelay: time.Minute, BatchSize: 1,
	}, nil)
	if err != nil {
		t.Fatalf("NewDispatcher() error = %v", err)
	}
	otherDBLease, err := coordination.Acquire(context.Background(), secondDB, coordination.Config{Owner: "worker-a"})
	if err != nil {
		t.Fatalf("Acquire(other database) error = %v", err)
	}
	t.Cleanup(func() { _ = otherDBLease.Close(context.Background()) })
	if _, err := dispatcher.RunOnce(context.Background(), otherDBLease, time.Now().UTC()); !errors.Is(err, coordination.ErrLeaseMismatch) {
		t.Fatalf("RunOnce(other database) error = %v", err)
	}
	otherOwnerLease, err := coordination.Acquire(context.Background(), firstDB, coordination.Config{Owner: "worker-b"})
	if err != nil {
		t.Fatalf("Acquire(other owner) error = %v", err)
	}
	t.Cleanup(func() { _ = otherOwnerLease.Close(context.Background()) })
	if _, err := dispatcher.RunOnce(context.Background(), otherOwnerLease, time.Now().UTC()); !errors.Is(err, coordination.ErrLeaseMismatch) {
		t.Fatalf("RunOnce(other owner) error = %v", err)
	}
}
