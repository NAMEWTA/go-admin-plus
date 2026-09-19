package authorization_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/account"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/administration"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	administrationmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0020-administration-schema"
	bootstraprecoverymigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0030-bootstrap-recovery"
	sessionprotectionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0040-session-protection"
	accountlifecyclemigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0060-account-lifecycle"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
	reliableruntimemigration "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations/reliable-runtime"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/outbox"
)

const (
	adminHarnessServe   = "GO_ADMIN_IAM_ADMIN_E2E_SERVE"
	adminHarnessProfile = "GO_ADMIN_IAM_ADMIN_E2E_PROFILE"
	adminHarnessReady   = "GO_ADMIN_IAM_ADMIN_E2E_READY_FILE"
	adminHarnessStatic  = "GO_ADMIN_IAM_ADMIN_E2E_STATIC_DIR"
)

type authorizationBrowserLoginFactNoop struct{}

func (authorizationBrowserLoginFactNoop) RecordLoginFact(context.Context, database.Tx, session.LoginFact) error {
	return nil
}

// TestIAMAdministrationBrowserHarnessServer is compiled by source gates and run only by the Lead
// matrix. It serves production handlers and tracked test controls over same-origin HTTPS.
func TestIAMAdministrationBrowserHarnessServer(t *testing.T) {
	if os.Getenv(adminHarnessServe) != "1" {
		t.Skip(adminHarnessServe + " is not enabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	db := openAdministrationBrowserDB(t, ctx, os.Getenv(adminHarnessProfile))
	runner, err := migrations.NewRunner(
		sessionmigration.Provider{},
		administrationmigration.Provider{},
		bootstraprecoverymigration.Provider{},
		sessionprotectionmigration.Provider{},
		accountlifecyclemigration.Provider{},
		reliableruntimemigration.Provider{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(ctx, db); err != nil {
		t.Fatal("browser harness migration failed")
	}
	seedAdministrationBrowserAccounts(t, ctx, db)
	policy, err := config.NewSessionPolicy(2*time.Minute, 8*time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := session.NewService(db, policy, session.WithLoginFactPort(authorizationBrowserLoginFactNoop{}))
	if err != nil {
		t.Fatal(err)
	}
	adminService, err := administration.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	store, err := outbox.NewStore(db, administration.AccountDeletionRequestedTopicSchema())
	if err != nil {
		t.Fatal(err)
	}
	deletions, err := administration.NewDeletionService(db, store)
	if err != nil {
		t.Fatal(err)
	}
	sessionHandler, err := session.NewHTTPHandler(sessions, func(*http.Request) string { return "0123456789abcdef" })
	if err != nil {
		t.Fatal(err)
	}
	adminHandler, err := administration.NewHTTPHandler(
		adminService,
		sessions,
		func(*http.Request) string { return "0123456789abcdef" },
		administration.WithHTTPDeletionService(deletions),
	)
	if err != nil {
		t.Fatal(err)
	}

	var cookieAttributes struct {
		sync.Mutex
		secure, httpOnly, strict bool
	}
	recordCookie := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder := &headerCapture{ResponseWriter: w}
			next.ServeHTTP(recorder, r)
			value := recorder.Header().Get("Set-Cookie")
			if value != "" {
				cookieAttributes.Lock()
				cookieAttributes.secure = strings.Contains(value, "Secure")
				cookieAttributes.httpOnly = strings.Contains(value, "HttpOnly")
				cookieAttributes.strict = strings.Contains(value, "SameSite=Strict")
				cookieAttributes.Unlock()
			}
		})
	}
	shutdown := make(chan struct{})
	var once sync.Once
	mux := http.NewServeMux()
	mux.Handle("/api/iam/session/", http.StripPrefix("/api", recordCookie(sessionHandler)))
	mux.Handle("/api/iam/administration/", http.StripPrefix("/api", recordCookie(adminHandler)))
	mux.HandleFunc("/__test/snapshot", func(w http.ResponseWriter, r *http.Request) {
		var users, roles, menus int
		for query, target := range map[string]*int{`SELECT COUNT(*) FROM iam_accounts`: &users, `SELECT COUNT(*) FROM iam_roles`: &roles, `SELECT COUNT(*) FROM iam_menus`: &menus} {
			if err := db.Bun().QueryRowContext(r.Context(), query).Scan(target); err != nil {
				w.WriteHeader(500)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"users": users, "roles": roles, "menus": menus})
	})
	mux.HandleFunc("/__test/cookie-attributes", func(w http.ResponseWriter, _ *http.Request) {
		cookieAttributes.Lock()
		defer cookieAttributes.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"secure": cookieAttributes.secure, "httpOnly": cookieAttributes.httpOnly, "strict": cookieAttributes.strict})
	})
	mux.HandleFunc("/__test/revoke-role-read", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		if _, err := db.Bun().ExecContext(r.Context(), `DELETE FROM iam_role_permissions WHERE role_id = ? AND permission_code = ?`, "role-system-admin", authorization.PermissionRolesRead); err != nil {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/__test/shutdown", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		w.WriteHeader(204)
		once.Do(func() { close(shutdown) })
	})
	staticRoot, readyFile := os.Getenv(adminHarnessStatic), os.Getenv(adminHarnessReady)
	if info, err := os.Stat(staticRoot); err != nil || !info.IsDir() || readyFile == "" {
		t.Fatal("browser harness paths are invalid")
	}
	mux.Handle("/", http.FileServer(http.Dir(staticRoot)))
	server := httptest.NewUnstartedServer(mux)
	server.StartTLS()
	defer server.Close()
	if err := os.MkdirAll(filepath.Dir(readyFile), 0o700); err != nil {
		t.Fatal("ready directory failed")
	}
	if err := os.WriteFile(readyFile, []byte(server.URL), 0o600); err != nil {
		t.Fatal("ready file failed")
	}
	select {
	case <-shutdown:
	case <-ctx.Done():
		t.Fatal("administration browser harness timed out")
	}
}

type headerCapture struct{ http.ResponseWriter }

func openAdministrationBrowserDB(t *testing.T, ctx context.Context, profile string) *database.Database {
	t.Helper()
	if profile == "sqlite" {
		if os.Getenv(postgresDSNEnvironment) != "" {
			t.Fatal("SQLite browser harness received PostgreSQL material")
		}
		db, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "administration.sqlite3")})
		if err != nil {
			t.Fatal("SQLite harness open failed")
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	if profile != "postgres" {
		t.Fatal("browser harness profile is invalid")
	}
	dsn := os.Getenv(postgresDSNEnvironment)
	if dsn == "" {
		t.Fatal("PostgreSQL browser harness material is required")
	}
	admin, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: dsn})
	if err != nil {
		t.Fatal("PostgreSQL harness administrator failed")
	}
	schema := fmt.Sprintf("t07_browser_%d", time.Now().UnixNano())
	if _, err := admin.SQL().ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		_ = admin.Close()
		t.Fatal("PostgreSQL harness schema failed")
	}
	var db *database.Database
	t.Cleanup(func() {
		if db != nil {
			_ = db.Close()
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.SQL().ExecContext(cleanup, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Error("PostgreSQL harness cleanup failed")
		}
		_ = admin.Close()
	})
	db, err = database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: administrationBrowserPostgresDSN(t, dsn, schema)})
	if err != nil {
		t.Fatal("isolated PostgreSQL harness failed")
	}
	return db
}

func administrationBrowserPostgresDSN(t *testing.T, dsn, schema string) string {
	t.Helper()
	return withSearchPath(t, dsn, schema)
}

func TestAdministrationBrowserPostgresDSNIncludesIsolatedSearchPath(t *testing.T) {
	const schema = "t07_browser_contract"
	value := administrationBrowserPostgresDSN(t, "postgres://localhost/database?sslmode=disable", schema)
	if !strings.Contains(value, "search_path="+schema) || !strings.Contains(value, "sslmode=disable") {
		t.Fatal("browser PostgreSQL DSN lost its isolated search path")
	}
}

func TestAdministrationBrowserDeletionFixtureContract(t *testing.T) {
	ctx := context.Background()
	db := openAdministrationBrowserDB(t, ctx, "sqlite")
	runner, err := migrations.NewRunner(
		sessionmigration.Provider{},
		administrationmigration.Provider{},
		bootstraprecoverymigration.Provider{},
		sessionprotectionmigration.Provider{},
		accountlifecyclemigration.Provider{},
		reliableruntimemigration.Provider{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	seedAdministrationBrowserAccounts(t, ctx, db)
	store, err := outbox.NewStore(db, administration.AccountDeletionRequestedTopicSchema())
	if err != nil {
		t.Fatal(err)
	}
	deletions, err := administration.NewDeletionService(db, store)
	if err != nil {
		t.Fatal(err)
	}
	value, err := deletions.StartDeletion(ctx, adminID, administration.StartDeletion{
		AccountID: "account-ordinary-1", Strategy: administration.DeletionStrategyPurge, PurgeConfirmed: true,
	})
	if err != nil {
		t.Fatalf("browser deletion fixture: %v", err)
	}
	if value.Status != administration.DeletionStatusQueued {
		t.Fatalf("browser deletion status = %q", value.Status)
	}
}

func seedAdministrationBrowserAccounts(t *testing.T, ctx context.Context, db *database.Database) {
	t.Helper()
	repository := account.NewRepository(db.Dialect())
	now := time.Now().UTC()
	for _, value := range []struct{ id, username, password string }{{adminID, "admin", "administrator password"}, {"account-ordinary-1", "ordinary", "ordinary password value"}} {
		hash, err := account.HashPassword(value.password)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
			return repository.Create(ctx, tx, account.Credential{Profile: account.Profile{ID: value.id, Username: value.username, DisplayName: strings.ToUpper(value.username[:1]) + value.username[1:], Email: value.username + "@example.test"}, PasswordHash: hash}, now)
		}); err != nil {
			t.Fatal("browser account seed failed")
		}
	}
	if _, err := db.Bun().ExecContext(ctx, `INSERT INTO iam_account_roles(account_id, role_id) VALUES (?, ?)`, adminID, "role-system-admin"); err != nil {
		t.Fatal("browser role seed failed")
	}
}
