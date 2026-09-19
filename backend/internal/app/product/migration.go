package product

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/recovery"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	platformdesktop "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/desktop"
)

var ErrSchemaIncompatible = errors.New("product schema is incompatible")

// MigrationResult is the command-facing projection of one forward migration.
// It deliberately does not expose the platform migration implementation.
type MigrationResult struct {
	Applied        int
	CurrentVersion int64
}

// Migrate owns database selection, process lifecycle and product migration
// composition for one typed Server profile.
func Migrate(ctx context.Context, snapshot config.Snapshot) (result MigrationResult, resultErr error) {
	if ctx == nil {
		return MigrationResult{}, errors.New("migration context is required")
	}
	databaseConfig, err := migrationDatabaseConfig(snapshot)
	if err != nil {
		return MigrationResult{}, err
	}
	db, err := database.NewProcess().Open(ctx, databaseConfig)
	if err != nil {
		return MigrationResult{}, errors.New("migration database startup failed")
	}
	defer func() {
		if err := db.Close(); err != nil {
			resultErr = errors.Join(resultErr, errors.New("migration database shutdown failed"))
		}
	}()
	guard, err := recovery.NewDatabaseOfflineGuard(db)
	if err != nil {
		return MigrationResult{}, errors.New("migration offline guard failed")
	}
	release, err := guard.Acquire(ctx)
	if err != nil {
		return MigrationResult{}, errors.New("migration requires an offline runtime")
	}
	defer func() {
		if err := release(); err != nil {
			resultErr = errors.Join(resultErr, errors.New("migration guard release failed"))
		}
	}()
	runner, err := NewMigrationRunner()
	if err != nil {
		return MigrationResult{}, errors.New("migration composition failed")
	}
	applied, err := runner.Up(ctx, db)
	if err != nil {
		return MigrationResult{}, err
	}
	return MigrationResult{Applied: applied.Applied, CurrentVersion: applied.CurrentVersion}, nil
}

// MigrateOffline adds the cross-process SQLite runtime fence used by the
// unified CLI. PostgreSQL is fenced by Migrate's database advisory guard.
func MigrateOffline(ctx context.Context, snapshot config.Snapshot, dataRoot string) (MigrationResult, error) {
	if snapshot.Profile() != config.ProfileServerSQLite {
		return Migrate(ctx, snapshot)
	}
	lock, err := platformdesktop.AcquireInstanceLock(dataRoot)
	if err != nil {
		return MigrationResult{}, errors.New("migration requires an offline runtime")
	}
	defer lock.Close()
	return Migrate(ctx, snapshot)
}

// PrepareRuntimeSchema applies SQLite's embedded migration policy or verifies
// that an externally migrated PostgreSQL schema exactly matches this binary.
func PrepareRuntimeSchema(ctx context.Context, db *database.Database, applySQLite bool) error {
	if ctx == nil || db == nil {
		return ErrSchemaIncompatible
	}
	runner, err := NewMigrationRunner()
	if err != nil {
		return ErrSchemaIncompatible
	}
	if applySQLite {
		if db.Dialect() != database.DialectSQLite {
			return ErrSchemaIncompatible
		}
		if _, err := runner.Up(ctx, db); err != nil {
			return ErrSchemaIncompatible
		}
		return nil
	}
	expected, err := expectedSchemaVersion(runner, db.Dialect())
	if err != nil {
		return ErrSchemaIncompatible
	}
	var current int64
	err = db.SQL().QueryRowContext(ctx,
		`SELECT version_id FROM goose_db_version WHERE is_applied = TRUE ORDER BY id DESC LIMIT 1`).Scan(&current)
	if err != nil || current != expected {
		return ErrSchemaIncompatible
	}
	return nil
}

func expectedSchemaVersion(runner interface {
	Compose(database.Dialect) (fs.FS, error)
}, dialect database.Dialect) (int64, error) {
	composed, err := runner.Compose(dialect)
	if err != nil {
		return 0, err
	}
	entries, err := fs.ReadDir(composed, ".")
	if err != nil || len(entries) == 0 {
		return 0, errors.New("product schema manifest is empty")
	}
	var expected int64
	for _, entry := range entries {
		prefix, _, found := strings.Cut(path.Base(entry.Name()), "_")
		version, parseErr := strconv.ParseInt(prefix, 10, 64)
		if !found || parseErr != nil || version <= 0 {
			return 0, errors.New("product schema manifest is invalid")
		}
		if version > expected {
			expected = version
		}
	}
	return expected, nil
}

func migrationDatabaseConfig(snapshot config.Snapshot) (database.Config, error) {
	if profile, ok := snapshot.ServerSQLite(); ok {
		return database.Config{Profile: config.ProfileServerSQLite, SQLitePath: profile.DatabasePath()}, nil
	}
	if profile, ok := snapshot.ServerPostgres(); ok {
		return database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: profile.DatabaseDSN()}, nil
	}
	return database.Config{}, errors.New("migration runtime profile is invalid")
}
