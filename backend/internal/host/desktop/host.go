// Package desktop owns the local sidecar resources and lifecycle used by the
// Tauri desktop host.
package desktop

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/application"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/application/health"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/host/lifecycle"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	desktopplatform "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/desktop"
)

const (
	requestTimeout  = 15 * time.Second
	shutdownTimeout = 5 * time.Second
)

// Application is the host-neutral application surface owned by the Desktop
// product composition root.
type Application interface {
	Handler() http.Handler
	Start(context.Context) error
	Stop(context.Context) error
	State() application.Snapshot
}

// PrivateRoute is an optional build-specific control route. Host applies the
// same loopback control boundary used by every non-readiness route.
type PrivateRoute struct {
	Pattern string
	Handler http.Handler
}

// Product is the complete application runtime returned by Builder.
type Product struct {
	Application  Application
	Readiness    []health.Checker
	PrivateRoute *PrivateRoute
}

// ProductOptions contains paths and worker identity chosen by the Desktop
// host. Business composition remains the Builder's responsibility.
type ProductOptions struct {
	FilesRoot   string
	WorkerOwner string
}

// Builder composes the product after the Desktop database is available.
type Builder func(context.Context, *database.Database, ProductOptions) (Product, error)

// Config contains the process inputs required by one Desktop Host.
type Config struct {
	Launch  desktopplatform.LaunchMaterial
	Version string
	Stop    context.CancelFunc
	Build   Builder
}

// Host owns one Desktop instance lock, database, application and loopback
// HTTP server.
type Host struct {
	config      Config
	owner       *lifecycle.Manager
	database    *database.Database
	backup      desktopplatform.DatabaseBackup
	instance    *desktopplatform.InstanceLock
	listener    net.Listener
	server      *http.Server
	application Application
	serveErrors chan error
	stopOnce    sync.Once
}

// New validates and assembles a single-use Desktop Host.
func New(value Config) (*Host, error) {
	if err := value.Launch.Validate(); err != nil || strings.TrimSpace(value.Version) == "" || value.Stop == nil || value.Build == nil {
		return nil, errors.New("desktop host dependencies are invalid")
	}
	host := &Host{config: value, serveErrors: make(chan error, 1)}
	owner, err := lifecycle.New(
		lifecycle.Lifecycle{Name: "desktop-instance", Start: host.startInstance, Drain: noopLifecycle, Stop: host.stopInstance},
		lifecycle.Lifecycle{Name: "desktop-database", Start: host.startDatabase, Drain: noopLifecycle, Stop: host.stopDatabase},
		lifecycle.Lifecycle{Name: "desktop-http", Start: host.startHTTP, Drain: host.drainHTTP, Stop: host.stopHTTP},
	)
	if err != nil {
		return nil, err
	}
	host.owner = owner
	return host, nil
}

// Start acquires and starts all Desktop resources in dependency order.
func (host *Host) Start(ctx context.Context) error { return host.owner.Start(ctx) }

// Drain stops accepting requests and releases all Desktop resources.
func (host *Host) Drain(ctx context.Context) error { return host.owner.Drain(ctx) }

// Port returns the operating-system-assigned loopback port after Start.
func (host *Host) Port() uint16 {
	if host.listener == nil {
		return 0
	}
	return uint16(host.listener.Addr().(*net.TCPAddr).Port)
}

// ServeErrors reports the terminal HTTP serve result.
func (host *Host) ServeErrors() <-chan error { return host.serveErrors }

func (host *Host) startInstance(context.Context) error {
	if err := secureDirectories(host.config.Launch.DataDirectory, host.config.Launch.LogDirectory); err != nil {
		return err
	}
	lock, err := desktopplatform.AcquireSecureInstanceLock(host.config.Launch.DataDirectory)
	if err != nil {
		return errors.New("desktop instance lock failed")
	}
	host.instance = lock
	return nil
}

func (host *Host) stopInstance(context.Context) error {
	if host.instance == nil {
		return nil
	}
	err := host.instance.Close()
	host.instance = nil
	return err
}

func (host *Host) startDatabase(ctx context.Context) error {
	databasePath := filepath.Join(host.config.Launch.DataDirectory, "go-admin-plus.db")
	backup, err := desktopplatform.BackupDatabase(databasePath, filepath.Join(host.config.Launch.DataDirectory, "backups"), time.Now())
	if err != nil {
		return errors.New("desktop database backup failed")
	}
	host.backup = backup
	db, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileDesktopSQLite, SQLitePath: databasePath})
	if err != nil {
		return databaseStartupFailure(nil, host.backup.Restore, "desktop database open failed")
	}
	host.database = db
	return nil
}

func (host *Host) recoverDatabase() error {
	if host.database == nil {
		return errors.New("desktop database recovery failed")
	}
	db := host.database
	host.database = nil
	return databaseStartupFailure(db.Close, host.backup.Restore, "desktop database migration failed")
}

func databaseStartupFailure(closeDatabase, restoreDatabase func() error, cause string) error {
	if closeDatabase != nil && closeDatabase() != nil {
		return errors.New("desktop database recovery failed")
	}
	if restoreDatabase() != nil {
		return errors.New("desktop database recovery failed")
	}
	return errors.New(cause)
}

func (host *Host) stopDatabase(context.Context) error {
	if host.database == nil {
		return nil
	}
	err := host.database.Close()
	host.database = nil
	return err
}

func (host *Host) startHTTP(ctx context.Context) error {
	handler, err := host.buildHandler(ctx)
	if err != nil {
		return err
	}
	var listenerConfig net.ListenConfig
	listener, err := listenerConfig.Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return host.abortHTTPStart("desktop loopback listener failed")
	}
	if !listener.Addr().(*net.TCPAddr).IP.IsLoopback() {
		_ = listener.Close()
		return host.abortHTTPStart("desktop loopback listener is unsafe")
	}
	host.listener = listener
	host.server = &http.Server{
		Handler: handler, ReadHeaderTimeout: requestTimeout, ReadTimeout: requestTimeout,
		WriteTimeout: requestTimeout, IdleTimeout: requestTimeout,
	}
	go func() { host.serveErrors <- host.server.Serve(listener) }()
	return nil
}

func (host *Host) buildHandler(ctx context.Context) (http.Handler, error) {
	built, err := host.config.Build(ctx, host.database, ProductOptions{
		FilesRoot:   filepath.Join(host.config.Launch.DataDirectory, "files"),
		WorkerOwner: fmt.Sprintf("desktop-%d", os.Getpid()),
	})
	if err != nil || built.Application == nil {
		return nil, host.recoverDatabase()
	}
	if err := built.Application.Start(ctx); err != nil {
		return nil, host.recoverDatabase()
	}
	host.application = built.Application
	operations, err := health.New(
		func() application.Snapshot { return built.Application.State() },
		health.Capabilities{
			Profile: "desktop-sqlite", Version: host.config.Version, Database: "sqlite",
			Desktop: true, Offline: true, NativeDialogs: true,
		},
		built.Readiness...,
	)
	if err != nil {
		return nil, host.abortHTTPStart("desktop operations handler failed")
	}
	readiness, err := desktopplatform.NewNonceGate(host.config.Launch.ReadinessNonce)
	if err != nil {
		return nil, host.abortHTTPStart("desktop readiness handler failed")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /__desktop/ready", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		if request.Header.Get("Origin") != "" || !isLoopback(request.RemoteAddr) || !readiness.Consume(request.Header.Get(desktopplatform.ReadinessHeader)) {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"state":"ready"}`))
	})
	controlled := host.requireControl(operations.Wrap(built.Application.Handler()))
	mux.Handle("/api/", controlled)
	mux.Handle("/health/", controlled)
	mux.Handle("/metrics", controlled)
	mux.Handle("POST /__desktop/shutdown", host.requireControl(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
		host.stopOnce.Do(host.config.Stop)
	})))
	if built.PrivateRoute != nil {
		if err := validatePrivateRoute(*built.PrivateRoute); err != nil {
			return nil, host.abortHTTPStart("desktop private route failed")
		}
		mux.Handle(built.PrivateRoute.Pattern, host.requireControl(built.PrivateRoute.Handler))
	}
	return mux, nil
}

func validatePrivateRoute(route PrivateRoute) error {
	parts := strings.Split(route.Pattern, " ")
	if route.Handler == nil || len(parts) != 2 || parts[0] != http.MethodPost ||
		!strings.HasPrefix(parts[1], "/__desktop/") || parts[1] == "/__desktop/" ||
		parts[1] == "/__desktop/ready" || parts[1] == "/__desktop/shutdown" ||
		strings.ContainsAny(parts[1], "{}*$") || strings.TrimSpace(route.Pattern) != route.Pattern {
		return errors.New("desktop private route is invalid")
	}
	return nil
}

func (host *Host) requireControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		if request.Header.Get("Origin") != "" || !isLoopback(request.RemoteAddr) ||
			!desktopplatform.MatchesControl(host.config.Launch.ControlToken, request.Header.Get(desktopplatform.ControlHeader)) {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func isLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback()
}

func (host *Host) drainHTTP(ctx context.Context) error {
	if host.server == nil {
		return nil
	}
	return host.server.Shutdown(ctx)
}

func (host *Host) stopHTTP(context.Context) error {
	var err error
	if host.server != nil {
		err = host.server.Close()
	}
	select {
	case serveErr := <-host.serveErrors:
		if !errors.Is(serveErr, http.ErrServerClosed) {
			err = errors.Join(err, serveErr)
		}
	default:
	}
	if host.application != nil {
		stop, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		err = errors.Join(err, host.application.Stop(stop))
		cancel()
		host.application = nil
	}
	return err
}

func (host *Host) abortHTTPStart(cause string) error {
	if host.application != nil {
		stop, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		_ = host.application.Stop(stop)
		cancel()
		host.application = nil
	}
	_ = host.recoverDatabase()
	return errors.New(cause)
}

func noopLifecycle(context.Context) error { return nil }

func secureDirectories(dataDirectory, logDirectory string) error {
	for _, directory := range []string{dataDirectory, logDirectory} {
		if !filepath.IsAbs(directory) {
			return errors.New("desktop runtime directory is invalid")
		}
		if _, err := desktopplatform.EnsurePrivateDirectory(filepath.Clean(directory)); err != nil {
			return errors.New("desktop runtime directory cannot be secured")
		}
	}
	dataDirectory, logDirectory = filepath.Clean(dataDirectory), filepath.Clean(logDirectory)
	dataToLog, dataErr := filepath.Rel(dataDirectory, logDirectory)
	logToData, logErr := filepath.Rel(logDirectory, dataDirectory)
	if dataErr != nil || logErr != nil || dataToLog == "." || dataToLog != ".." && !strings.HasPrefix(dataToLog, ".."+string(filepath.Separator)) ||
		logToData != ".." && !strings.HasPrefix(logToData, ".."+string(filepath.Separator)) {
		return errors.New("desktop runtime directories conflict")
	}
	return nil
}
