package web_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/app/product"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/bootstrap"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

const (
	serveEnvironment      = "GO_ADMIN_WEB_SHELL_E2E_SERVE"
	profileEnvironment    = "GO_ADMIN_WEB_SHELL_E2E_PROFILE"
	readyEnvironment      = "GO_ADMIN_WEB_SHELL_E2E_READY_FILE"
	staticEnvironment     = "GO_ADMIN_WEB_SHELL_E2E_STATIC_DIR"
	postgresEnvironment   = "GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN"
	administratorPassword = "web browser administrator password"
)

type bootstrapAudit struct{}

func (bootstrapAudit) RecordBootstrap(context.Context, database.Tx, bootstrap.Fact) error { return nil }

// TestProductWebShellHarness serves the production application and compiled Web App on one
// TLS origin. The source gate only compiles it; the required parent-candidate runner owns use.
func TestProductWebShellHarness(t *testing.T) {
	if os.Getenv(serveEnvironment) != "1" {
		if os.Getenv(postgresEnvironment) != "" {
			t.Fatal("compile self-check received PostgreSQL material")
		}
		t.Skip(serveEnvironment + " is not enabled")
	}
	profile := os.Getenv(profileEnvironment)
	staticRoot := os.Getenv(staticEnvironment)
	readyFile := os.Getenv(readyEnvironment)
	if staticRoot == "" || readyFile == "" {
		t.Fatal("product Web harness paths are required")
	}
	if info, err := os.Stat(staticRoot); err != nil || !info.IsDir() {
		t.Fatal("product Web static directory is unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	db, _ := openProductWebDatabase(t, ctx, profile)
	if profile == "postgres" {
		runner, err := product.NewMigrationRunner()
		if err != nil {
			t.Fatal("product Web migration runner failed")
		}
		if _, err := runner.Up(ctx, db); err != nil {
			t.Fatal("explicit PostgreSQL product Web migration failed")
		}
	}
	if err := product.PrepareRuntimeSchema(ctx, db, profile == "sqlite"); err != nil {
		t.Fatal("product Web migration failed")
	}
	policy, err := config.NewSessionPolicy(10*time.Minute, time.Hour, 2*time.Minute)
	if err != nil {
		t.Fatal("product Web session policy failed")
	}
	temporary := t.TempDir()
	runtime, err := product.BuildPrepared(ctx, db, product.Options{
		SessionPolicy: policy, FilesRoot: filepath.Join(temporary, "files"), WorkerOwner: "web-e2e-worker",
		WorkerInterval: 50 * time.Millisecond, AuditRetentionAge: 24 * time.Hour,
	}, false)
	if err != nil {
		t.Fatal("product Web runtime build failed")
	}
	secret, err := bootstrap.ReadSecret(strings.NewReader(administratorPassword))
	if err != nil {
		t.Fatal("product Web bootstrap secret failed")
	}
	bootstrapper, err := bootstrap.NewService(db, bootstrapAudit{}, bootstrap.WithIDGenerator(func() (string, error) {
		return "account-web-browser-e2e-00000001", nil
	}))
	if err != nil {
		t.Fatal("product Web bootstrap service failed")
	}
	if _, err := bootstrapper.Bootstrap(ctx, bootstrap.Command{
		Username: "admin", DisplayName: "Web Administrator", Email: "web-admin@example.test", Secret: secret,
	}); err != nil {
		t.Fatal("product Web bootstrap failed")
	}
	if err := runtime.Application.Start(ctx); err != nil {
		t.Fatal("product Web application start failed")
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		if err := runtime.Application.Stop(stopCtx); err != nil {
			t.Error("product Web application stop failed")
		}
	}()

	shutdown := make(chan struct{})
	var shutdownOnce sync.Once
	mux := http.NewServeMux()
	mux.Handle("/api/", runtime.Application.Handler())
	mux.HandleFunc("/__test/revoke-permission", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Query().Get("code") != "iam.roles.read" {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		result, err := db.Bun().ExecContext(request.Context(), `DELETE FROM iam_role_permissions
			WHERE permission_code = ? AND role_id = (SELECT id FROM iam_roles WHERE role_key = 'system-admin')`, "iam.roles.read")
		if err != nil {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			response.WriteHeader(http.StatusConflict)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/__test/revoke-sessions", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if _, err := db.Bun().ExecContext(request.Context(), `UPDATE iam_sessions SET state = 'revoked', revoked_at = CURRENT_TIMESTAMP WHERE state = 'active'`); err != nil {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/__test/shutdown", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		response.WriteHeader(http.StatusNoContent)
		shutdownOnce.Do(func() { close(shutdown) })
	})
	mux.Handle("/", productWebStaticHandler(staticRoot))

	server := httptest.NewUnstartedServer(mux)
	server.StartTLS()
	defer server.Close()
	if err := os.MkdirAll(filepath.Dir(readyFile), 0o700); err != nil {
		t.Fatal("product Web readiness directory is unavailable")
	}
	if err := os.WriteFile(readyFile, []byte(server.URL), 0o600); err != nil {
		t.Fatal("product Web readiness file is unavailable")
	}
	select {
	case <-shutdown:
	case <-ctx.Done():
		t.Fatal("product Web harness timed out")
	}
}

func productWebStaticHandler(root string) http.Handler {
	files := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		cleaned := filepath.Clean(strings.TrimPrefix(request.URL.Path, "/"))
		candidate := filepath.Join(root, cleaned)
		relative, err := filepath.Rel(root, candidate)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
				files.ServeHTTP(response, request)
				return
			}
		}
		http.ServeFile(response, request, filepath.Join(root, "index.html"))
	})
}

func openProductWebDatabase(t *testing.T, ctx context.Context, profile string) (*database.Database, string) {
	t.Helper()
	if profile == "sqlite" {
		if os.Getenv(postgresEnvironment) != "" {
			t.Fatal("SQLite product Web harness received PostgreSQL material")
		}
		db, err := database.NewProcess().Open(ctx, database.Config{
			Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "product-web.sqlite3"),
		})
		if err != nil {
			t.Fatal("open SQLite product Web database failed")
		}
		t.Cleanup(func() { _ = db.Close() })
		return db, "main"
	}
	if profile != "postgres" {
		t.Fatal("product Web profile is invalid")
	}
	dsn := os.Getenv(postgresEnvironment)
	if dsn == "" {
		t.Fatal("disposable PostgreSQL material is required")
	}
	admin, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: dsn})
	if err != nil {
		t.Fatal("open PostgreSQL product Web administrator failed")
	}
	schema := fmt.Sprintf("web_shell_%d", time.Now().UnixNano())
	if _, err := admin.SQL().ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		_ = admin.Close()
		t.Fatal("create PostgreSQL product Web schema failed")
	}
	isolated, err := productWebPostgresDSN(dsn, schema)
	if err != nil {
		_, _ = admin.SQL().ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		_ = admin.Close()
		t.Fatal("isolate PostgreSQL product Web DSN failed")
	}
	db, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: isolated})
	if err != nil {
		_, _ = admin.SQL().ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		_ = admin.Close()
		t.Fatal("open isolated PostgreSQL product Web database failed")
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error("close isolated PostgreSQL product Web database failed")
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.SQL().ExecContext(cleanup, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Error("drop PostgreSQL product Web schema failed")
		}
		if err := admin.Close(); err != nil {
			t.Error("close PostgreSQL product Web administrator failed")
		}
	})
	return db, schema
}

func productWebPostgresDSN(dsn, schema string) (string, error) {
	if !strings.HasPrefix(schema, "web_shell_") || strings.Trim(strings.TrimPrefix(schema, "web_shell_"), "0123456789") != "" {
		return "", errors.New("invalid product Web schema")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Host == "" || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", errors.New("invalid PostgreSQL URL")
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func TestProductWebPostgresDSNPreservesIsolation(t *testing.T) {
	value, err := productWebPostgresDSN("postgres://localhost/database?sslmode=disable", "web_shell_1234")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Query().Get("search_path") != "web_shell_1234" || parsed.Query().Get("sslmode") != "disable" {
		t.Fatal("product Web DSN lost isolation or existing parameters")
	}
	for _, invalid := range []struct{ dsn, schema string }{
		{"host=localhost dbname=database", "web_shell_1234"},
		{"https://localhost/database", "web_shell_1234"},
		{"postgres://localhost/database", "public"},
	} {
		if _, err := productWebPostgresDSN(invalid.dsn, invalid.schema); err == nil {
			t.Fatal("invalid product Web isolation input was accepted")
		}
	}
}
