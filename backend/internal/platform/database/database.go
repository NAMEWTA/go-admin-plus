// Package database owns the process-wide SQL connection and transaction boundary.
package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gofrs/flock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	_ "modernc.org/sqlite"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
)

// Dialect identifies the SQL grammar selected for this process.
type Dialect string

const (
	DialectPostgres Dialect = "postgres"
	DialectSQLite   Dialect = "sqlite"
)

var (
	ErrAlreadyOpened  = errors.New("database process already opened")
	ErrInstanceLocked = errors.New("sqlite database is already owned by another process")
)

// Tx is the SQL-first Bun transaction surface exposed to repositories.
type Tx = bun.IDB

// Config contains startup-only database material. Its serialization methods intentionally redact
// connection strings and paths because startup diagnostics are routinely structured.
type Config struct {
	Profile            config.Profile
	PostgresDSN        string
	SQLitePath         string
	MaxOpenConnections int
	MaxIdleConnections int
}

func (c Config) String() string {
	return fmt.Sprintf("database.Config{Profile:%q, PostgresDSN:[redacted], SQLitePath:[redacted], MaxOpenConnections:%d, MaxIdleConnections:%d}", c.Profile, c.MaxOpenConnections, c.MaxIdleConnections)
}

func (c Config) GoString() string { return c.String() }

func (c Config) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Profile            config.Profile `json:"profile"`
		PostgresDSN        string         `json:"postgres_dsn"`
		SQLitePath         string         `json:"sqlite_path"`
		MaxOpenConnections int            `json:"max_open_connections"`
		MaxIdleConnections int            `json:"max_idle_connections"`
	}{c.Profile, "[redacted]", "[redacted]", c.MaxOpenConnections, c.MaxIdleConnections})
}

func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("profile", string(c.Profile)),
		slog.String("postgres_dsn", "[redacted]"),
		slog.String("sqlite_path", "[redacted]"),
		slog.Int("max_open_connections", c.MaxOpenConnections),
		slog.Int("max_idle_connections", c.MaxIdleConnections),
	)
}

// Process is a single-use owner. A successful Open permanently consumes it, even after Close.
type Process struct {
	mu     sync.Mutex
	opened bool
}

func NewProcess() *Process { return &Process{} }

// Open validates the selected profile, obtains any process lock, then proves connectivity before
// publishing the Database. Errors deliberately describe only the failed stage.
func (p *Process) Open(ctx context.Context, cfg Config) (*Database, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.opened {
		return nil, ErrAlreadyOpened
	}

	db, err := open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	p.opened = true
	return db, nil
}

// Database is the sole SQL/Bun connection published within one application process.
type Database struct {
	sqlDB    *sql.DB
	bunDB    *bun.DB
	dialect  Dialect
	release  func() error
	once     sync.Once
	closeErr error
}

func (db *Database) SQL() *sql.DB     { return db.sqlDB }
func (db *Database) Bun() *bun.DB     { return db.bunDB }
func (db *Database) Dialect() Dialect { return db.dialect }

func (db *Database) WithinTx(ctx context.Context, fn func(context.Context, Tx) error) error {
	if fn == nil {
		return errors.New("transaction callback is required")
	}
	return db.bunDB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return fn(ctx, tx)
	})
}

func (db *Database) Close() error {
	db.once.Do(func() {
		var failed bool
		if db.bunDB != nil && db.bunDB.Close() != nil {
			failed = true
		}
		if db.release != nil && db.release() != nil {
			failed = true
		}
		if failed {
			db.closeErr = errors.New("database close failed")
		}
	})
	return db.closeErr
}

func open(ctx context.Context, cfg Config) (*Database, error) {
	dialect, err := DialectForProfile(cfg.Profile)
	if err != nil {
		return nil, err
	}
	switch dialect {
	case DialectPostgres:
		return openPostgres(ctx, cfg)
	case DialectSQLite:
		return openSQLite(ctx, cfg)
	default:
		return nil, errors.New("unsupported database profile")
	}
}

// DialectForProfile is the exhaustive profile-to-driver decision used before any connection I/O.
func DialectForProfile(profile config.Profile) (Dialect, error) {
	switch profile {
	case config.ProfileServerPostgres:
		return DialectPostgres, nil
	case config.ProfileServerSQLite, config.ProfileDesktopSQLite:
		return DialectSQLite, nil
	default:
		return "", errors.New("unsupported database profile")
	}
}

func openPostgres(ctx context.Context, cfg Config) (*Database, error) {
	if cfg.PostgresDSN == "" {
		return nil, errors.New("postgres connection material is required")
	}
	parsed, err := pgx.ParseConfig(cfg.PostgresDSN)
	if err != nil {
		return nil, errors.New("postgres connection material is invalid")
	}
	sqlDB := stdlib.OpenDB(*parsed)
	configurePool(sqlDB, cfg, 20)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, sanitizedContextError(ctx, "postgres connection check failed", err)
	}
	bunDB := bun.NewDB(sqlDB, pgdialect.New())
	return &Database{sqlDB: sqlDB, bunDB: bunDB, dialect: DialectPostgres}, nil
}

func openSQLite(ctx context.Context, cfg Config) (*Database, error) {
	if cfg.SQLitePath == "" {
		return nil, errors.New("sqlite database path is required")
	}
	path, err := filepath.Abs(filepath.Clean(cfg.SQLitePath))
	if err != nil {
		return nil, errors.New("sqlite database path is invalid")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, errors.New("sqlite database directory is unavailable")
	}
	owner := flock.New(path + ".lock")
	release, err := acquireInstanceLock(owner)
	if err != nil {
		return nil, err
	}

	dsn, err := buildSQLiteURI(path)
	if err != nil {
		_ = release()
		return nil, errors.New("sqlite database path is invalid")
	}
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = release()
		return nil, errors.New("sqlite open failed")
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		_ = release()
		return nil, sanitizedContextError(ctx, "sqlite connection check failed", err)
	}
	bunDB := bun.NewDB(sqlDB, sqlitedialect.New())
	return &Database{sqlDB: sqlDB, bunDB: bunDB, dialect: DialectSQLite, release: release}, nil
}

// buildSQLiteURI normalizes absolute paths without consulting the host GOOS. SQLite requires an
// empty URI authority; a UNC server therefore remains the first segment of a double-slash path.
func buildSQLiteURI(databasePath string) (string, error) {
	if databasePath == "" {
		return "", errors.New("sqlite database path is required")
	}
	// Rust canonicalize 在 Windows 返回扩展路径。SQLite URI 不认识设备前缀，
	// 只将本地盘符和 UNC 文件路径转换为等价路径，拒绝其他设备命名空间。
	if strings.HasPrefix(databasePath, `\\?\`) {
		path := strings.TrimPrefix(databasePath, `\\?\`)
		if len(path) >= 4 && strings.EqualFold(path[:4], `UNC\`) {
			databasePath = `\\` + path[4:]
		} else if isWindowsDrivePath(path) {
			databasePath = path
		} else {
			return "", errors.New("sqlite extended path is invalid")
		}
	}
	uri := url.URL{Scheme: "file"}
	switch {
	case strings.HasPrefix(databasePath, `\\`) || strings.HasPrefix(databasePath, "//"):
		normalized := strings.ReplaceAll(databasePath, `\`, "/")
		serverAndPath := strings.TrimPrefix(normalized, "//")
		parts := strings.SplitN(serverAndPath, "/", 2)
		if len(parts) != 2 || parts[0] == "" || strings.Trim(parts[1], "/") == "" || strings.ContainsAny(parts[0], " \\/?#@") {
			return "", errors.New("sqlite UNC path is invalid")
		}
		uri.Path = "//" + parts[0] + "/" + parts[1]
	case isWindowsDrivePath(databasePath):
		normalized := strings.ReplaceAll(databasePath, `\`, "/")
		uri.Path = "/" + normalized
	case strings.HasPrefix(databasePath, "/"):
		uri.Path = databasePath
	default:
		return "", errors.New("sqlite database path is not absolute")
	}
	query := url.Values{}
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "journal_mode(WAL)")
	uri.RawQuery = query.Encode()
	return uri.String(), nil
}

func isWindowsDrivePath(path string) bool {
	if len(path) < 3 || path[1] != ':' || path[2] != '/' && path[2] != '\\' {
		return false
	}
	drive := path[0]
	return drive >= 'A' && drive <= 'Z' || drive >= 'a' && drive <= 'z'
}

func sanitizedContextError(ctx context.Context, stage string, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("%s: %w", stage, contextErr)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", stage, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", stage, context.DeadlineExceeded)
	}
	return errors.New(stage)
}

type instanceLock interface {
	TryLock() (bool, error)
	Unlock() error
}

func acquireInstanceLock(owner instanceLock) (func() error, error) {
	locked, err := owner.TryLock()
	if err != nil {
		return nil, errors.New("sqlite instance lock failed")
	}
	if !locked {
		return nil, ErrInstanceLocked
	}
	return owner.Unlock, nil
}

func configurePool(db *sql.DB, cfg Config, defaultMax int) {
	maxOpen := cfg.MaxOpenConnections
	if maxOpen <= 0 {
		maxOpen = defaultMax
	}
	maxIdle := cfg.MaxIdleConnections
	if maxIdle < 0 {
		maxIdle = 0
	} else if maxIdle == 0 {
		maxIdle = maxOpen
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
}
