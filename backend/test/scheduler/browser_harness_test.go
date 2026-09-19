package scheduler_test

import (
	"context"
	"encoding/json"
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

	"github.com/NAMEWTA/go-admin-plus/backend/internal/app/adapters"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/account"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/administration"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	administrationmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0020-administration-schema"
	sessionprotectionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0040-session-protection"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/scheduler"
	schedulermigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/scheduler/migrations"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/coordination"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
	reliablemigration "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations/reliable-runtime"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/outbox"
	"github.com/google/uuid"
)

const (
	schedulerHarnessServe   = "GO_ADMIN_SCHEDULER_E2E_SERVE"
	schedulerHarnessProfile = "GO_ADMIN_SCHEDULER_E2E_PROFILE"
	schedulerHarnessReady   = "GO_ADMIN_SCHEDULER_E2E_READY_FILE"
	schedulerHarnessStatic  = "GO_ADMIN_SCHEDULER_E2E_STATIC_DIR"
	schedulerPostgresDSN    = "GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN"
	schedulerAdminID        = "account-scheduler-admin-001"
	schedulerOwner          = "scheduler-e2e-owner"
)

type browserParameters struct {
	Key  string `json:"key"`
	Fail bool   `json:"fail"`
}
type browserClock struct {
	sync.Mutex
	value time.Time
}

func (clock *browserClock) Now() time.Time { clock.Lock(); defer clock.Unlock(); return clock.value }
func (clock *browserClock) advance() {
	clock.Lock()
	clock.value = clock.value.AddDate(0, 0, 7)
	clock.Unlock()
}

type schedulerLoginFactNoop struct{}

func (schedulerLoginFactNoop) RecordLoginFact(context.Context, database.Tx, session.LoginFact) error {
	return nil
}

// TestSchedulerBrowserHarnessServer is compiled by source gates. Only the required candidate E2E
// sets the serve flag and runs this HTTPS host for both isolated database profiles.
func TestSchedulerBrowserHarnessServer(t *testing.T) {
	if os.Getenv(schedulerHarnessServe) != "1" {
		t.Skip(schedulerHarnessServe + " is not enabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	db := openSchedulerBrowserDB(t, ctx, os.Getenv(schedulerHarnessProfile))
	runner, err := migrations.NewRunner(reliablemigration.Provider{}, sessionmigration.Provider{}, administrationmigration.Provider{}, sessionprotectionmigration.Provider{}, schedulermigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(ctx, db); err != nil {
		t.Fatal("scheduler browser migrations failed")
	}
	if _, err := db.Bun().ExecContext(ctx, `CREATE TABLE scheduler_browser_effects(effect_key TEXT PRIMARY KEY)`); err != nil {
		t.Fatal("scheduler effect fixture failed")
	}
	payloadType := "BLOB"
	if db.Dialect() == database.DialectPostgres {
		payloadType = "BYTEA"
	}
	if _, err := db.Bun().ExecContext(ctx, `CREATE TABLE scheduler_browser_events(business_key TEXT PRIMARY KEY, payload `+payloadType+` NOT NULL)`); err != nil {
		t.Fatal("scheduler event fixture failed")
	}
	seedSchedulerBrowserIdentity(t, ctx, db)
	capabilities, err := authorization.NewCapabilityRegistry(db)
	if err != nil || scheduler.RegisterCapabilities(ctx, capabilities) != nil {
		t.Fatal("scheduler capability registration failed")
	}
	policy, err := config.NewSessionPolicy(2*time.Minute, 8*time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := session.NewService(db, policy, session.WithLoginFactPort(schedulerLoginFactNoop{}))
	if err != nil {
		t.Fatal(err)
	}
	clock := &browserClock{value: time.Date(2026, 8, 27, 10, 0, 30, 0, time.UTC)}
	store, err := outbox.NewStore(db, outbox.TopicSchema{Topic: "scheduler.browser", Payload: []outbox.PayloadFieldSchema{{Name: "outcome", Kind: outbox.PayloadString, Required: true, AllowedStrings: []string{"ok", "fail"}}}, BusinessKey: outbox.BusinessKeySchema{Prefix: "scheduler", MinParts: 1, MaxParts: 1}})
	if err != nil {
		t.Fatal(err)
	}
	registration, err := scheduler.NewTaskRegistration("browser.effect", "Browser effect", []scheduler.ParameterField{{Name: "key", Label: "Key", Kind: scheduler.ParameterString, Required: true}, {Name: "fail", Label: "Fail after staging", Kind: scheduler.ParameterBoolean, Required: true}}, func(ctx context.Context, tx database.Tx, parameters browserParameters) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO scheduler_browser_effects(effect_key) VALUES (?)`, parameters.Key); err != nil {
			return err
		}
		payload := "ok"
		if parameters.Fail {
			payload = "fail"
		}
		if _, err := store.Enqueue(ctx, tx, outbox.Event{ID: uuid.NewString(), Topic: "scheduler.browser", BusinessKey: "scheduler:" + parameters.Key, Payload: []byte(`{"outcome":"` + payload + `"}`), OccurredAt: clock.Now()}); err != nil {
			return err
		}
		if parameters.Fail {
			return scheduler.NewTaskFailure("browser_expected_failure")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := scheduler.NewRegistry(registration)
	if err != nil {
		t.Fatal(err)
	}
	authorizationAdapters, err := adapters.NewAuthorization(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err := scheduler.NewService(db, authorizationAdapters.Scheduler(), registry, clock)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := scheduler.NewExecutor(db, registry, scheduler.ExecutorConfig{Owner: schedulerOwner, BatchSize: 20, TaskTimeout: 5 * time.Second, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := outbox.NewTransactionalConsumer("scheduler", "scheduler-browser", []string{"scheduler_browser_events"}, outbox.Mutation{Operation: outbox.OperationInsert, Table: "scheduler_browser_events", Values: []outbox.ColumnBinding{{Column: "business_key", Field: outbox.FieldBusinessKey}, {Column: "payload", Field: outbox.FieldPayload}}, ExpectExactly: 1})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := outbox.NewDispatcher(store, outbox.DispatcherConfig{Owner: schedulerOwner, LeaseDuration: time.Minute, RetryDelay: time.Minute, BatchSize: 20, Now: clock.Now}, map[string]outbox.TransactionalConsumer{"scheduler.browser": consumer})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := coordination.Acquire(ctx, db, coordination.Config{Owner: schedulerOwner})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close(context.Background()) }()

	sessionHandler, err := session.NewHTTPHandler(sessions, func(*http.Request) string { return "0123456789abcdef" })
	if err != nil {
		t.Fatal(err)
	}
	sessionAdapters, err := adapters.NewSession(sessions)
	if err != nil {
		t.Fatal(err)
	}
	schedulerHandler, err := scheduler.NewHTTPHandler(service, sessionAdapters.Scheduler(), func(*http.Request) string { return "0123456789abcdef" })
	if err != nil {
		t.Fatal(err)
	}
	administrationService, err := administration.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	administrationHandler, err := administration.NewHTTPHandler(administrationService, sessions, func(*http.Request) string { return "0123456789abcdef" })
	if err != nil {
		t.Fatal(err)
	}

	shutdown := make(chan struct{})
	var once sync.Once
	mux := http.NewServeMux()
	mux.Handle("/api/iam/session/", http.StripPrefix("/api", sessionHandler))
	mux.Handle("/api/iam/administration/", http.StripPrefix("/api", administrationHandler))
	mux.Handle("/api/scheduler/", http.StripPrefix("/api", schedulerHandler))
	mux.HandleFunc("/__test/run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		clock.advance()
		executed, executionErr := executor.RunOnce(r.Context(), lease)
		dispatched, dispatchErr := dispatcher.RunOnce(r.Context(), lease, clock.Now())
		if executionErr != nil || dispatchErr != nil {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"triggered": executed.Triggered, "succeeded": executed.Succeeded, "failed": executed.Failed, "delivered": dispatched.Delivered})
	})
	mux.HandleFunc("/__test/snapshot", func(w http.ResponseWriter, r *http.Request) {
		result := map[string]int{}
		for key, table := range map[string]string{"definitions": "scheduler_definitions", "executions": "scheduler_executions", "effects": "scheduler_browser_effects", "events": "scheduler_browser_events", "outbox": "reliable_outbox"} {
			var count int
			if err := db.Bun().QueryRowContext(r.Context(), `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
				w.WriteHeader(500)
				return
			}
			result[key] = count
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
	mux.HandleFunc("/__test/contender", func(w http.ResponseWriter, r *http.Request) {
		candidate, err := coordination.Acquire(r.Context(), db, coordination.Config{Owner: schedulerOwner})
		if candidate != nil {
			_ = candidate.Close(context.Background())
		}
		if !errors.Is(err, coordination.ErrNotLeader) {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/__test/scope", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Scope string `json:"scope"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body) != nil || body.Scope != "self" && body.Scope != "all" {
			w.WriteHeader(400)
			return
		}
		if _, err := db.Bun().ExecContext(r.Context(), `UPDATE iam_roles SET data_scope = ? WHERE id = ?`, body.Scope, "role-system-admin"); err != nil {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/__test/revoke-read", func(w http.ResponseWriter, r *http.Request) {
		if _, err := db.Bun().ExecContext(r.Context(), `DELETE FROM iam_role_permissions WHERE role_id = ? AND permission_code = ?`, "role-system-admin", scheduler.PermissionDefinitionsRead); err != nil {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/__test/revoke-session", func(w http.ResponseWriter, r *http.Request) {
		if err := sessions.RevokeAccount(r.Context(), schedulerAdminID); err != nil {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/__test/shutdown", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204); once.Do(func() { close(shutdown) }) })
	staticRoot, readyFile := os.Getenv(schedulerHarnessStatic), os.Getenv(schedulerHarnessReady)
	if info, err := os.Stat(staticRoot); err != nil || !info.IsDir() || readyFile == "" {
		t.Fatal("scheduler browser harness paths invalid")
	}
	mux.Handle("/", http.FileServer(http.Dir(staticRoot)))
	server := httptest.NewUnstartedServer(mux)
	server.StartTLS()
	defer server.Close()
	if err := os.MkdirAll(filepath.Dir(readyFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readyFile, []byte(server.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-shutdown:
	case <-ctx.Done():
		t.Fatal("scheduler browser harness timed out")
	}
}

func seedSchedulerBrowserIdentity(t *testing.T, ctx context.Context, db *database.Database) {
	t.Helper()
	hash, err := account.HashPassword("scheduler administrator password")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		return account.NewRepository(db.Dialect()).Create(ctx, tx, account.Credential{Profile: account.Profile{ID: schedulerAdminID, Username: "scheduler-admin", DisplayName: "Scheduler Administrator", Email: "scheduler-admin@example.test"}, PasswordHash: hash}, time.Now().UTC())
	}); err != nil {
		t.Fatal("scheduler account seed failed")
	}
	if _, err := db.Bun().ExecContext(ctx, `INSERT INTO iam_account_roles(account_id, role_id) VALUES (?, ?)`, schedulerAdminID, "role-system-admin"); err != nil {
		t.Fatal("scheduler role seed failed")
	}
}

func openSchedulerBrowserDB(t *testing.T, ctx context.Context, profile string) *database.Database {
	t.Helper()
	if profile == "sqlite" {
		if os.Getenv(schedulerPostgresDSN) != "" {
			t.Fatal("SQLite harness received PostgreSQL material")
		}
		db, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "scheduler.sqlite3")})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	if profile != "postgres" {
		t.Fatal("scheduler browser profile invalid")
	}
	dsn := os.Getenv(schedulerPostgresDSN)
	if dsn == "" {
		t.Fatal("scheduler PostgreSQL material required")
	}
	admin, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: dsn})
	if err != nil {
		t.Fatal("scheduler PostgreSQL administrator failed")
	}
	schema := "t12_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Bun().ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		_ = admin.Close()
		t.Fatal("scheduler PostgreSQL schema failed")
	}
	isolatedDSN, err := isolatedSchedulerBrowserDSN(dsn, schema)
	if err != nil {
		t.Fatal("scheduler PostgreSQL DSN invalid")
	}
	db, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: isolatedDSN, MaxOpenConnections: 8, MaxIdleConnections: 4})
	if err != nil {
		t.Fatal("scheduler isolated PostgreSQL failed")
	}
	t.Cleanup(func() {
		_ = db.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Bun().ExecContext(cleanup, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Error("scheduler PostgreSQL cleanup failed")
		}
		_ = admin.Close()
	})
	return db
}

func isolatedSchedulerBrowserDSN(dsn, schema string) (string, error) {
	suffix := strings.TrimPrefix(schema, "t12_")
	if len(suffix) != 32 || strings.Trim(suffix, "0123456789abcdef") != "" {
		return "", fmt.Errorf("invalid Scheduler browser schema")
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

func TestIsolatedSchedulerBrowserDSNPreservesParameters(t *testing.T) {
	const schema = "t12_0123456789abcdef0123456789abcdef"
	value, err := isolatedSchedulerBrowserDSN("postgres://localhost/database?host=%2Ftmp%2Fpostgres&sslmode=disable", schema)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Query().Get("search_path") != schema || parsed.Query().Get("host") != "/tmp/postgres" || parsed.Query().Get("sslmode") != "disable" {
		t.Fatal("isolated Scheduler browser DSN lost its search path or existing parameters")
	}
	if _, err := isolatedSchedulerBrowserDSN("postgres://localhost/database", "public"); err == nil {
		t.Fatal("invalid Scheduler browser schema was accepted")
	}
}
