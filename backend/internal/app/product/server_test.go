package product

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	platformdesktop "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/desktop"
)

func TestServerRuntimeProfileMapsOnlyServerDatabaseModes(t *testing.T) {
	sqlitePath := filepath.Join(t.TempDir(), "server.sqlite3")
	sqlite, err := config.Load(config.Input{Profile: config.ProfileServerSQLite, CLI: map[string]string{"database.path": sqlitePath}})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := serverRuntimeProfile(sqlite)
	if err != nil {
		t.Fatal(err)
	}
	if profile.address != "127.0.0.1:8080" || profile.database.Profile != config.ProfileServerSQLite ||
		profile.database.SQLitePath != sqlitePath || profile.databaseCapability != "sqlite" {
		t.Fatalf("SQLite runtime = %#v", profile)
	}

	desktop, err := config.Load(config.Input{Profile: config.ProfileDesktopSQLite, Desktop: config.DesktopMaterial{
		DataDirectory: filepath.Join(t.TempDir(), "data"), LogDirectory: filepath.Join(t.TempDir(), "logs"),
		LoopbackPort: 4321, StartupToken: "0123456789abcdef0123456789abcdef",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serverRuntimeProfile(desktop); err == nil {
		t.Fatal("serverRuntimeProfile accepted Desktop profile")
	}
}

func TestServerRuntimeProfileMapsPostgresWithoutSQLiteState(t *testing.T) {
	snapshot, err := config.Load(config.Input{
		Profile:     config.ProfileServerPostgres,
		Environment: map[string]string{"GO_ADMIN_DATABASE_DSN": "postgres://operator:secret@localhost/product"},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := serverRuntimeProfile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if profile.database.Profile != config.ProfileServerPostgres || profile.database.PostgresDSN == "" ||
		profile.database.SQLitePath != "" || profile.databaseCapability != "postgres" {
		t.Fatalf("PostgreSQL runtime = %#v", profile)
	}
	if dialect, err := database.DialectForProfile(profile.database.Profile); err != nil || dialect != database.DialectPostgres {
		t.Fatalf("PostgreSQL dialect = %q, %v", dialect, err)
	}
	if _, err := NewServerHost(ServerLaunch{
		Snapshot: snapshot, DataRoot: t.TempDir(), Version: "test", WithWorker: true,
	}); err != nil {
		t.Fatalf("PostgreSQL combined runtime rejected: %v", err)
	}
}

func TestNewServerHostOwnsRuntimePathResolution(t *testing.T) {
	snapshot, err := config.Load(config.Input{
		Profile: config.ProfileServerSQLite,
		CLI:     map[string]string{"database.path": filepath.Join(t.TempDir(), "server.sqlite3")},
	})
	if err != nil {
		t.Fatal(err)
	}
	dataRoot := filepath.Join(t.TempDir(), "runtime")
	if _, err := NewServerHost(ServerLaunch{
		Snapshot: snapshot, DataRoot: dataRoot, Version: "test",
	}); err != nil {
		t.Fatalf("NewServerHost: %v", err)
	}
	if resolved, err := canonicalServerDataRoot(dataRoot); err != nil || resolved == "" {
		t.Fatalf("canonical data root = %q, %v", resolved, err)
	}
	if _, err := NewServerHost(ServerLaunch{Snapshot: snapshot, DataRoot: dataRoot}); err == nil {
		t.Fatal("NewServerHost accepted an empty version")
	}
}

func TestNewServerHostBuildsSQLiteProductAndStopsCleanly(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "server.sqlite3")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := config.Load(config.Input{
		Profile: config.ProfileServerSQLite,
		CLI: map[string]string{
			"database.path": databasePath,
			"http.listen":   address,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	dataRoot := filepath.Join(t.TempDir(), "runtime")
	if _, err := MigrateOffline(context.Background(), snapshot, dataRoot); err != nil {
		t.Fatal(err)
	}
	host, err := NewServerHost(ServerLaunch{
		Snapshot: snapshot, DataRoot: dataRoot, Version: "test",
	})
	if err != nil {
		t.Fatalf("NewServerHost: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Run(ctx) }()
	select {
	case <-host.Started():
	case err := <-done:
		t.Fatalf("server stopped before start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	if lock, err := platformdesktop.AcquireInstanceLock(dataRoot); !errors.Is(err, platformdesktop.ErrInstanceLocked) {
		if lock != nil {
			_ = lock.Close()
		}
		t.Fatalf("SQLite server did not hold its runtime lock: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
	lock, err := platformdesktop.AcquireInstanceLock(dataRoot)
	if err != nil {
		t.Fatalf("SQLite runtime lock remained held after shutdown: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{databasePath, filepath.Join(dataRoot, "files")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("runtime path %s: %v", path, err)
		}
	}
}
