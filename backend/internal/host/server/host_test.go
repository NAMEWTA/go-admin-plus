package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/application"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/application/health"
)

func TestRequestLoggerEmitsOperationalFieldsWithoutQueryOrHeaders(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	handler := requestLogger(logger, "postgres", http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusAccepted)
	}))
	request := httptest.NewRequest(http.MethodPost, "/api/files/objects?token=private-query", nil)
	request.Header.Set("Authorization", "Bearer private-header")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	logged := output.String()
	for _, required := range []string{`"route":"/api/files/objects"`, `"status":202`, `"database":"postgres"`, `"latency_ms"`} {
		if !strings.Contains(logged, required) {
			t.Fatalf("log missing %s: %s", required, logged)
		}
	}
	if strings.Contains(logged, "private-query") || strings.Contains(logged, "private-header") {
		t.Fatalf("request log leaked sensitive request material: %s", logged)
	}
}

func TestHostServesOperationsAndStopsEveryOwnedResource(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	var dependencyDown atomic.Bool
	app := newFakeApplication()
	var closeCalls atomic.Int32
	config := testConfig(listener.Addr().String())
	config.ShutdownTimeout = time.Second
	host, err := New(config, func(context.Context) (Runtime, error) {
		return Runtime{
			Application: app,
			Readiness: []health.Checker{{Name: "database", Check: func(context.Context) error {
				if dependencyDown.Load() {
					return errors.New("database unavailable")
				}
				return nil
			}}},
			Close: func(context.Context) error {
				closeCalls.Add(1)
				return nil
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	host.listen = func(context.Context, string) (net.Listener, error) { return listener, nil }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Run(ctx) }()

	select {
	case <-host.Started():
	case <-time.After(3 * time.Second):
		t.Fatal("host did not report start")
	}
	baseURL := "http://" + listener.Addr().String()
	assertHTTPStatus(t, baseURL+"/health/live", http.StatusOK)
	assertHTTPStatus(t, baseURL+"/health/ready", http.StatusOK)
	assertHTTPStatus(t, baseURL+"/api/v1/runtime/capabilities", http.StatusOK)
	assertHTTPStatus(t, baseURL+"/api/v1/runtime/status", http.StatusOK)
	assertHTTPStatus(t, baseURL+"/metrics", http.StatusOK)
	assertHTTPStatus(t, baseURL+"/business", http.StatusNoContent)

	dependencyDown.Store(true)
	assertHTTPStatus(t, baseURL+"/health/live", http.StatusOK)
	assertHTTPStatus(t, baseURL+"/health/ready", http.StatusServiceUnavailable)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("host did not stop")
	}
	if !app.started.Load() || !app.stopped.Load() {
		t.Fatalf("application lifecycle started=%v stopped=%v", app.started.Load(), app.stopped.Load())
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("runtime close calls = %d, want 1", closeCalls.Load())
	}
}

func TestHostReturnsBindFailureWithoutStartingApplication(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer occupied.Close()

	app := newFakeApplication()
	var closeCalls atomic.Int32
	host, err := New(testConfig(occupied.Addr().String()), func(context.Context) (Runtime, error) {
		return Runtime{
			Application: app,
			Close: func(context.Context) error {
				closeCalls.Add(1)
				return nil
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = host.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("Run error = %v, want listen failure", err)
	}
	if app.started.Load() {
		t.Fatal("application started after bind failure")
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("runtime close calls = %d, want 1", closeCalls.Load())
	}
}

func TestHostClosesRuntimeWhenBuildOrStartFails(t *testing.T) {
	wantBuildErr := errors.New("dependency failed")
	host, err := New(testConfig("127.0.0.1:0"), func(context.Context) (Runtime, error) {
		return Runtime{}, wantBuildErr
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := host.Run(context.Background()); !errors.Is(err, wantBuildErr) {
		t.Fatalf("build failure = %v, want %v", err, wantBuildErr)
	}

	app := newFakeApplication()
	app.startErr = errors.New("module failed")
	var closeCalls atomic.Int32
	host, err = New(testConfig("127.0.0.1:0"), func(context.Context) (Runtime, error) {
		return Runtime{Application: app, Close: func(context.Context) error {
			closeCalls.Add(1)
			return nil
		}}, nil
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := host.Run(context.Background()); !errors.Is(err, app.startErr) {
		t.Fatalf("start failure = %v, want %v", err, app.startErr)
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("runtime close calls = %d, want 1", closeCalls.Load())
	}
}

func TestHostStopsApplicationWhenAfterStartFails(t *testing.T) {
	app := newFakeApplication()
	wantErr := errors.New("startup callback failed")
	host, err := New(testConfig("127.0.0.1:0"), func(context.Context) (Runtime, error) {
		return Runtime{
			Application: app,
			AfterStart: func(context.Context) error {
				if app.State().State != application.StateReady {
					t.Fatal("AfterStart ran before the application became ready")
				}
				return wantErr
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := host.Run(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
	if !app.started.Load() || !app.stopped.Load() {
		t.Fatalf("application lifecycle started=%v stopped=%v", app.started.Load(), app.stopped.Load())
	}
	select {
	case <-host.Started():
		t.Fatal("host reported Started after AfterStart failure")
	default:
	}
}

func TestHostValidatesTLSFilesAsAPair(t *testing.T) {
	config := testConfig("127.0.0.1:0")
	config.TLS.CertificateFile = "server.crt"
	_, err := New(config, func(context.Context) (Runtime, error) { return Runtime{}, nil })
	if err == nil || !strings.Contains(err.Error(), "configured together") {
		t.Fatalf("New with incomplete TLS configuration = %v", err)
	}
}

func TestHostAppliesHTTPHardeningDefaults(t *testing.T) {
	host, err := New(testConfig("127.0.0.1:0"), func(context.Context) (Runtime, error) { return Runtime{}, nil })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if host.config.ReadHeaderTimeout <= 0 || host.config.IdleTimeout <= 0 || host.config.MaxHeaderBytes <= 0 {
		t.Fatalf("HTTP hardening defaults = header=%s idle=%s max=%d", host.config.ReadHeaderTimeout, host.config.IdleTimeout, host.config.MaxHeaderBytes)
	}
	for _, field := range []struct {
		name string
		set  func(*Config)
	}{
		{name: "read header", set: func(config *Config) { config.ReadHeaderTimeout = -time.Second }},
		{name: "idle", set: func(config *Config) { config.IdleTimeout = -time.Second }},
		{name: "max header", set: func(config *Config) { config.MaxHeaderBytes = -1 }},
	} {
		config := testConfig("127.0.0.1:0")
		field.set(&config)
		if _, err := New(config, func(context.Context) (Runtime, error) { return Runtime{}, nil }); err == nil {
			t.Fatalf("negative %s setting was accepted", field.name)
		}
	}
}

func TestHostRejectsInconsistentCapabilitiesBeforeBuild(t *testing.T) {
	config := testConfig("127.0.0.1:0")
	config.Capabilities.Database = "postgres"
	var builds atomic.Int32
	_, err := New(config, func(context.Context) (Runtime, error) {
		builds.Add(1)
		return Runtime{}, nil
	})
	if err == nil || builds.Load() != 0 {
		t.Fatalf("New capabilities error = %v, build calls = %d", err, builds.Load())
	}
}

func TestHostReleasesRuntimeWhenShutdownTimesOut(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	app := newFakeApplication()
	app.stop = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	var closeCalls atomic.Int32
	config := testConfig(listener.Addr().String())
	config.ShutdownTimeout = 20 * time.Millisecond
	host, err := New(config, func(context.Context) (Runtime, error) {
		return Runtime{
			Application: app,
			Close: func(context.Context) error {
				closeCalls.Add(1)
				return nil
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	host.listen = func(context.Context, string) (net.Listener, error) { return listener, nil }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Run(ctx) }()
	select {
	case <-host.Started():
	case <-time.After(3 * time.Second):
		t.Fatal("host did not report start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run error = %v, want shutdown deadline", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("host did not stop after shutdown timeout")
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("runtime close calls = %d, want 1", closeCalls.Load())
	}
}

func TestCloseRuntimeBoundsNonCooperativeCleanup(t *testing.T) {
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	cleanupFinished := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseCleanup) })

	startedAt := time.Now()
	err := closeRuntime(20*time.Millisecond, func(context.Context) error {
		close(cleanupStarted)
		<-releaseCleanup
		close(cleanupFinished)
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("closeRuntime error = %v, want deadline", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("closeRuntime ignored its deadline and took %s", elapsed)
	}
	select {
	case <-cleanupStarted:
	default:
		t.Fatal("runtime cleanup was not started")
	}
	releaseOnce.Do(func() { close(releaseCleanup) })
	select {
	case <-cleanupFinished:
	case <-time.After(time.Second):
		t.Fatal("runtime cleanup did not continue after the Host deadline")
	}
}

type fakeApplication struct {
	mu       sync.Mutex
	state    application.State
	started  atomic.Bool
	stopped  atomic.Bool
	startErr error
	stop     func(context.Context) error
}

func newFakeApplication() *fakeApplication {
	return &fakeApplication{state: application.StateConstructed}
}

func (app *fakeApplication) Handler() http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/business" {
			http.NotFound(response, request)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})
}

func (app *fakeApplication) Start(context.Context) error {
	if app.startErr != nil {
		app.setState(application.StateFailed)
		return app.startErr
	}
	app.started.Store(true)
	app.setState(application.StateReady)
	return nil
}

func (app *fakeApplication) Stop(ctx context.Context) error {
	app.stopped.Store(true)
	app.setState(application.StateStopped)
	if app.stop != nil {
		return app.stop(ctx)
	}
	return nil
}

func (app *fakeApplication) State() application.Snapshot {
	app.mu.Lock()
	defer app.mu.Unlock()
	return application.Snapshot{State: app.state}
}

func (app *fakeApplication) setState(state application.State) {
	app.mu.Lock()
	app.state = state
	app.mu.Unlock()
}

func assertHTTPStatus(t *testing.T, url string, want int) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("GET %s status = %d, want %d", url, response.StatusCode, want)
	}
}

func testConfig(address string) Config {
	return Config{
		Address: address,
		Capabilities: health.Capabilities{
			Profile:  "server-sqlite",
			Version:  "host-test",
			Database: "sqlite",
		},
	}
}
