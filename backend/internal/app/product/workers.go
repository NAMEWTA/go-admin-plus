package product

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/scheduler"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/coordination"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/outbox"
)

// workerGroup 分别监督后台循环。短暂故障只影响当前循环，备用进程会持续尝试接管。
type workerGroup struct {
	mu         sync.Mutex
	db         *database.Database
	owner      string
	interval   time.Duration
	executor   *scheduler.Executor
	dispatcher *outbox.Dispatcher
	lifecycle  *files.AccountLifecycle
	reconcile  func(context.Context) error
	lease      *coordination.Lease
	cancel     context.CancelFunc
	done       chan struct{}
	failures   map[string]error
	started    bool
}

func newWorkerGroup(db *database.Database, owner string, interval time.Duration, executor *scheduler.Executor, dispatcher *outbox.Dispatcher, lifecycle *files.AccountLifecycle, reconcile func(context.Context) error) *workerGroup {
	return &workerGroup{db: db, owner: owner, interval: interval, executor: executor, dispatcher: dispatcher, lifecycle: lifecycle, reconcile: reconcile, failures: map[string]error{}}
}

func (g *workerGroup) Start(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.started {
		return errors.New("product workers already started")
	}
	lease, err := coordination.Acquire(ctx, g.db, coordination.Config{Owner: g.owner})
	if err != nil && !errors.Is(err, coordination.ErrNotLeader) {
		return errors.New("product worker lease failed")
	}
	workerContext, cancel := context.WithCancel(ctx)
	g.lease, g.cancel, g.done, g.started = lease, cancel, make(chan struct{}), true
	go g.run(workerContext, g.done)
	return nil
}

func (g *workerGroup) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	var wg sync.WaitGroup
	loops := map[string]func(context.Context, *coordination.Lease) error{
		"scheduler": func(ctx context.Context, l *coordination.Lease) error {
			_, err := g.executor.RunAvailable(ctx)
			return err
		},
		"outbox": func(ctx context.Context, l *coordination.Lease) error {
			_, err := g.dispatcher.RunOnce(ctx, l, time.Now().UTC())
			return err
		},
		"account-lifecycle": func(ctx context.Context, _ *coordination.Lease) error {
			if g.lifecycle == nil {
				return nil
			}
			return g.lifecycle.RunOnce(ctx)
		},
		"files-recovery": func(ctx context.Context, _ *coordination.Lease) error {
			if g.reconcile == nil {
				return nil
			}
			return g.reconcile(ctx)
		},
	}
	for name, fn := range loops {
		wg.Add(1)
		go func() { defer wg.Done(); g.loop(ctx, name, fn) }()
	}
	defer wg.Wait()
	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()
	for {
		g.mu.Lock()
		missing := g.lease == nil
		g.mu.Unlock()
		if missing {
			lease, err := coordination.Acquire(ctx, g.db, coordination.Config{Owner: g.owner})
			g.mu.Lock()
			if err == nil {
				g.lease = lease
				delete(g.failures, "coordination")
			} else if !errors.Is(err, coordination.ErrNotLeader) {
				g.failures["coordination"] = errors.New("worker coordination unavailable")
			}
			g.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (g *workerGroup) loop(ctx context.Context, name string, fn func(context.Context, *coordination.Lease) error) {
	delay := g.interval
	for {
		g.mu.Lock()
		lease := g.lease
		g.mu.Unlock()
		if lease != nil || name != "outbox" {
			err := runWorkerStep(ctx, lease, fn)
			g.mu.Lock()
			if err == nil {
				delete(g.failures, name)
				delay = g.interval
			} else if ctx.Err() == nil {
				g.failures[name] = errors.New(name + " worker unavailable")
				delay = min(max(delay*2, time.Second), 30*time.Second)
				if errors.Is(err, coordination.ErrLeaseLost) && g.lease == lease {
					g.lease = nil
				}
			}
			g.mu.Unlock()
			if errors.Is(err, coordination.ErrLeaseLost) {
				_ = lease.Close(context.Background())
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func runWorkerStep(ctx context.Context, lease *coordination.Lease, fn func(context.Context, *coordination.Lease) error) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("worker panicked")
		}
	}()
	return fn(ctx, lease)
}

func (g *workerGroup) Stop(ctx context.Context) error {
	g.mu.Lock()
	if !g.started {
		g.mu.Unlock()
		return nil
	}
	cancel, done := g.cancel, g.done
	g.mu.Unlock()
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	g.mu.Lock()
	lease := g.lease
	g.lease = nil
	g.started = false
	g.mu.Unlock()
	if lease != nil {
		return lease.Close(ctx)
	}
	return nil
}
func (g *workerGroup) Check(context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	failures := make([]error, 0, len(g.failures))
	for _, err := range g.failures {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}
