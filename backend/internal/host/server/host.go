// Package server owns the network and process lifecycle for a server-hosted
// Application.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/application"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/application/health"
)

// ErrAlreadyRun reports an attempt to start the same single-use Host twice.
var ErrAlreadyRun = errors.New("server host has already run")

const (
	defaultShutdownTimeout   = 5 * time.Second
	defaultReadHeaderTimeout = 10 * time.Second
	defaultIdleTimeout       = 120 * time.Second
	defaultMaxHeaderBytes    = 1 << 20
)

// Application is the complete interface the ServerHost needs from the
// host-neutral application runtime.
type Application interface {
	Handler() http.Handler
	Start(context.Context) error
	Stop(context.Context) error
	State() application.Snapshot
}

// Runtime is one fully built Application and its profile-owned resources.
// Close is always called after a successful Build, including bind failures.
type Runtime struct {
	Application Application
	Readiness   []health.Checker
	Logger      *slog.Logger
	AfterStart  func(context.Context) error
	Close       func(context.Context) error
}

// Builder completes configuration, profile construction and migrations
// before returning a runtime that may become externally reachable.
type Builder func(context.Context) (Runtime, error)

// TLSConfig keeps direct TLS support while allowing a reverse proxy to leave
// it disabled.
type TLSConfig struct {
	CertificateFile string
	KeyFile         string
}

// Config describes only process-host concerns. Database and module wiring
// belong to Builder.
type Config struct {
	Address           string
	ReadTimeout       time.Duration
	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
	ShutdownTimeout   time.Duration
	TLS               TLSConfig
	Capabilities      health.Capabilities
}

// Host owns one run of one network listener.
type Host struct {
	config  Config
	build   Builder
	listen  func(context.Context, string) (net.Listener, error)
	started chan struct{}
	run     atomic.Bool
	start   sync.Once
}

// New validates the stable ServerHost contract.
func New(config Config, build Builder) (*Host, error) {
	config.Address = strings.TrimSpace(config.Address)
	if config.Address == "" {
		return nil, errors.New("server listen address is required")
	}
	if build == nil {
		return nil, errors.New("server runtime builder is required")
	}
	if config.ReadTimeout < 0 || config.ReadHeaderTimeout < 0 || config.WriteTimeout < 0 || config.IdleTimeout < 0 || config.MaxHeaderBytes < 0 || config.ShutdownTimeout < 0 {
		return nil, errors.New("server timeouts cannot be negative")
	}
	if config.ReadHeaderTimeout == 0 {
		config.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = defaultIdleTimeout
	}
	if config.MaxHeaderBytes == 0 {
		config.MaxHeaderBytes = defaultMaxHeaderBytes
	}
	if (config.TLS.CertificateFile == "") != (config.TLS.KeyFile == "") {
		return nil, errors.New("TLS certificate and key must be configured together")
	}
	if config.ShutdownTimeout == 0 {
		config.ShutdownTimeout = defaultShutdownTimeout
	}
	if err := config.Capabilities.Validate(); err != nil {
		return nil, errors.New("server host capabilities are invalid")
	}
	return &Host{
		config:  config,
		build:   build,
		listen:  listenTCP,
		started: make(chan struct{}),
	}, nil
}

// Started closes after the Application is ready and the HTTP serve loop has
// been launched. It never closes when Build, bind or Start fails.
func (host *Host) Started() <-chan struct{} { return host.started }

// Run owns signal subscription, listener, HTTP server and ordered cleanup.
func (host *Host) Run(parent context.Context) (runErr error) {
	if parent == nil {
		return errors.New("server host context is required")
	}
	if !host.run.CompareAndSwap(false, true) {
		return ErrAlreadyRun
	}

	runCtx, stopSignals := signal.NotifyContext(parent, terminationSignals()...)
	defer stopSignals()
	runtime, err := host.build(runCtx)
	if err != nil {
		return fmt.Errorf("build server runtime: %w", err)
	}
	if runtime.Application == nil {
		return errors.Join(errors.New("server runtime application is required"), closeRuntime(host.config.ShutdownTimeout, runtime.Close))
	}
	defer func() {
		runErr = errors.Join(runErr, closeRuntime(host.config.ShutdownTimeout, runtime.Close))
	}()

	operations, err := health.New(
		func() application.Snapshot { return runtime.Application.State() },
		host.config.Capabilities,
		runtime.Readiness...,
	)
	if err != nil {
		return fmt.Errorf("build server operations handler: %w", err)
	}
	listener, err := host.listen(runCtx, host.config.Address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", host.config.Address, err)
	}
	listenerOwned := true
	defer func() {
		if listenerOwned {
			runErr = errors.Join(runErr, listener.Close())
		}
	}()

	if err := runtime.Application.Start(runCtx); err != nil {
		return fmt.Errorf("start application: %w", err)
	}
	applicationStarted := true
	if runtime.AfterStart != nil {
		if err := runtime.AfterStart(runCtx); err != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), host.config.ShutdownTimeout)
			defer cancel()
			return errors.Join(
				fmt.Errorf("complete server startup: %w", err),
				runtime.Application.Stop(shutdownCtx),
			)
		}
	}

	applicationHandler := runtime.Application.Handler()
	if runtime.Logger != nil {
		applicationHandler = requestLogger(runtime.Logger, host.config.Capabilities.Database, applicationHandler)
	}
	httpServer := &http.Server{
		Addr:              host.config.Address,
		Handler:           operations.Wrap(applicationHandler),
		ReadTimeout:       host.config.ReadTimeout,
		ReadHeaderTimeout: host.config.ReadHeaderTimeout,
		WriteTimeout:      host.config.WriteTimeout,
		IdleTimeout:       host.config.IdleTimeout,
		MaxHeaderBytes:    host.config.MaxHeaderBytes,
	}
	serveErrors := make(chan error, 1)
	go func() {
		var serveErr error
		if host.config.TLS.CertificateFile != "" {
			serveErr = httpServer.ServeTLS(listener, host.config.TLS.CertificateFile, host.config.TLS.KeyFile)
		} else {
			serveErr = httpServer.Serve(listener)
		}
		serveErrors <- serveErr
	}()
	host.start.Do(func() { close(host.started) })

	var serveErr error
	select {
	case <-runCtx.Done():
	case serveErr = <-serveErrors:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		} else if serveErr != nil {
			serveErr = fmt.Errorf("serve HTTP: %w", serveErr)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), host.config.ShutdownTimeout)
	defer cancel()
	serverStopErr := httpServer.Shutdown(shutdownCtx)
	if serverStopErr != nil {
		serverStopErr = errors.Join(serverStopErr, httpServer.Close())
	}
	listenerOwned = false

	var applicationStopErr error
	if applicationStarted {
		applicationStopErr = runtime.Application.Stop(shutdownCtx)
	}
	return errors.Join(serveErr, serverStopErr, applicationStopErr)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (writer *statusWriter) WriteHeader(status int) {
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func requestLogger(logger *slog.Logger, databaseName string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		writer := &statusWriter{ResponseWriter: response, status: http.StatusOK}
		next.ServeHTTP(writer, request)
		logger.InfoContext(request.Context(), "http_request",
			slog.String("route", request.URL.Path),
			slog.String("module", "http"),
			slog.Int("status", writer.status),
			slog.Int64("latency_ms", time.Since(started).Milliseconds()),
			slog.String("database", databaseName),
		)
	})
}

func listenTCP(ctx context.Context, address string) (net.Listener, error) {
	var config net.ListenConfig
	return config.Listen(ctx, "tcp", address)
}

func closeRuntime(timeout time.Duration, close func(context.Context) error) error {
	if close == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- close(ctx) }()
	select {
	case err := <-result:
		if err != nil {
			return fmt.Errorf("close server runtime: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("close server runtime: %w", ctx.Err())
	}
}
