package lifecycle_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/host/lifecycle"
)

func TestManagerStartsThenDrainsInDeterministicOrder(t *testing.T) {
	var events []string
	var runtime *lifecycle.Manager
	unit := func(name string) lifecycle.Lifecycle {
		return lifecycle.Lifecycle{
			Name: name,
			Start: func(context.Context) error {
				events = append(events, "start:"+name)
				return nil
			},
			Drain: func(context.Context) error {
				if runtime.Snapshot().State != lifecycle.StateDraining {
					t.Fatalf("state during drain = %q", runtime.Snapshot().State)
				}
				events = append(events, "drain:"+name)
				return nil
			},
			Stop: func(context.Context) error {
				events = append(events, "stop:"+name)
				return nil
			},
		}
	}
	var err error
	runtime, err = lifecycle.New(unit("database"), unit("http"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if state := runtime.Snapshot().State; state != lifecycle.StateStarting {
		t.Fatalf("initial state = %q", state)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if state := runtime.Snapshot().State; state != lifecycle.StateReady {
		t.Fatalf("state after Start() = %q", state)
	}
	if err := runtime.Drain(context.Background()); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if state := runtime.Snapshot().State; state != lifecycle.StateStopped {
		t.Fatalf("state after Drain() = %q", state)
	}
	want := []string{"start:database", "start:http", "drain:http", "stop:http", "drain:database", "stop:database"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestDependencyFailureIsNotReadyAndCleansEveryLifecycle(t *testing.T) {
	var events []string
	var runtime *lifecycle.Manager
	unit := func(name string, drainErr error) lifecycle.Lifecycle {
		return lifecycle.Lifecycle{
			Name:  name,
			Start: func(context.Context) error { return nil },
			Drain: func(context.Context) error {
				if snapshot := runtime.Snapshot(); snapshot.State != lifecycle.StateFailed || snapshot.Failure != lifecycle.FailureDependency {
					t.Fatalf("snapshot during dependency cleanup = %#v", snapshot)
				}
				events = append(events, "drain:"+name)
				return drainErr
			},
			Stop: func(context.Context) error {
				events = append(events, "stop:"+name)
				return nil
			},
		}
	}
	secretFailure := errors.New("database password=do-not-expose")
	var err error
	runtime, err = lifecycle.New(unit("database", nil), unit("worker", secretFailure))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	err = runtime.DependencyFailed(context.Background())
	if !errors.Is(err, secretFailure) {
		t.Fatalf("DependencyFailed() error = %v", err)
	}
	if strings.Contains(err.Error(), "do-not-expose") || strings.Contains(err.Error(), "password") {
		t.Fatalf("DependencyFailed() leaked cleanup detail: %v", err)
	}
	snapshot := runtime.Snapshot()
	if snapshot.State != lifecycle.StateFailed || snapshot.Failure != lifecycle.FailureDependency || len(snapshot.Started) != 0 {
		t.Fatalf("Snapshot() = %#v", snapshot)
	}
	if strings.Contains(strings.Join(snapshot.Started, ","), "do-not-expose") {
		t.Fatal("Snapshot() leaked dependency failure detail")
	}
	want := []string{"drain:worker", "stop:worker", "drain:database", "stop:database"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestStartFailureStopsPartialRuntimeAndNeverBecomesReady(t *testing.T) {
	var events []string
	secretFailure := errors.New("open /private/data: password=do-not-log")
	first := lifecycle.Lifecycle{
		Name:  "database",
		Start: func(context.Context) error { events = append(events, "start:database"); return nil },
		Drain: func(context.Context) error { events = append(events, "drain:database"); return nil },
		Stop:  func(context.Context) error { events = append(events, "stop:database"); return nil },
	}
	second := lifecycle.Lifecycle{
		Name:  "worker",
		Start: func(context.Context) error { events = append(events, "start:worker"); return secretFailure },
		Drain: func(context.Context) error { events = append(events, "drain:worker"); return nil },
		Stop:  func(context.Context) error { events = append(events, "stop:worker"); return nil },
	}
	runtime, err := lifecycle.New(first, second)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = runtime.Start(context.Background())
	if !errors.Is(err, secretFailure) {
		t.Fatalf("Start() error = %v", err)
	}
	if strings.Contains(err.Error(), "do-not-log") || strings.Contains(err.Error(), "/private/data") {
		t.Fatalf("Start() leaked dependency detail: %v", err)
	}
	snapshot := runtime.Snapshot()
	if snapshot.State != lifecycle.StateFailed || snapshot.Failure != lifecycle.FailureStartup || len(snapshot.Started) != 0 {
		t.Fatalf("Snapshot() = %#v", snapshot)
	}
	want := []string{"start:database", "start:worker", "stop:database"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestConcurrentLifecycleRequestsHaveOneTerminalTransition(t *testing.T) {
	startEntered := make(chan struct{})
	releaseStart := make(chan struct{})
	var starts, drains, stops atomic.Int32
	runtime, err := lifecycle.New(lifecycle.Lifecycle{
		Name: "runtime",
		Start: func(context.Context) error {
			starts.Add(1)
			close(startEntered)
			<-releaseStart
			return nil
		},
		Drain: func(context.Context) error {
			drains.Add(1)
			return nil
		},
		Stop: func(context.Context) error {
			stops.Add(1)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	startResult := make(chan error, 1)
	go func() { startResult <- runtime.Start(context.Background()) }()
	waitSignal(t, startEntered, "initial Start callback")

	const readers = 8
	readerGate := make(chan struct{})
	var readersReady sync.WaitGroup
	readersReady.Add(readers)
	var readersDone sync.WaitGroup
	readersDone.Add(readers)
	for range readers {
		go func() {
			defer readersDone.Done()
			readersReady.Done()
			<-readerGate
			for range 100 {
				snapshot := runtime.Snapshot()
				switch snapshot.State {
				case lifecycle.StateStarting, lifecycle.StateReady, lifecycle.StateDraining, lifecycle.StateStopped, lifecycle.StateFailed:
				default:
					t.Errorf("Snapshot state = %q", snapshot.State)
					return
				}
			}
		}()
	}
	readersReady.Wait()
	close(readerGate)

	operationGate := make(chan struct{})
	drainResult := make(chan error, 1)
	dependencyResult := make(chan error, 1)
	repeatStartResult := make(chan error, 1)
	var operationsReady sync.WaitGroup
	operationsReady.Add(3)
	go func() { operationsReady.Done(); <-operationGate; drainResult <- runtime.Drain(context.Background()) }()
	go func() {
		operationsReady.Done()
		<-operationGate
		dependencyResult <- runtime.DependencyFailed(context.Background())
	}()
	go func() {
		operationsReady.Done()
		<-operationGate
		repeatStartResult <- runtime.Start(context.Background())
	}()
	operationsReady.Wait()
	close(operationGate)
	close(releaseStart)

	if err := waitResult(t, startResult, "initial Start"); err != nil {
		t.Fatalf("initial Start error = %v", err)
	}
	drainErr := waitResult(t, drainResult, "concurrent Drain")
	dependencyErr := waitResult(t, dependencyResult, "concurrent DependencyFailed")
	repeatErr := waitResult(t, repeatStartResult, "repeated Start")
	readersDone.Wait()

	if !errors.Is(repeatErr, lifecycle.ErrAlreadyStarted) {
		t.Fatalf("repeated Start error = %v", repeatErr)
	}
	oneSucceeded := (drainErr == nil && errors.Is(dependencyErr, lifecycle.ErrNotReady)) ||
		(dependencyErr == nil && errors.Is(drainErr, lifecycle.ErrNotReady))
	if !oneSucceeded {
		t.Fatalf("Drain error = %v, DependencyFailed error = %v", drainErr, dependencyErr)
	}
	if starts.Load() != 1 || drains.Load() != 1 || stops.Load() != 1 {
		t.Fatalf("callback counts start=%d drain=%d stop=%d", starts.Load(), drains.Load(), stops.Load())
	}
	snapshot := runtime.Snapshot()
	if snapshot.State != lifecycle.StateStopped && snapshot.State != lifecycle.StateFailed {
		t.Fatalf("terminal Snapshot() = %#v", snapshot)
	}
	if snapshot.State == lifecycle.StateStopped && snapshot.Failure != lifecycle.FailureNone {
		t.Fatalf("stopped failure = %q", snapshot.Failure)
	}
	if snapshot.State == lifecycle.StateFailed && snapshot.Failure != lifecycle.FailureDependency {
		t.Fatalf("failed reason = %q", snapshot.Failure)
	}
	if len(snapshot.Started) != 0 {
		t.Fatalf("terminal started lifecycles = %v", snapshot.Started)
	}
}

func TestStartCancellationCleansStartedLifecycleAndJoinsCleanupError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cleanupFailure := errors.New("cleanup failed with private detail")
	var stops atomic.Int32
	runtime, err := lifecycle.New(lifecycle.Lifecycle{
		Name: "database",
		Start: func(context.Context) error {
			cancel()
			return nil
		},
		Drain: func(context.Context) error { return nil },
		Stop: func(context.Context) error {
			stops.Add(1)
			return cleanupFailure
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = runtime.Start(ctx)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, cleanupFailure) {
		t.Fatalf("Start() error = %v, want cancellation and cleanup failure", err)
	}
	if strings.Contains(err.Error(), "private detail") {
		t.Fatalf("Start() leaked cleanup error: %v", err)
	}
	if stops.Load() != 1 {
		t.Fatalf("Stop calls = %d, want 1", stops.Load())
	}
	snapshot := runtime.Snapshot()
	if snapshot.State != lifecycle.StateFailed || snapshot.Failure != lifecycle.FailureStartup || len(snapshot.Started) != 0 {
		t.Fatalf("Snapshot() = %#v", snapshot)
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}

func waitResult(t *testing.T, result <-chan error, operation string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
		return nil
	}
}
