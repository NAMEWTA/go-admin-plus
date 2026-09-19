package application

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

var (
	ErrMissingHandler = errors.New("application handler is required")
	ErrAlreadyStarted = errors.New("application has already been started")
	ErrNotRunning     = errors.New("application is not running")
)

type State string

const (
	StateConstructed State = "constructed"
	StateStarting    State = "starting"
	StateReady       State = "ready"
	StateStopping    State = "stopping"
	StateStopped     State = "stopped"
	StateFailed      State = "failed"
)

type Config struct {
	Name string
}

type Dependencies struct {
	Handler http.Handler
}

type Snapshot struct {
	State          State
	StartedModules []string
}

type Application struct {
	config  Config
	handler http.Handler
	modules []Module

	lifecycleMu sync.Mutex
	stateMu     sync.RWMutex
	state       State
	started     []Module
	cancel      context.CancelFunc
}

func Build(config Config, dependencies Dependencies, moduleSet ModuleSet) (*Application, error) {
	if dependencies.Handler == nil {
		return nil, ErrMissingHandler
	}

	modules := moduleSet.Modules()
	seen := make(map[string]struct{}, len(modules))
	for index, module := range modules {
		if module == nil {
			return nil, fmt.Errorf("module at index %d is nil", index)
		}
		id := strings.TrimSpace(module.ID())
		if id == "" {
			return nil, fmt.Errorf("module at index %d has an empty ID", index)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("duplicate module ID %q", id)
		}
		seen[id] = struct{}{}
		if err := registerRoutes(module, dependencies.Handler); err != nil {
			return nil, err
		}
	}

	return &Application{
		config:  config,
		handler: dependencies.Handler,
		modules: modules,
		state:   StateConstructed,
	}, nil
}

func registerRoutes(module Module, handler http.Handler) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("register routes for module %q: %v", module.ID(), recovered)
		}
	}()
	if err := module.RegisterRoutes(handler); err != nil {
		return fmt.Errorf("register routes for module %q: %w", module.ID(), err)
	}
	return nil
}

func (a *Application) Handler() http.Handler {
	return a.handler
}

func (a *Application) Config() Config {
	return a.config
}

func (a *Application) Migrations() []Migration {
	var migrations []Migration
	for _, module := range a.modules {
		migrations = append(migrations, module.Migrations()...)
	}
	return migrations
}

func (a *Application) State() Snapshot {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()

	started := make([]string, 0, len(a.started))
	for _, module := range a.started {
		started = append(started, module.ID())
	}
	return Snapshot{State: a.state, StartedModules: started}
}

func (a *Application) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("start context is required")
	}

	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()

	if a.currentState() != StateConstructed {
		return ErrAlreadyStarted
	}
	a.setState(StateStarting)

	runCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	for _, module := range a.modules {
		if err := runCtx.Err(); err != nil {
			return a.failStart(ctx, module.ID(), err)
		}
		if err := module.Start(runCtx); err != nil {
			return a.failStart(ctx, module.ID(), err)
		}
		a.appendStarted(module)
	}
	if err := runCtx.Err(); err != nil {
		return a.failStart(ctx, "application", err)
	}

	a.setState(StateReady)
	return nil
}

func (a *Application) failStart(startCtx context.Context, moduleID string, startErr error) error {
	if a.cancel != nil {
		a.cancel()
	}
	cleanupErr := a.stopStarted(context.WithoutCancel(startCtx))
	a.setState(StateFailed)
	if cleanupErr != nil {
		return errors.Join(fmt.Errorf("start module %q: %w", moduleID, startErr), cleanupErr)
	}
	return fmt.Errorf("start module %q: %w", moduleID, startErr)
}

func (a *Application) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("stop context is required")
	}

	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()

	if a.currentState() != StateReady {
		return ErrNotRunning
	}
	a.setState(StateStopping)
	if a.cancel != nil {
		a.cancel()
	}
	err := a.stopStarted(ctx)
	a.setState(StateStopped)
	return err
}

func (a *Application) stopStarted(ctx context.Context) error {
	a.stateMu.RLock()
	started := append([]Module(nil), a.started...)
	a.stateMu.RUnlock()

	var stopErrors []error
	for index := len(started) - 1; index >= 0; index-- {
		module := started[index]
		if err := module.Stop(ctx); err != nil {
			stopErrors = append(stopErrors, fmt.Errorf("stop module %q: %w", module.ID(), err))
		}
	}
	a.stateMu.Lock()
	a.started = nil
	a.stateMu.Unlock()
	return errors.Join(stopErrors...)
}

func (a *Application) currentState() State {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.state
}

func (a *Application) setState(state State) {
	a.stateMu.Lock()
	a.state = state
	a.stateMu.Unlock()
}

func (a *Application) appendStarted(module Module) {
	a.stateMu.Lock()
	a.started = append(a.started, module)
	a.stateMu.Unlock()
}
