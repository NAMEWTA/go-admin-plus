package product

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/application/health"
	serverhost "github.com/NAMEWTA/go-admin-plus/backend/internal/host/server"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/recovery"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	platformdesktop "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/desktop"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/logging"
)

// ServerLaunch contains the typed launch material needed by the Server
// composition root. Process argument and environment parsing remain in cmd.
type ServerLaunch struct {
	Snapshot    config.Snapshot
	DataRoot    string
	Version     string
	WithWorker  bool
	Development bool
}

type serverProfile struct {
	address            string
	database           database.Config
	sessionPolicy      config.SessionPolicy
	databaseCapability string
	logLevel           string
}

// NewServerHost composes the Server profile, database, product and network
// host without exposing concrete runtime dependencies to the command entry.
func NewServerHost(launch ServerLaunch) (*serverhost.Host, error) {
	launch.Version = strings.TrimSpace(launch.Version)
	if launch.Version == "" || strings.TrimSpace(launch.DataRoot) == "" {
		return nil, errors.New("server launch material is invalid")
	}
	profile, err := serverRuntimeProfile(launch.Snapshot)
	if err != nil {
		return nil, err
	}
	dataRoot, err := canonicalServerDataRoot(launch.DataRoot)
	if err != nil {
		return nil, errors.New("server data root failed")
	}
	return serverhost.New(serverhost.Config{
		Address:           profile.address,
		ReadTimeout:       10 * time.Minute,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ShutdownTimeout:   10 * time.Second,
		Capabilities: health.Capabilities{
			Profile:  string(launch.Snapshot.Profile()),
			Version:  launch.Version,
			Database: profile.databaseCapability,
		},
	}, func(ctx context.Context) (serverhost.Runtime, error) {
		var instanceLock *platformdesktop.InstanceLock
		if launch.Snapshot.Profile() == config.ProfileServerSQLite {
			instanceLock, err = platformdesktop.AcquireInstanceLock(dataRoot)
			if err != nil {
				return serverhost.Runtime{}, errors.New("server SQLite instance is already active")
			}
		}
		db, err := database.NewProcess().Open(ctx, profile.database)
		if err != nil {
			_ = instanceLock.Close()
			return serverhost.Runtime{}, errors.New("server database startup failed")
		}
		releasePresence, err := recovery.AcquireRuntimePresence(ctx, db)
		if err != nil {
			_ = db.Close()
			_ = instanceLock.Close()
			return serverhost.Runtime{}, errors.New("server runtime presence failed")
		}
		if err := PrepareRuntimeSchema(ctx, db, false); err != nil {
			_ = releasePresence()
			_ = db.Close()
			_ = instanceLock.Close()
			return serverhost.Runtime{}, err
		}
		logger, logCloser, err := logging.New(logging.Config{
			Service: "go-admin-plus", Version: launch.Version, Profile: string(launch.Snapshot.Profile()), Level: profile.logLevel,
			Sink: logging.Sink{Mode: serverLogMode(launch.Development), Writer: os.Stdout},
		})
		if err != nil {
			_ = releasePresence()
			_ = db.Close()
			_ = instanceLock.Close()
			return serverhost.Runtime{}, errors.New("server logging startup failed")
		}
		built, err := BuildPrepared(ctx, db, configuredServerOptions(profile, dataRoot, launch.Snapshot), launch.WithWorker)
		if err != nil {
			_ = logCloser.Close()
			_ = releasePresence()
			_ = db.Close()
			_ = instanceLock.Close()
			return serverhost.Runtime{}, err
		}
		logger.Info("runtime_ready", slog.String("role", "serve"), slog.Bool("workers", launch.WithWorker))
		return serverhost.Runtime{
			Application: built.Application,
			Readiness:   built.Readiness,
			Logger:      logger,
			Close: func(context.Context) error {
				logger.Info("runtime_stopped", slog.String("role", "serve"))
				return errors.Join(releasePresence(), logCloser.Close(), db.Close(), instanceLock.Close())
			},
		}, nil
	})
}

// RunServerWorker runs the worker role without constructing a network Host.
func RunServerWorker(ctx context.Context, launch ServerLaunch) (resultErr error) {
	if ctx == nil {
		return errors.New("worker context is required")
	}
	launch.Version = strings.TrimSpace(launch.Version)
	if launch.Version == "" || strings.TrimSpace(launch.DataRoot) == "" {
		return errors.New("worker launch material is invalid")
	}
	profile, err := serverRuntimeProfile(launch.Snapshot)
	if err != nil {
		return err
	}
	dataRoot, err := canonicalServerDataRoot(launch.DataRoot)
	if err != nil {
		return errors.New("worker data root failed")
	}
	var instanceLock *platformdesktop.InstanceLock
	if launch.Snapshot.Profile() == config.ProfileServerSQLite {
		instanceLock, err = platformdesktop.AcquireInstanceLock(dataRoot)
		if err != nil {
			return errors.New("worker SQLite instance is already active")
		}
		defer func() { resultErr = errors.Join(resultErr, instanceLock.Close()) }()
	}
	db, err := database.NewProcess().Open(ctx, profile.database)
	if err != nil {
		return errors.New("worker database startup failed")
	}
	defer func() { resultErr = errors.Join(resultErr, db.Close()) }()
	releasePresence, err := recovery.AcquireRuntimePresence(ctx, db)
	if err != nil {
		return errors.New("worker runtime presence failed")
	}
	defer func() { resultErr = errors.Join(resultErr, releasePresence()) }()
	if err := PrepareRuntimeSchema(ctx, db, false); err != nil {
		return err
	}
	logger, closer, err := logging.New(logging.Config{
		Service: "go-admin-plus", Version: launch.Version, Profile: string(launch.Snapshot.Profile()), Level: profile.logLevel,
		Sink: logging.Sink{Mode: serverLogMode(launch.Development), Writer: os.Stdout},
	})
	if err != nil {
		return errors.New("worker logging startup failed")
	}
	defer func() { resultErr = errors.Join(resultErr, closer.Close()) }()
	built, err := BuildPrepared(ctx, db, configuredServerOptions(profile, dataRoot, launch.Snapshot), true)
	if err != nil {
		return err
	}
	if err := built.Application.Start(ctx); err != nil {
		return errors.New("worker application startup failed")
	}
	logger.Info("runtime_ready", slog.String("role", "worker"))
	var workerErr error
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for workerErr == nil {
		select {
		case <-ctx.Done():
			workerErr = nil
			goto stop
		case <-ticker.C:
			for _, checker := range built.Readiness {
				if checker.Name == "workers" {
					if err := checker.Check(ctx); err != nil {
						logger.Warn("worker_retrying", slog.String("component", checker.Name))
					}
				}
			}
		}
	}
stop:
	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	logger.Info("runtime_stopped", slog.String("role", "worker"))
	return errors.Join(workerErr, built.Application.Stop(stopCtx))
}

func serverLogMode(development bool) logging.Mode {
	if development {
		return logging.ModeConsole
	}
	return logging.ModeJSON
}

func serverOptions(profile serverProfile, dataRoot string) Options {
	return Options{
		SessionPolicy: profile.sessionPolicy, FilesRoot: filepath.Join(dataRoot, "files"),
		WorkerOwner: fmt.Sprintf("server-%d", os.Getpid()), WorkerInterval: time.Second,
		AuditRetentionAge: 30 * 24 * time.Hour,
	}
}

func serverRuntimeProfile(snapshot config.Snapshot) (serverProfile, error) {
	if profile, ok := snapshot.ServerSQLite(); ok {
		return serverProfile{
			address:            profile.HTTPListen(),
			database:           database.Config{Profile: config.ProfileServerSQLite, SQLitePath: profile.DatabasePath()},
			sessionPolicy:      profile.SessionPolicy(),
			databaseCapability: "sqlite",
			logLevel:           profile.LogLevel(),
		}, nil
	}
	if profile, ok := snapshot.ServerPostgres(); ok {
		return serverProfile{
			address:            profile.HTTPListen(),
			database:           database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: profile.DatabaseDSN()},
			sessionPolicy:      profile.SessionPolicy(),
			databaseCapability: "postgres",
			logLevel:           profile.LogLevel(),
		}, nil
	}
	return serverProfile{}, errors.New("server runtime profile is invalid")
}

func canonicalServerDataRoot(root string) (string, error) {
	absolute, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func configuredServerOptions(profile serverProfile, dataRoot string, snapshot config.Snapshot) Options {
	options := serverOptions(profile, dataRoot)
	settings := snapshot.Settings()
	options.Settings = &settings
	options.FilesRoot = settings.Storage.Local.Root
	if !filepath.IsAbs(options.FilesRoot) {
		options.FilesRoot = filepath.Join(dataRoot, options.FilesRoot)
	}
	options.AuditRetentionAge = time.Duration(settings.Audit.RetentionDays) * 24 * time.Hour
	return options
}
