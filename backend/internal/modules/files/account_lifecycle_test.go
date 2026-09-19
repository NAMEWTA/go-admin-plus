package files

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	lifecyclemigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/account_lifecycle_migration"
	filesmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/migrations/0010-files"
	capacitymigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/migrations/0020-capacity"
	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	administrationmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0020-administration-schema"
	deletionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0060-account-lifecycle"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/coordination"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
	reliablemigration "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations/reliable-runtime"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/outbox"
)

func TestAccountLifecycleTransfersOwnersAndCapacityIdempotently(t *testing.T) {
	db := openAccountLifecycleDatabase(t)
	port := &lifecyclePortStub{claim: AccountDeletionClaim{AccountID: "source-account-01", Strategy: AccountDeletionTransfer, TransferTargetID: "target-account-01"}}
	worker, err := NewAccountLifecycle(db, &lifecycleStorageStub{}, port, WithAccountLifecycleCapacityPolicy(DefaultCapacityPolicy()))
	if err != nil {
		t.Fatal(err)
	}
	seedLifecycleFile(t, db, "source-account-01", "object-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 10)
	deletionID := "11111111-1111-4111-8111-111111111111"
	seedLifecycleInbox(t, db, deletionID, "source-account-01", "target-account-01", "transfer")
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var owner, state string
	if err := db.Bun().QueryRow(`SELECT owner_account_id FROM files_objects WHERE storage_key = ?`, "object-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").Scan(&owner); err != nil || owner != "target-account-01" {
		t.Fatalf("owner = %q, %v", owner, err)
	}
	if err := db.Bun().QueryRow(`SELECT state FROM files_account_lifecycle_events WHERE business_key LIKE ?`, "account-deletion:"+deletionID+":%").Scan(&state); err != nil || state != "completed" {
		t.Fatalf("inbox state = %q, %v", state, err)
	}
	if port.completed != 1 {
		t.Fatalf("completion calls = %d", port.completed)
	}
}

func TestAccountLifecyclePurgeRecoversAfterPartialPhysicalDelete(t *testing.T) {
	db := openAccountLifecycleDatabase(t)
	storage := &lifecycleStorageStub{failKey: "object-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	port := &lifecyclePortStub{claim: AccountDeletionClaim{AccountID: "source-account-01", Strategy: AccountDeletionPurge}}
	now := time.Now().UTC()
	worker, err := NewAccountLifecycle(db, storage, port, WithAccountLifecycleClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	seedLifecycleFile(t, db, "source-account-01", "object-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 10)
	seedLifecycleFile(t, db, "source-account-01", "object-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 20)
	seedLifecycleInbox(t, db, "22222222-2222-4222-8222-222222222222", "source-account-01", "none", "purge")
	if err := worker.RunOnce(context.Background()); err == nil {
		t.Fatal("first purge unexpectedly succeeded")
	}
	storage.failKey = ""
	now = now.Add(2 * time.Minute)
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var objects int
	if err := db.Bun().QueryRow(`SELECT COUNT(*) FROM files_objects WHERE owner_account_id = ?`, "source-account-01").Scan(&objects); err != nil || objects != 0 {
		t.Fatalf("remaining objects = %d, %v", objects, err)
	}
	if port.failed != 1 || port.completed != 1 {
		t.Fatalf("port calls failed=%d completed=%d", port.failed, port.completed)
	}
}

func TestAccountLifecycleOutboxProjectionFeedsOwnedInbox(t *testing.T) {
	db := openAccountLifecycleDatabase(t)
	schema := outbox.TopicSchema{
		Topic: AccountDeletionRequestedTopic,
		Payload: []outbox.PayloadFieldSchema{
			{Name: "strategy", Kind: outbox.PayloadString, Required: true, AllowedStrings: []string{"transfer", "purge"}},
			{Name: "version", Kind: outbox.PayloadNumber, Required: true},
		},
		BusinessKey: outbox.BusinessKeySchema{Prefix: "account-deletion", MinParts: 3, MaxParts: 3},
	}
	store, err := outbox.NewStore(db, schema)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	event := outbox.Event{
		ID: "event-account-deletion-1", Topic: AccountDeletionRequestedTopic,
		BusinessKey: "account-deletion:33333333-3333-4333-8333-333333333333:source-account-01:target-account-01",
		Payload:     []byte(`{"strategy":"transfer","version":1}`), OccurredAt: now,
	}
	if err := db.WithinTx(context.Background(), func(ctx context.Context, tx database.Tx) error {
		_, err := store.Enqueue(ctx, tx, event)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	consumer, err := NewAccountDeletionRequestedConsumer()
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := outbox.NewDispatcher(store, outbox.DispatcherConfig{
		Owner: "lifecycle-worker", LeaseDuration: time.Minute, RetryDelay: time.Second, BatchSize: 10,
		Now: func() time.Time { return now },
	}, map[string]outbox.TransactionalConsumer{AccountDeletionRequestedTopic: consumer})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := coordination.Acquire(context.Background(), db, coordination.Config{Owner: "lifecycle-worker"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close(context.Background()) })
	result, err := dispatcher.RunOnce(context.Background(), lease, now)
	if err != nil || result.Delivered != 1 {
		t.Fatalf("RunOnce() = %#v, %v", result, err)
	}
	var state string
	if err := db.Bun().QueryRow(`SELECT state FROM files_account_lifecycle_events WHERE event_id = ?`, event.ID).Scan(&state); err != nil || state != "queued" {
		t.Fatalf("projected inbox = %q, %v", state, err)
	}
}

type lifecyclePortStub struct {
	claim     AccountDeletionClaim
	failed    int
	completed int
}

func (p *lifecyclePortStub) ClaimAccountDeletion(context.Context, string) (string, string, string, error) {
	return p.claim.AccountID, string(p.claim.Strategy), p.claim.TransferTargetID, nil
}
func (p *lifecyclePortStub) FailAccountDeletion(context.Context, string, string) error {
	p.failed++
	return nil
}
func (p *lifecyclePortStub) CompleteAccountDeletion(context.Context, string) error {
	p.completed++
	return nil
}

type lifecycleStorageStub struct{ failKey string }

func (*lifecycleStorageStub) Stage(context.Context, string, io.Reader) (StagedContent, error) {
	panic("not used")
}
func (*lifecycleStorageStub) Publish(context.Context, string, string) error       { panic("not used") }
func (*lifecycleStorageStub) Abort(context.Context, string) error                 { return nil }
func (*lifecycleStorageStub) Open(context.Context, string) (io.ReadCloser, error) { panic("not used") }
func (s *lifecycleStorageStub) Delete(_ context.Context, key string) error {
	if key == s.failKey {
		return errors.New("injected storage failure")
	}
	return nil
}
func (*lifecycleStorageStub) ObjectExists(context.Context, string) (bool, error) { panic("not used") }
func (*lifecycleStorageStub) TemporaryExists(context.Context, string) (bool, error) {
	panic("not used")
}

func openAccountLifecycleDatabase(t *testing.T) *database.Database {
	t.Helper()
	db, err := database.NewProcess().Open(context.Background(), database.Config{Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "files-lifecycle.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner, err := migrations.NewRunner(reliablemigration.Provider{}, sessionmigration.Provider{}, administrationmigration.Provider{}, deletionmigration.Provider{}, filesmigration.Provider{}, capacitymigration.Provider{}, lifecyclemigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedLifecycleFile(t *testing.T, db *database.Database, owner, storageKey string, size int64) {
	t.Helper()
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	id := "11111111-1111-1111-1111-" + storageKey[len(storageKey)-12:]
	_, err := db.Bun().Exec(`INSERT INTO files_objects(id, owner_account_id, original_name, name_key, media_type, size_bytes, sha256, storage_key, state, revision, created_at, updated_at)
		VALUES (?, ?, 'file.txt', 'file.txt', 'text/plain', ?, ?, ?, 'ready', 1, ?, ?)`, id, owner, size,
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", storageKey, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Bun().Exec(`INSERT INTO files_capacity_counters(scope_kind, scope_id, reserved_bytes, reserved_objects) VALUES ('account', ?, ?, 1)
		ON CONFLICT(scope_kind, scope_id) DO UPDATE SET reserved_bytes = files_capacity_counters.reserved_bytes + excluded.reserved_bytes, reserved_objects = files_capacity_counters.reserved_objects + 1`, owner, size)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Bun().Exec(`UPDATE files_capacity_counters SET reserved_bytes = reserved_bytes + ?, reserved_objects = reserved_objects + 1 WHERE scope_kind = 'global' AND scope_id = 'global'`, size)
}

func seedLifecycleInbox(t *testing.T, db *database.Database, deletionID, source, target, strategy string) {
	t.Helper()
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	key := "account-deletion:" + deletionID + ":" + source + ":" + target
	_, err := db.Bun().Exec(`INSERT INTO files_account_lifecycle_events(event_id, business_key, payload, occurred_at) VALUES (?, ?, ?, ?)`,
		"event-"+deletionID, key, []byte(`{"strategy":"`+strategy+`","version":1}`), now)
	if err != nil {
		t.Fatal(err)
	}
}
