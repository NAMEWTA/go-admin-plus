package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/app/adapters"
	audit "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/audit"
	auditmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/audit/migrations/0011-audit"
	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	administrationmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0020-administration-schema"
	sessionprotectionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0040-session-protection"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	reliablemigration "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations/reliable-runtime"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/outbox"
)

const (
	auditE2EServeEnv        = "GO_ADMIN_AUDIT_E2E_SERVE"
	auditE2EProfileEnv      = "GO_ADMIN_AUDIT_E2E_PROFILE"
	auditE2EReadyEnv        = "GO_ADMIN_AUDIT_E2E_READY_FILE"
	auditE2EStaticEnv       = "GO_ADMIN_AUDIT_E2E_STATIC_DIR"
	auditUIInitialFacts     = 3
	auditUIPostCleanupFacts = 3
)

var (
	auditUIFixtureTime   = time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	auditUIOperationTime = auditUIFixtureTime.Add(-60 * 24 * time.Hour)
	auditUICleanupBefore = time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
)

type auditHarnessClock struct {
	mu  sync.RWMutex
	now time.Time
}

type auditHarnessRotationProof struct {
	mu                      sync.RWMutex
	replacementCookie, csrf bool
}

func (proof *auditHarnessRotationProof) observe(header http.Header) {
	proof.mu.Lock()
	proof.replacementCookie = proof.replacementCookie || header.Get("Set-Cookie") != ""
	proof.csrf = proof.csrf || header.Get("X-CSRF-Token") != ""
	proof.mu.Unlock()
}

func (proof *auditHarnessRotationProof) snapshot() (bool, bool) {
	proof.mu.RLock()
	defer proof.mu.RUnlock()
	return proof.replacementCookie, proof.csrf
}

type auditHarnessProofWriter struct {
	http.ResponseWriter
	proof *auditHarnessRotationProof
}

func (writer auditHarnessProofWriter) WriteHeader(status int) {
	writer.proof.observe(writer.Header())
	writer.ResponseWriter.WriteHeader(status)
}

func (writer auditHarnessProofWriter) Write(body []byte) (int, error) {
	writer.proof.observe(writer.Header())
	return writer.ResponseWriter.Write(body)
}

func (clock *auditHarnessClock) current() time.Time {
	clock.mu.RLock()
	defer clock.mu.RUnlock()
	return clock.now
}

func (clock *auditHarnessClock) advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

func TestAuditUIFixtureCleanupDeletesOnlyExpiredOperation(t *testing.T) {
	const minimumAge = 30 * 24 * time.Hour
	cutoff := auditUIFixtureTime.Add(31 * time.Minute).Add(-minimumAge)
	if !auditUIOperationTime.Before(auditUICleanupBefore) || auditUICleanupBefore.After(cutoff) {
		t.Fatalf("invalid Audit cleanup fixture window: operation=%s before=%s cutoff=%s", auditUIOperationTime, auditUICleanupBefore, cutoff)
	}

	ctx := context.Background()
	db := openSQLite(t)
	migrate(t, db, reliablemigration.Provider{}, sessionmigration.Provider{}, administrationmigration.Provider{}, sessionprotectionmigration.Provider{}, auditmigration.Provider{})
	createAuditIAMFixture(t, db, auditUIFixtureTime)
	loginRecorder, err := audit.NewLoginRecorder(db)
	if err != nil {
		t.Fatal(err)
	}
	loginFacts := adapters.NewLoginFact(loginRecorder)
	policy, err := config.NewSessionPolicy(time.Hour, 8*time.Hour, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := session.NewService(db, policy, session.WithClock(func() time.Time { return auditUIFixtureTime }), session.WithLoginFactPort(loginFacts))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Login(ctx, "missing", "incorrect password"); !errors.Is(err, session.ErrCredentials) {
		t.Fatal("failed login fixture was not recorded")
	}
	if _, err := sessions.Login(ctx, "admin", "correct horse battery"); err != nil {
		t.Fatal("successful login fixture was not recorded")
	}
	store := newAuditStore(t, db)
	enqueue(t, db, store, outbox.Event{ID: "audit-ui-window-001", Topic: audit.TopicOperationUpdated, BusinessKey: "resource:files:ui-record-revision-2:account-00000001", Payload: []byte(`{"source":"web"}`), OccurredAt: auditUIOperationTime})
	dispatch(t, db, store, mustConsumers(t), auditUIFixtureTime)
	if count := countAuditRows(t, db, "audit_facts"); count != auditUIInitialFacts {
		t.Fatalf("initial Audit fixture count = %d", count)
	}
	service := mustServiceWithPolicy(t, db, allowAll{}, audit.RetentionPolicy{MinimumAge: minimumAge, CleanupLimit: 10, Now: func() time.Time { return auditUIFixtureTime.Add(31 * time.Minute) }})
	result, err := service.Cleanup(ctx, audit.Principal{ID: "auditor-00000001"}, audit.CleanupCommand{Before: auditUICleanupBefore, Confirmation: audit.CleanupConfirmation})
	if err != nil || result.Deleted != 1 || result.MoreEligible {
		t.Fatalf("cleanup result = %#v, %v", result, err)
	}
	if count := countAuditRows(t, db, "audit_facts"); count != auditUIPostCleanupFacts {
		t.Fatalf("post-cleanup Audit fixture count = %d", count)
	}
	page, err := service.List(ctx, audit.Principal{ID: "auditor-00000001"}, audit.Filter{Page: 1, PageSize: 20})
	if err != nil || len(page.Records) != auditUIPostCleanupFacts {
		t.Fatalf("remaining Audit fixture = %#v, %v", page, err)
	}
	for _, fact := range page.Records {
		if fact.Kind != audit.KindLogin && (fact.Subject != "audit:retention" || fact.Operation == nil || *fact.Operation != "CleanupAudit") {
			t.Fatalf("cleanup retained non-login fact: %#v", fact)
		}
	}
}

// TestAuditUIHarnessServer is the tracked required-E2E host. Source gates compile it and skip;
// the Lead-owned runner opts in for both SQLite and PostgreSQL and drives the actual Web client
// and controller against this real Outbox-to-Audit process.
func TestAuditUIHarnessServer(t *testing.T) {
	if os.Getenv(auditE2EServeEnv) != "1" {
		if _, present := os.LookupEnv(auditPostgresEnv); present {
			t.Fatal("Audit UI harness compile self-check received PostgreSQL connection material")
		}
		t.Skip(auditE2EServeEnv + " is not enabled")
	}
	profile := os.Getenv(auditE2EProfileEnv)
	readyFile := os.Getenv(auditE2EReadyEnv)
	staticRoot := os.Getenv(auditE2EStaticEnv)
	if readyFile == "" || staticRoot == "" {
		t.Fatal("Audit UI harness paths are required")
	}
	if info, err := os.Stat(staticRoot); err != nil || !info.IsDir() {
		t.Fatal("Audit UI harness static directory is unavailable")
	}
	_, hasPostgres := os.LookupEnv(auditPostgresEnv)
	if profile == "sqlite" && hasPostgres {
		t.Fatal("SQLite Audit UI harness received PostgreSQL connection material")
	}
	if profile == "postgres" && !hasPostgres {
		t.Fatal("PostgreSQL Audit UI harness connection material is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := openAuditUIHarnessDatabase(t, ctx, profile)
	migrate(t, db, reliablemigration.Provider{}, sessionmigration.Provider{}, administrationmigration.Provider{}, sessionprotectionmigration.Provider{}, auditmigration.Provider{})
	store := newAuditStore(t, db)
	fixtureTime := auditUIFixtureTime
	clock := &auditHarnessClock{now: fixtureTime}
	createAuditIAMFixture(t, db, fixtureTime)
	loginRecorder, err := audit.NewLoginRecorder(db)
	if err != nil {
		t.Fatal(err)
	}
	loginFacts := adapters.NewLoginFact(loginRecorder)
	policy, err := config.NewSessionPolicy(time.Hour, 8*time.Hour, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := session.NewService(db, policy, session.WithClock(clock.current), session.WithLoginFactPort(loginFacts))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Login(ctx, "missing", "incorrect password"); !errors.Is(err, session.ErrCredentials) {
		t.Fatal("seed failed login fact failed")
	}
	enqueue(t, db, store, outbox.Event{ID: "audit-ui-event-001", Topic: audit.TopicOperationUpdated, BusinessKey: "resource:files:ui-record-revision-2:account-00000001", Payload: []byte(`{"source":"web"}`), OccurredAt: auditUIOperationTime})
	dispatch(t, db, store, mustConsumers(t), fixtureTime)

	authorizationAdapters, err := adapters.NewAuthorization(db)
	if err != nil {
		t.Fatal(err)
	}
	service := mustServiceWithPolicy(t, db, authorizationAdapters.Audit(), audit.RetentionPolicy{MinimumAge: 30 * 24 * time.Hour, CleanupLimit: 10, Now: clock.current})
	sessionAdapters, err := adapters.NewSession(sessions)
	if err != nil {
		t.Fatal(err)
	}
	auditAPI, err := audit.NewHTTPHandler(service, sessionAdapters.Audit(), func(*http.Request) string { return "0123456789abcdef" })
	if err != nil {
		t.Fatal(err)
	}
	sessionAPI, err := session.NewHTTPHandler(sessions, func(*http.Request) string { return "0123456789abcdef" })
	if err != nil {
		t.Fatal(err)
	}

	shutdown := make(chan struct{})
	var shutdownOnce sync.Once
	rotationProof := &auditHarnessRotationProof{}
	mux := http.NewServeMux()
	api := http.NewServeMux()
	api.Handle("/audit/", http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		auditAPI.ServeHTTP(auditHarnessProofWriter{ResponseWriter: response, proof: rotationProof}, request)
	}))
	api.Handle("/iam/", sessionAPI)
	mux.Handle("/api/", http.StripPrefix("/api", api))
	mux.HandleFunc("/__test/snapshot", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var count int
		if err := db.Bun().QueryRowContext(request.Context(), "SELECT COUNT(*) FROM audit_facts").Scan(&count); err != nil {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]int{"count": count})
	})
	mux.HandleFunc("/__test/advance-session", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		clock.advance(31 * time.Minute)
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/__test/session-state", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var active, rotated int
		if err := db.Bun().QueryRowContext(request.Context(), `SELECT
			COALESCE(SUM(CASE WHEN state = 'active' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'rotated' THEN 1 ELSE 0 END), 0)
			FROM iam_sessions`).Scan(&active, &rotated); err != nil {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		replacementCookie, csrf := rotationProof.snapshot()
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{"active": active, "rotated": rotated, "replacementCookie": replacementCookie, "csrf": csrf})
	})
	mux.HandleFunc("/__test/audit-permission", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		enabled := request.URL.Query().Get("enabled")
		var err error
		if enabled == "false" {
			_, err = db.Bun().ExecContext(request.Context(), `DELETE FROM iam_role_permissions WHERE role_id = ? AND permission_code = ?`, "role-system-admin", audit.PermissionRead)
		} else if enabled == "true" {
			_, err = db.Bun().ExecContext(request.Context(), `INSERT INTO iam_role_permissions(role_id, permission_code) VALUES (?, ?)`, "role-system-admin", audit.PermissionRead)
		} else {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		if err != nil {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/__test/revoke-sessions", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if _, err := db.Bun().ExecContext(request.Context(), `UPDATE iam_sessions SET state = 'revoked', revoked_at = ? WHERE state = 'active'`, clock.current()); err != nil {
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
	mux.Handle("/", http.FileServer(http.Dir(staticRoot)))
	server := httptest.NewUnstartedServer(mux)
	server.StartTLS()
	defer server.Close()
	if err := os.MkdirAll(filepath.Dir(readyFile), 0o700); err != nil {
		t.Fatal("Audit UI harness readiness directory is unavailable")
	}
	if err := os.WriteFile(readyFile, []byte(server.URL), 0o600); err != nil {
		t.Fatal("Audit UI harness readiness file is unavailable")
	}
	select {
	case <-shutdown:
	case <-ctx.Done():
		t.Fatal("Audit UI harness timed out")
	}
}

func openAuditUIHarnessDatabase(t *testing.T, ctx context.Context, profile string) *database.Database {
	t.Helper()
	switch profile {
	case "sqlite":
		db, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "audit-ui.sqlite3")})
		if err != nil {
			t.Fatal("open SQLite Audit UI harness failed")
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	case "postgres":
		return openIsolatedAuditPostgres(t, ctx, os.Getenv(auditPostgresEnv))
	default:
		t.Fatal("Audit UI harness profile is invalid")
		return nil
	}
}
