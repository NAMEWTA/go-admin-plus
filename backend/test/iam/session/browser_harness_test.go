package session_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/account"
	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	sessionprotectionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0040-session-protection"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
)

const (
	browserHarnessServeEnv    = "GO_ADMIN_IAM_E2E_SERVE"
	browserHarnessProfileEnv  = "GO_ADMIN_IAM_E2E_PROFILE"
	browserHarnessReadyEnv    = "GO_ADMIN_IAM_E2E_READY_FILE"
	browserHarnessStaticEnv   = "GO_ADMIN_IAM_E2E_STATIC_DIR"
	browserHarnessPostgresEnv = "GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN"
)

type browserLoginFactNoop struct{}

func (browserLoginFactNoop) RecordLoginFact(context.Context, database.Tx, session.LoginFact) error {
	return nil
}

type harnessClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *harnessClock) current() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *harnessClock) advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

// TestIAMBrowserHarnessServer is a tracked required-E2E host. The source gate compiles it and
// skips; the Lead-owned runner opts in and drives it through Chromium over same-origin HTTPS.
func TestIAMBrowserHarnessServer(t *testing.T) {
	if os.Getenv(browserHarnessServeEnv) != "1" {
		if _, present := os.LookupEnv(browserHarnessPostgresEnv); present {
			t.Fatal("browser harness compile self-check received PostgreSQL connection material")
		}
		t.Skip(browserHarnessServeEnv + " is not enabled")
	}
	profile := os.Getenv(browserHarnessProfileEnv)
	_, hasPostgresMaterial := os.LookupEnv(browserHarnessPostgresEnv)
	if profile == "sqlite" && hasPostgresMaterial {
		t.Fatal("SQLite browser harness received PostgreSQL connection material")
	}
	if profile == "postgres" && !hasPostgresMaterial {
		t.Fatal("PostgreSQL browser harness connection material is required")
	}
	staticRoot := os.Getenv(browserHarnessStaticEnv)
	readyFile := os.Getenv(browserHarnessReadyEnv)
	if staticRoot == "" || readyFile == "" {
		t.Fatal("browser harness paths are required")
	}
	if info, err := os.Stat(staticRoot); err != nil || !info.IsDir() {
		t.Fatal("browser harness static directory is unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := openBrowserHarnessDatabase(t, ctx, profile)
	runner, err := migrations.NewRunner(sessionmigration.Provider{}, sessionprotectionmigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(ctx, db); err != nil {
		t.Fatal("browser harness migration failed")
	}
	seedBrowserHarnessAccount(t, ctx, db)

	clock := &harnessClock{now: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)}
	policy, err := config.NewSessionPolicy(2*time.Minute, 5*time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service, err := session.NewService(db, policy, session.WithClock(clock.current), session.WithLoginFactPort(browserLoginFactNoop{}))
	if err != nil {
		t.Fatal(err)
	}
	api, err := session.NewHTTPHandler(service, func(*http.Request) string { return "0123456789abcdef" })
	if err != nil {
		t.Fatal(err)
	}

	var failLogout atomic.Bool
	shutdown := make(chan struct{})
	var shutdownOnce sync.Once
	mux := http.NewServeMux()
	mux.Handle("/api/", http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if failLogout.Load() && request.URL.Path == "/api/iam/session/logout" {
			response.Header().Set("Content-Type", "application/problem+json")
			response.Header().Set("Cache-Control", "no-store")
			response.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"type": "urn:go-admin-plus:problem:internal", "title": "Internal server error",
				"status": 500, "category": "internal", "code": "INTERNAL_ERROR", "traceId": "0123456789abcdef",
			})
			return
		}
		http.StripPrefix("/api", api).ServeHTTP(response, request)
	}))
	mux.HandleFunc("/__test/advance", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		seconds, err := strconv.ParseInt(request.URL.Query().Get("seconds"), 10, 16)
		if err != nil || seconds < 1 || seconds > 600 {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		clock.advance(time.Duration(seconds) * time.Second)
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/__test/logout-failure", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		enabled, err := strconv.ParseBool(request.URL.Query().Get("enabled"))
		if err != nil {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		failLogout.Store(enabled)
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/__test/snapshot", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var displayName string
		var passwordHash string
		var activeSessions int
		if err := db.Bun().QueryRowContext(request.Context(), `SELECT display_name, password_hash FROM iam_accounts WHERE id = ?`, "account-00000001").Scan(&displayName, &passwordHash); err != nil {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		if err := db.Bun().QueryRowContext(request.Context(), `SELECT COUNT(*) FROM iam_sessions WHERE state = 'active'`).Scan(&activeSessions); err != nil {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"displayName": displayName, "activeSessions": activeSessions,
			"replacementPasswordActive": account.VerifyPassword(passwordHash, "replacement password value"),
		})
	})
	mux.HandleFunc("/__test/shutdown", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		response.WriteHeader(http.StatusNoContent)
		shutdownOnce.Do(func() { close(shutdown) })
	})
	mux.Handle("/", http.FileServer(http.Dir(staticRoot)))

	server := httptest.NewUnstartedServer(mux)
	server.StartTLS()
	defer server.Close()
	if err := os.MkdirAll(filepath.Dir(readyFile), 0o700); err != nil {
		t.Fatal("browser harness readiness directory is unavailable")
	}
	if err := os.WriteFile(readyFile, []byte(server.URL), 0o600); err != nil {
		t.Fatal("browser harness readiness file is unavailable")
	}
	select {
	case <-shutdown:
	case <-ctx.Done():
		t.Fatal("browser harness timed out")
	}
}

func openBrowserHarnessDatabase(t *testing.T, ctx context.Context, profile string) *database.Database {
	t.Helper()
	switch profile {
	case "sqlite":
		db, err := database.NewProcess().Open(ctx, database.Config{
			Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "iam-browser.sqlite3"),
		})
		if err != nil {
			t.Fatal("open SQLite browser harness database failed")
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	case "postgres":
		dsn := os.Getenv(browserHarnessPostgresEnv)
		if dsn == "" {
			t.Fatal("disposable PostgreSQL environment is required")
		}
		admin, err := database.NewProcess().Open(ctx, database.Config{
			Profile: config.ProfileServerPostgres, PostgresDSN: dsn, MaxOpenConnections: 2, MaxIdleConnections: 2,
		})
		if err != nil {
			t.Fatal("open PostgreSQL browser harness administrator failed")
		}
		schema := fmt.Sprintf("iam_browser_%d", time.Now().UnixNano())
		if _, err := admin.SQL().ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
			if closeErr := admin.Close(); closeErr != nil {
				t.Error("close PostgreSQL browser harness administrator failed")
			}
			t.Fatal("create PostgreSQL browser harness schema failed")
		}
		isolatedDSN, err := isolatedIAMBrowserDSN(dsn, schema)
		if err != nil {
			cleanupPostgresBrowserHarness(t, admin, schema)
			t.Fatal("parse PostgreSQL browser harness connection failed")
		}
		db, err := database.NewProcess().Open(ctx, database.Config{
			Profile: config.ProfileServerPostgres, PostgresDSN: isolatedDSN, MaxOpenConnections: 4, MaxIdleConnections: 4,
		})
		if err != nil {
			cleanupPostgresBrowserHarness(t, admin, schema)
			t.Fatal("open isolated PostgreSQL browser harness database failed")
		}
		t.Cleanup(func() {
			if err := db.Close(); err != nil {
				t.Error("close isolated PostgreSQL browser harness database failed")
			}
			cleanupPostgresBrowserHarness(t, admin, schema)
		})
		return db
	default:
		t.Fatal("browser harness profile is invalid")
		return nil
	}
}

func isolatedIAMBrowserDSN(dsn, schema string) (string, error) {
	suffix := strings.TrimPrefix(schema, "iam_browser_")
	if suffix == "" || strings.Trim(suffix, "0123456789") != "" {
		return "", fmt.Errorf("invalid IAM browser schema")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Host == "" || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", fmt.Errorf("invalid PostgreSQL URL")
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func TestIsolatedIAMBrowserDSNPreservesParameters(t *testing.T) {
	value, err := isolatedIAMBrowserDSN("postgres://localhost/database?host=%2Ftmp%2Fpostgres&port=5432&sslmode=disable", "iam_browser_1234")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Query().Get("search_path") != "iam_browser_1234" || parsed.Query().Get("host") != "/tmp/postgres" || parsed.Query().Get("sslmode") != "disable" {
		t.Fatal("isolated IAM browser DSN lost its search path or existing parameters")
	}
	for _, invalid := range []struct{ dsn, schema string }{
		{"host=localhost dbname=database", "iam_browser_1234"},
		{"postgres://localhost/database", "public"},
		{"https://localhost/database", "iam_browser_1234"},
	} {
		if _, err := isolatedIAMBrowserDSN(invalid.dsn, invalid.schema); err == nil {
			t.Fatal("invalid IAM browser isolation input was accepted")
		}
	}
}

func cleanupPostgresBrowserHarness(t *testing.T, admin *database.Database, schema string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := admin.SQL().ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
		t.Error("drop PostgreSQL browser harness schema failed")
	}
	if err := admin.Close(); err != nil {
		t.Error("close PostgreSQL browser harness administrator failed")
	}
}

func seedBrowserHarnessAccount(t *testing.T, ctx context.Context, db *database.Database) {
	t.Helper()
	hash, err := account.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	repository := account.NewRepository(db.Dialect())
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if err := db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		return repository.Create(ctx, tx, account.Credential{
			Profile: account.Profile{
				ID: "account-00000001", Username: "admin", DisplayName: "Administrator", Email: "admin@example.test",
			},
			PasswordHash: hash,
		}, now)
	}); err != nil {
		t.Fatal("seed browser harness account failed")
	}
}
