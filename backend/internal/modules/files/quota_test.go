package files

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	platformdb "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

type capacityProbeStub struct {
	capacity Capacity
	err      error
	calls    int
}

func (probe *capacityProbeStub) Capacity(context.Context) (Capacity, error) {
	probe.calls++
	return probe.capacity, probe.err
}

func capacityPolicy() CapacityPolicy {
	return CapacityPolicy{
		MaximumObjectBytes:       8,
		MaximumAccountBytes:      8,
		MaximumAccountObjects:    2,
		MaximumTotalBytes:        16,
		MaximumTotalObjects:      4,
		MinimumAvailableBytes:    2,
		MinimumAvailableFraction: 0.1,
		ReservationTTL:           time.Minute,
		ReconcileBatchSize:       2,
	}
}

func TestDiskCapacityProbeUsesStorageRoot(t *testing.T) {
	root := canonicalTestRoot(t, "disk-capacity")
	storage, err := NewLocalStorage(root)
	if err != nil {
		t.Fatalf("create storage root: %v", err)
	}
	defer storage.Close()
	probe := NewDiskCapacityProbe(root)
	capacity, err := probe.Capacity(context.Background())
	if err != nil {
		t.Fatalf("disk capacity = %v", err)
	}
	if capacity.TotalBytes <= 0 || capacity.AvailableBytes <= 0 || capacity.AvailableBytes > capacity.TotalBytes {
		t.Fatalf("invalid disk capacity: %#v", capacity)
	}
	if _, err := NewDiskCapacityProbe(t.TempDir() + "-missing").Capacity(context.Background()); !errors.Is(err, ErrDiskCapacity) {
		t.Fatalf("missing storage root error = %v", err)
	}
}

func newCapacityService(t *testing.T, policy CapacityPolicy, probe CapacityProbe) (*Service, *LocalStorage) {
	t.Helper()
	storage, err := NewLocalStorage(canonicalTestRoot(t, "capacity"), WithMaximumContentBytes(policy.MaximumObjectBytes))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	service, err := NewService(filesDatabase(t), storage, &authorizerStub{scope: ScopeAll}, WithCapacityPolicy(policy), WithCapacityProbe(probe))
	if err != nil {
		t.Fatal(err)
	}
	return service, storage
}

func TestUploadRejectsLowDiskBeforeReadingOrReserving(t *testing.T) {
	probe := &capacityProbeStub{capacity: Capacity{AvailableBytes: 6, TotalBytes: 100}}
	service, _ := newCapacityService(t, capacityPolicy(), probe)
	reader := &countingReader{}
	_, err := service.Upload(context.Background(), "account-a", UploadInput{
		OriginalName: "low-disk.txt", DeclaredMediaType: "text/plain", DeclaredSizeBytes: 5, Content: reader,
	})
	if !errors.Is(err, ErrDiskCapacity) || reader.reads != 0 || probe.calls != 1 {
		t.Fatalf("low disk err=%v reads=%d probe=%d", err, reader.reads, probe.calls)
	}
	assertCapacityUsage(t, service.db, "account-a", 0, 0)
}

func TestUnauthorizedUploadDoesNotProbeCapacityOrReadBody(t *testing.T) {
	policy := capacityPolicy()
	probe := &capacityProbeStub{capacity: Capacity{AvailableBytes: 1_000, TotalBytes: 2_000}}
	storage, err := NewLocalStorage(canonicalTestRoot(t, "unauthorized-capacity"), WithMaximumContentBytes(policy.MaximumObjectBytes))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	service, err := NewService(filesDatabase(t), storage, &authorizerStub{err: ErrDenied}, WithCapacityPolicy(policy), WithCapacityProbe(probe))
	if err != nil {
		t.Fatal(err)
	}
	reader := &countingReader{}
	_, err = service.Upload(context.Background(), "account-a", UploadInput{
		OriginalName: "denied.txt", DeclaredMediaType: "text/plain", DeclaredSizeBytes: 5, Content: reader,
	})
	if !errors.Is(err, ErrDenied) || reader.reads != 0 || probe.calls != 0 {
		t.Fatalf("denied err=%v reads=%d probe=%d", err, reader.reads, probe.calls)
	}
}

func TestConcurrentUploadsCannotOversellAndDeleteReleasesQuota(t *testing.T) {
	probe := &capacityProbeStub{capacity: Capacity{AvailableBytes: 1_000, TotalBytes: 2_000}}
	service, _ := newCapacityService(t, capacityPolicy(), probe)
	start := make(chan struct{})
	results := make(chan struct {
		metadata Metadata
		err      error
	}, 2)
	var wait sync.WaitGroup
	for index := range 2 {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			value, err := service.Upload(context.Background(), "account-a", UploadInput{
				OriginalName: "quota-" + string(rune('a'+index)) + ".txt", DeclaredMediaType: "text/plain", DeclaredSizeBytes: 5, Content: strings.NewReader("12345"),
			})
			results <- struct {
				metadata Metadata
				err      error
			}{value, err}
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	var created Metadata
	succeeded, rejected := 0, 0
	for result := range results {
		switch {
		case result.err == nil:
			succeeded++
			created = result.metadata
		case errors.Is(result.err, ErrQuotaExceeded):
			rejected++
		default:
			t.Fatalf("unexpected upload error: %v", result.err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("succeeded=%d rejected=%d", succeeded, rejected)
	}
	assertCapacityUsage(t, service.db, "account-a", 5, 1)
	if err := service.Delete(context.Background(), "account-a", []DeleteTarget{{ID: created.ID, Revision: created.Revision}}); err != nil {
		t.Fatal(err)
	}
	assertCapacityUsage(t, service.db, "account-a", 0, 0)
	if _, err := service.Upload(context.Background(), "account-a", UploadInput{
		OriginalName: "after-delete.txt", DeclaredMediaType: "text/plain", DeclaredSizeBytes: 5, Content: strings.NewReader("12345"),
	}); err != nil {
		t.Fatalf("quota was not released: %v", err)
	}
}

func TestDeclaredSizeMismatchReleasesReservation(t *testing.T) {
	service, _ := newCapacityService(t, capacityPolicy(), &capacityProbeStub{capacity: Capacity{AvailableBytes: 1_000, TotalBytes: 2_000}})
	_, err := service.Upload(context.Background(), "account-a", UploadInput{
		OriginalName: "drift.txt", DeclaredMediaType: "text/plain", DeclaredSizeBytes: 6, Content: strings.NewReader("12345"),
	})
	if !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("size drift error=%v", err)
	}
	assertCapacityUsage(t, service.db, "account-a", 0, 0)
}

func TestReconcileReleasesOnlyOneBoundedBatchOfExpiredReservations(t *testing.T) {
	policy := capacityPolicy()
	policy.MaximumAccountObjects = 4
	service, _ := newCapacityService(t, policy, &capacityProbeStub{capacity: Capacity{AvailableBytes: 1_000, TotalBytes: 2_000}})
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	for index := range 3 {
		id := "00000000-0000-0000-0000-00000000000" + string(rune('1'+index))
		if err := service.db.WithinTx(context.Background(), func(ctx context.Context, tx platformdb.Tx) error {
			return service.repository.reserve(ctx, tx, id, "account-a", 1, now.Add(-2*time.Minute), now.Add(-time.Minute), policy)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCapacityUsage(t, service.db, "account-a", 1, 1)
}

func assertCapacityUsage(t *testing.T, db Database, owner string, bytes, objects int64) {
	t.Helper()
	var actualBytes, actualObjects int64
	err := db.WithinTx(context.Background(), func(ctx context.Context, tx platformdb.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT reserved_bytes, reserved_objects FROM files_capacity_counters WHERE scope_kind = ? AND scope_id = ?`, "account", owner).Scan(&actualBytes, &actualObjects)
	})
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil || actualBytes != bytes || actualObjects != objects {
		t.Fatalf("usage bytes=%d objects=%d err=%v want=%d/%d", actualBytes, actualObjects, err, bytes, objects)
	}
}
