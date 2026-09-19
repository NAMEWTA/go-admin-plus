package reliableruntime_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/coordination"
)

func TestCoordinationConfigCannotSelectAnotherAdvisoryKey(t *testing.T) {
	t.Parallel()
	if _, configurable := reflect.TypeFor[coordination.Config]().FieldByName("AdvisoryKey"); configurable {
		t.Fatal("coordination Config exposes an advisory key bypass")
	}
}

func TestSQLiteAllowsOneExecutorAndTakeoverAfterRelease(t *testing.T) {
	t.Parallel()
	db := openReliableSQLite(t)
	first, err := coordination.Acquire(context.Background(), db, coordination.Config{Owner: "worker-a"})
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	if _, err := coordination.Acquire(context.Background(), db, coordination.Config{Owner: "worker-b"}); !errors.Is(err, coordination.ErrNotLeader) {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	second, err := coordination.Acquire(context.Background(), db, coordination.Config{Owner: "worker-b"})
	if err != nil {
		t.Fatalf("takeover Acquire() error = %v", err)
	}
	if err := second.Close(context.Background()); err != nil {
		t.Fatalf("takeover Close() error = %v", err)
	}
}

func TestSQLiteConcurrentAcquireHasOneWinner(t *testing.T) {
	t.Parallel()
	db := openReliableSQLite(t)
	start := make(chan struct{})
	winners := make(chan *coordination.Lease, 16)
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Add(1)
		go func(owner string) {
			defer workers.Done()
			<-start
			lease, err := coordination.Acquire(context.Background(), db, coordination.Config{Owner: owner})
			if err == nil {
				winners <- lease
			}
		}(fmt.Sprintf("worker-%d", worker))
	}
	close(start)
	workers.Wait()
	close(winners)
	if len(winners) != 1 {
		t.Fatalf("executor winners = %d, want 1", len(winners))
	}
	for lease := range winners {
		if err := lease.Close(context.Background()); err != nil {
			t.Fatalf("winner Close() error = %v", err)
		}
	}
}
