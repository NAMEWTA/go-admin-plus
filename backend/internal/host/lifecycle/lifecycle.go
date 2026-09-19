// Package lifecycle owns the host resource lifecycle state machine.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

var (
	ErrAlreadyStarted = errors.New("lifecycle manager has already started")
	ErrNotReady       = errors.New("lifecycle manager is not ready")
)

// State is the externally observable application lifecycle state.
type State string

const (
	StateStarting State = "starting"
	StateReady    State = "ready"
	StateDraining State = "draining"
	StateStopped  State = "stopped"
	StateFailed   State = "failed"
)

// Failure classifies a failed state without exposing an implementation error.
type Failure string

const (
	FailureNone       Failure = ""
	FailureStartup    Failure = "startup"
	FailureDependency Failure = "dependency"
)

// Lifecycle describes one owned runtime resource. Start order is declaration
// order; Drain and Stop run together in reverse declaration order.
type Lifecycle struct {
	Name  string
	Start func(context.Context) error
	Drain func(context.Context) error
	Stop  func(context.Context) error
}

// Snapshot is a point-in-time copy. Mutating Started cannot affect the manager.
type Snapshot struct {
	State   State
	Failure Failure
	Started []string
}

// Manager is single-start and safe for concurrent status reads and lifecycle
// requests. Lifecycle callbacks may read Snapshot but must not re-enter Start,
// Drain, or DependencyFailed.
type Manager struct {
	lifecycleMu sync.Mutex
	stateMu     sync.RWMutex
	state       State
	failure     Failure
	units       []Lifecycle
	started     []Lifecycle
}

// New validates and snapshots lifecycle definitions. The returned manager is in
// StateStarting and owns all further lifecycle transitions.
func New(units ...Lifecycle) (*Manager, error) {
	owned := append([]Lifecycle(nil), units...)
	seen := make(map[string]struct{}, len(owned))
	for index := range owned {
		owned[index].Name = strings.TrimSpace(owned[index].Name)
		if owned[index].Name == "" {
			return nil, fmt.Errorf("lifecycle at index %d has no name", index)
		}
		if _, exists := seen[owned[index].Name]; exists {
			return nil, fmt.Errorf("duplicate lifecycle %q", owned[index].Name)
		}
		seen[owned[index].Name] = struct{}{}
		if owned[index].Start == nil || owned[index].Drain == nil || owned[index].Stop == nil {
			return nil, fmt.Errorf("lifecycle %q requires start, drain, and stop functions", owned[index].Name)
		}
	}
	return &Manager{state: StateStarting, units: owned}, nil
}

// Snapshot returns an owned status copy suitable for observability handlers.
func (runtime *Manager) Snapshot() Snapshot {
	runtime.stateMu.RLock()
	defer runtime.stateMu.RUnlock()
	started := make([]string, 0, len(runtime.started))
	for _, unit := range runtime.started {
		started = append(started, unit.Name)
	}
	return Snapshot{State: runtime.state, Failure: runtime.failure, Started: started}
}

// Start starts every lifecycle in declaration order and marks the manager ready
// only after all starts succeed. A failure stops already-started lifecycles.
func (runtime *Manager) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("start context is required")
	}
	runtime.lifecycleMu.Lock()
	defer runtime.lifecycleMu.Unlock()
	if runtime.currentState() != StateStarting || len(runtime.started) != 0 {
		return ErrAlreadyStarted
	}
	for _, unit := range runtime.units {
		if err := ctx.Err(); err != nil {
			return runtime.failStart(ctx, unit.Name, err)
		}
		if err := unit.Start(ctx); err != nil {
			return runtime.failStart(ctx, unit.Name, err)
		}
		runtime.appendStarted(unit)
		if err := ctx.Err(); err != nil {
			return runtime.failStart(ctx, unit.Name, err)
		}
	}
	runtime.setStatus(StateReady, FailureNone)
	return nil
}

func (runtime *Manager) failStart(ctx context.Context, name string, cause error) error {
	runtime.setStatus(StateFailed, FailureStartup)
	cleanupErr := runtime.stopStarted(context.WithoutCancel(ctx), false)
	startErr := lifecycleError{operation: "start", name: name, cause: cause}
	return errors.Join(startErr, cleanupErr)
}

// Drain makes readiness false before draining and stopping resources in reverse
// order. Every resource receives Stop even when an earlier callback fails.
func (runtime *Manager) Drain(ctx context.Context) error {
	if ctx == nil {
		return errors.New("drain context is required")
	}
	runtime.lifecycleMu.Lock()
	defer runtime.lifecycleMu.Unlock()
	if runtime.currentState() != StateReady {
		return ErrNotReady
	}
	runtime.setStatus(StateDraining, FailureNone)
	err := runtime.stopStarted(ctx, true)
	runtime.setStatus(StateStopped, FailureNone)
	return err
}

// DependencyFailed makes readiness false before cleanup and retains StateFailed
// after resources stop. The triggering dependency detail is never stored.
func (runtime *Manager) DependencyFailed(ctx context.Context) error {
	if ctx == nil {
		return errors.New("dependency failure context is required")
	}
	runtime.lifecycleMu.Lock()
	defer runtime.lifecycleMu.Unlock()
	if runtime.currentState() != StateReady {
		return ErrNotReady
	}
	runtime.setStatus(StateFailed, FailureDependency)
	return runtime.stopStarted(ctx, true)
}

func (runtime *Manager) stopStarted(ctx context.Context, drain bool) error {
	runtime.stateMu.RLock()
	started := append([]Lifecycle(nil), runtime.started...)
	runtime.stateMu.RUnlock()
	var cleanupErrors []error
	for index := len(started) - 1; index >= 0; index-- {
		unit := started[index]
		if drain {
			if err := unit.Drain(ctx); err != nil {
				cleanupErrors = append(cleanupErrors, lifecycleError{operation: "drain", name: unit.Name, cause: err})
			}
		}
		if err := unit.Stop(ctx); err != nil {
			cleanupErrors = append(cleanupErrors, lifecycleError{operation: "stop", name: unit.Name, cause: err})
		}
	}
	runtime.stateMu.Lock()
	runtime.started = nil
	runtime.stateMu.Unlock()
	return errors.Join(cleanupErrors...)
}

func (runtime *Manager) currentState() State {
	runtime.stateMu.RLock()
	defer runtime.stateMu.RUnlock()
	return runtime.state
}

func (runtime *Manager) setStatus(state State, failure Failure) {
	runtime.stateMu.Lock()
	runtime.state = state
	runtime.failure = failure
	runtime.stateMu.Unlock()
}

func (runtime *Manager) appendStarted(unit Lifecycle) {
	runtime.stateMu.Lock()
	runtime.started = append(runtime.started, unit)
	runtime.stateMu.Unlock()
}

type lifecycleError struct {
	operation string
	name      string
	cause     error
}

func (failure lifecycleError) Error() string {
	return fmt.Sprintf("%s lifecycle %q: failed", failure.operation, failure.name)
}

func (failure lifecycleError) Unwrap() error { return failure.cause }
