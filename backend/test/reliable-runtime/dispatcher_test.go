package reliableruntime_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/coordination"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/outbox"
)

func TestDispatcherProductionEntryRequiresConcreteCoordinationLease(t *testing.T) {
	t.Parallel()
	method := reflect.TypeOf((*outbox.Dispatcher).RunOnce)
	if method.In(2) != reflect.TypeFor[*coordination.Lease]() {
		t.Fatalf("RunOnce executor parameter = %v", method.In(2))
	}
}

func TestDispatcherTreatsConnectionProbeFailureAsImmediateLeaseLoss(t *testing.T) {
	t.Parallel()
	db := openReliableSQLite(t)
	store := newReliableStore(t, db)
	dispatcher, err := outbox.NewDispatcher(store, outbox.DispatcherConfig{
		Owner: "worker-a", LeaseDuration: time.Minute, RetryDelay: time.Minute, BatchSize: 10,
	}, nil)
	if err != nil {
		t.Fatalf("NewDispatcher() error = %v", err)
	}
	lease := acquireSQLiteExecutor(t, db, "worker-a")
	if err := db.Close(); err != nil {
		t.Fatalf("database Close() error = %v", err)
	}
	waits := 0
	var observations []outbox.LoopObservation
	err = dispatcher.Run(context.Background(), lease, outbox.LoopOptions{
		PollInterval: time.Second, FailureBackoff: 30 * time.Second,
		Now: func() time.Time { return time.Date(2026, 8, 26, 16, 0, 0, 0, time.UTC) },
		Wait: func(_ context.Context, delay time.Duration) error {
			waits++
			return nil
		},
		Observer: outbox.ObserveFunc(func(observation outbox.LoopObservation) {
			observations = append(observations, observation)
		}),
	})
	if !errors.Is(err, coordination.ErrLeaseLost) || waits != 0 {
		t.Fatalf("Run() = err %v, waits %v", err, waits)
	}
	if len(observations) != 1 || observations[0].Outcome != outbox.LoopLeaseLost ||
		!observations[0].LostLock || observations[0].ActiveExecutor {
		t.Fatalf("observations = %#v", observations)
	}
}

func TestDispatcherStopsImmediatelyWhenExecutorLeaseIsLost(t *testing.T) {
	t.Parallel()
	db := openReliableSQLite(t)
	store := newReliableStore(t, db)
	dispatcher, err := outbox.NewDispatcher(store, outbox.DispatcherConfig{
		Owner: "worker-a", LeaseDuration: time.Minute, RetryDelay: time.Minute, BatchSize: 10,
	}, nil)
	if err != nil {
		t.Fatalf("NewDispatcher() error = %v", err)
	}
	lease := acquireSQLiteExecutor(t, db, "worker-a")
	if err := lease.Close(context.Background()); err != nil {
		t.Fatalf("lease Close() error = %v", err)
	}
	waits := 0
	var observations []outbox.LoopObservation
	err = dispatcher.Run(context.Background(), lease, outbox.LoopOptions{
		PollInterval: time.Second, FailureBackoff: 30 * time.Second,
		Now: func() time.Time { return time.Date(2026, 8, 26, 16, 30, 0, 0, time.UTC) },
		Wait: func(context.Context, time.Duration) error {
			waits++
			return nil
		},
		Observer: outbox.ObserveFunc(func(observation outbox.LoopObservation) {
			observations = append(observations, observation)
		}),
	})
	if !errors.Is(err, coordination.ErrLeaseLost) || waits != 0 {
		t.Fatalf("Run() = err %v, waits %d", err, waits)
	}
	if len(observations) != 1 || observations[0].Outcome != outbox.LoopLeaseLost || observations[0].Delay != 0 {
		t.Fatalf("lease observations = %#v", observations)
	}
}

func TestDispatcherRetriesConsumerFailureWithoutLeakingDiagnostic(t *testing.T) {
	t.Parallel()
	db := openReliableSQLite(t)
	createEffectsTable(t, db)
	store := newReliableStore(t, db)
	now := time.Date(2026, 8, 26, 17, 0, 0, 0, time.UTC)
	event := reliableEvent("event-dispatch-1", "order:dispatch", now)
	enqueueEvent(t, db, store, event)
	clockCalls := 0
	consumer := newEffectConsumer(t, true)
	dispatcher, err := outbox.NewDispatcher(store, outbox.DispatcherConfig{
		Owner: "worker-a", LeaseDuration: time.Minute, RetryDelay: 5 * time.Minute, BatchSize: 10,
		Now: func() time.Time {
			clockCalls++
			if clockCalls == 1 {
				return now.Add(10 * time.Second)
			}
			return now.Add(45 * time.Second)
		},
	}, map[string]outbox.TransactionalConsumer{event.Topic: consumer})
	if err != nil {
		t.Fatalf("NewDispatcher() error = %v", err)
	}
	lease := acquireSQLiteExecutor(t, db, "worker-a")
	result, err := dispatcher.RunOnce(context.Background(), lease, now)
	if err != nil || result.Claimed != 1 || result.Retried != 1 || result.Delivered != 0 {
		t.Fatalf("RunOnce() = %#v, %v", result, err)
	}
	record, found, err := store.Lookup(context.Background(), event.ID)
	if err != nil || !found || record.State != outbox.StateRetry || record.LastErrorCode != "consumer_failed" {
		t.Fatalf("retry record = %#v, found %v, err %v", record, found, err)
	}
	if want := now.Add(45*time.Second + 5*time.Minute); !record.AvailableAt.Equal(want) {
		t.Fatalf("retry available_at = %v, want failure time based %v", record.AvailableAt, want)
	}
	assertEffectCalls(t, db, event.BusinessKey, 0)
}

func TestDispatcherObserverReportsDispatchAndRetryOutcomes(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		failure bool
		want    outbox.LoopOutcome
	}{
		{name: "delivered", want: outbox.LoopDispatched},
		{name: "retried", failure: true, want: outbox.LoopRetried},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openReliableSQLite(t)
			createEffectsTable(t, db)
			store := newReliableStore(t, db)
			now := time.Date(2026, 8, 26, 17, 15, 0, 0, time.UTC)
			event := reliableEvent("event-observer-"+testCase.name, "order:observer:"+testCase.name, now)
			enqueueEvent(t, db, store, event)
			consumer := newEffectConsumer(t, testCase.failure)
			measureCalls := 0
			dispatcher, err := outbox.NewDispatcher(store, outbox.DispatcherConfig{
				Owner: "worker-a", LeaseDuration: time.Minute, RetryDelay: time.Minute, BatchSize: 1,
				Now: func() time.Time { return now.Add(10 * time.Second) },
				Measure: func() time.Time {
					measureCalls++
					return now.Add(time.Duration(measureCalls-1) * 15 * time.Millisecond)
				},
			}, map[string]outbox.TransactionalConsumer{event.Topic: consumer})
			if err != nil {
				t.Fatalf("NewDispatcher() error = %v", err)
			}
			lease := acquireSQLiteExecutor(t, db, "worker-a")
			stop := errors.New("stop")
			var observations []outbox.LoopObservation
			err = dispatcher.Run(context.Background(), lease, outbox.LoopOptions{
				PollInterval: time.Second, FailureBackoff: time.Minute, Now: func() time.Time { return now.Add(20 * time.Second) },
				Wait: func(context.Context, time.Duration) error { return stop },
				Observer: outbox.ObserveFunc(func(observation outbox.LoopObservation) {
					observations = append(observations, observation)
				}),
			})
			if !errors.Is(err, stop) || len(observations) != 1 || observations[0].Outcome != testCase.want ||
				!observations[0].ActiveExecutor || observations[0].Attempts != 1 || observations[0].PendingAge != 20*time.Second ||
				observations[0].ClaimDuration != 15*time.Millisecond ||
				observations[0].Retries != observations[0].Result.Retried {
				t.Fatalf("Run() error = %v, observations = %#v", err, observations)
			}
		})
	}
}

func TestConsumerCompletingAfterClaimExpiryCannotCommit(t *testing.T) {
	t.Parallel()
	db := openReliableSQLite(t)
	createEffectsTable(t, db)
	store := newReliableStore(t, db)
	now := time.Date(2026, 8, 26, 17, 25, 0, 0, time.UTC)
	clockCalls := 0
	event := reliableEvent("event-slow-consumer-1", "order:slow-consumer", now)
	enqueueEvent(t, db, store, event)
	consumer := newEffectConsumer(t, false)
	dispatcher, err := outbox.NewDispatcher(store, outbox.DispatcherConfig{
		Owner: "worker-a", LeaseDuration: time.Minute, RetryDelay: time.Minute, BatchSize: 1,
		Now: func() time.Time {
			clockCalls++
			if clockCalls == 1 {
				return now.Add(10 * time.Second)
			}
			return now.Add(2 * time.Minute)
		},
	}, map[string]outbox.TransactionalConsumer{event.Topic: consumer})
	if err != nil {
		t.Fatalf("NewDispatcher() error = %v", err)
	}
	lease := acquireSQLiteExecutor(t, db, "worker-a")
	result, err := dispatcher.RunOnce(context.Background(), lease, now)
	if !errors.Is(err, outbox.ErrClaimLost) || result.Claimed != 1 {
		t.Fatalf("RunOnce() = %#v, error %v", result, err)
	}
	assertEffectCalls(t, db, event.BusinessKey, 0)
}

func acquireSQLiteExecutor(t *testing.T, db *database.Database, owner string) *coordination.Lease {
	t.Helper()
	lease, err := coordination.Acquire(context.Background(), db, coordination.Config{Owner: owner})
	if err != nil {
		t.Fatalf("coordination Acquire() error = %v", err)
	}
	t.Cleanup(func() { _ = lease.Close(context.Background()) })
	return lease
}
