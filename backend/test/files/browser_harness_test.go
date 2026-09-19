package files_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/app/adapters"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files"
	filesmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/migrations/0010-files"
	capacitymigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/migrations/0020-capacity"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/administration"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	administrationmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0020-administration-schema"
	sessionprotectionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0040-session-protection"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
)

const (
	filesHarnessServe   = "GO_ADMIN_FILES_E2E_SERVE"
	filesHarnessProfile = "GO_ADMIN_FILES_E2E_PROFILE"
	filesHarnessReady   = "GO_ADMIN_FILES_E2E_READY_FILE"
	filesHarnessStatic  = "GO_ADMIN_FILES_E2E_STATIC_DIR"
	filesAdminID        = "account-files-browser-admin"
	filesForeignID      = "account-files-browser-foreign"
)

type filesLoginFactNoop struct{}

func (filesLoginFactNoop) RecordLoginFact(context.Context, database.Tx, session.LoginFact) error {
	return nil
}

// TestFilesBrowserHarnessServer is compiled by source gates and run only by the Lead matrix.
func TestFilesBrowserHarnessServer(t *testing.T) {
	if os.Getenv(filesHarnessServe) != "1" {
		t.Skip(filesHarnessServe + " is not enabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	profile := os.Getenv(filesHarnessProfile)
	var db *database.Database
	var databaseCleanup func()
	if profile == "sqlite" {
		if os.Getenv(postgresDSNEnvironment) != "" {
			t.Fatal("SQLite files harness received PostgreSQL material")
		}
		db, databaseCleanup = openSQLite(t, config.ProfileServerSQLite)
	} else if profile == "postgres" {
		var schema string
		db, databaseCleanup, schema = openPostgres(t)
		var current string
		if err := db.Bun().QueryRowContext(ctx, `SELECT current_schema()`).Scan(&current); err != nil || current != schema {
			t.Fatalf("files harness schema=%q err=%v", current, err)
		}
	} else {
		t.Fatal("files harness profile is invalid")
	}
	defer databaseCleanup()
	runner, err := migrations.NewRunner(sessionmigration.Provider{}, administrationmigration.Provider{}, sessionprotectionmigration.Provider{}, filesmigration.Provider{}, capacitymigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(ctx, db); err != nil {
		t.Fatal("files harness migrations failed")
	}
	seedAccount(t, db, filesAdminID, "files-admin")
	seedAccount(t, db, filesForeignID, "files-foreign")
	registry, err := authorization.NewCapabilityRegistry(db)
	if err != nil || files.RegisterCapabilities(ctx, registry) != nil {
		t.Fatal("files harness capability registration failed")
	}
	for _, id := range []string{filesAdminID, filesForeignID} {
		if _, err := db.Bun().ExecContext(ctx, `INSERT INTO iam_account_roles(account_id, role_id) VALUES (?, ?)`, id, "role-system-admin"); err != nil {
			t.Fatal("files harness role seed failed")
		}
	}
	authorizationAdapters, err := adapters.NewAuthorization(db)
	if err != nil {
		t.Fatal(err)
	}
	root := canonicalContentRoot(t)
	current := &filesSwitchingHandler{}
	type runtimeState struct {
		storage  *files.LocalStorage
		sessions *session.Service
	}
	var liveMu sync.RWMutex
	var live runtimeState
	build := func() {
		storage, err := files.NewLocalStorage(root)
		if err != nil {
			t.Fatal("files harness storage failed")
		}
		policy, err := config.NewSessionPolicy(2*time.Minute, 8*time.Minute, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		sessions, err := session.NewService(db, policy, session.WithLoginFactPort(filesLoginFactNoop{}))
		if err != nil {
			t.Fatal(err)
		}
		service, err := files.NewService(db, storage, authorizationAdapters.Files())
		if err != nil {
			t.Fatal(err)
		}
		if err := service.Reconcile(ctx); err != nil {
			t.Fatal("files harness reconciliation failed")
		}
		sessionAdapters, err := adapters.NewSession(sessions)
		if err != nil {
			t.Fatal(err)
		}
		filesHandler, err := files.NewHTTPHandler(service, sessionAdapters.Files(), func(*http.Request) string { return "0123456789abcdef" })
		if err != nil {
			t.Fatal(err)
		}
		sessionHandler, err := session.NewHTTPHandler(sessions, func(*http.Request) string { return "0123456789abcdef" })
		if err != nil {
			t.Fatal(err)
		}
		adminService, err := administration.NewService(db)
		if err != nil {
			t.Fatal(err)
		}
		adminHandler, err := administration.NewHTTPHandler(adminService, sessions, func(*http.Request) string { return "0123456789abcdef" })
		if err != nil {
			t.Fatal(err)
		}
		api := http.NewServeMux()
		api.HandleFunc("GET /runtime/info", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": 1, "modules": []string{"files"}, "maxUploadBytes": 10485760, "allowedTypes": []string{"application/pdf", "image/jpeg", "image/png", "text/plain"}})
		})
		api.Handle("/files/", filesHandler)
		api.Handle("/iam/session/", sessionHandler)
		api.Handle("/iam/administration/", adminHandler)
		current.set(http.StripPrefix("/api", api))
		liveMu.Lock()
		live = runtimeState{storage: storage, sessions: sessions}
		liveMu.Unlock()
	}
	build()
	t.Cleanup(func() {
		liveMu.RLock()
		storage := live.storage
		liveMu.RUnlock()
		if storage != nil {
			_ = storage.Close()
		}
	})
	foreignStorage := func() *files.LocalStorage { liveMu.RLock(); defer liveMu.RUnlock(); return live.storage }()
	foreignService, err := files.NewService(db, foreignStorage, authorizationAdapters.Files())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreignService.Upload(ctx, filesForeignID, files.UploadInput{OriginalName: "foreign.txt", DeclaredMediaType: "text/plain", Content: strings.NewReader("foreign")}); err != nil {
		t.Fatal("foreign file seed failed")
	}

	shutdown := make(chan struct{})
	var shutdownOnce sync.Once
	mux := http.NewServeMux()
	mux.Handle("/api/", current)
	mux.HandleFunc("/__test/snapshot", func(w http.ResponseWriter, r *http.Request) {
		var total, ready int
		if err := db.Bun().QueryRowContext(r.Context(), `SELECT COUNT(*), COALESCE(SUM(CASE WHEN state = 'ready' THEN 1 ELSE 0 END), 0) FROM files_objects`).Scan(&total, &ready); err != nil {
			w.WriteHeader(500)
			return
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"metadata": total, "ready": ready, "objects": len(entries)})
	})
	mux.HandleFunc("/__test/restart", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		liveMu.Lock()
		old := live.storage
		liveMu.Unlock()
		if err := old.Close(); err != nil {
			w.WriteHeader(500)
			return
		}
		build()
		w.WriteHeader(204)
	})
	mux.HandleFunc("/__test/scope", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Scope string `json:"scope"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body) != nil || (body.Scope != "self" && body.Scope != "all") {
			w.WriteHeader(400)
			return
		}
		if _, err := db.Bun().ExecContext(r.Context(), `UPDATE iam_roles SET data_scope = ? WHERE id = ?`, body.Scope, "role-system-admin"); err != nil {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/__test/permissions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		if body.Enabled {
			registry, err := authorization.NewCapabilityRegistry(db)
			if err != nil || files.RegisterCapabilities(r.Context(), registry) != nil {
				w.WriteHeader(500)
				return
			}
		} else if _, err := db.Bun().ExecContext(r.Context(), `DELETE FROM iam_role_permissions WHERE role_id = ? AND permission_code IN (?, ?)`, "role-system-admin", files.PermissionFilesWrite, files.PermissionFilesDelete); err != nil {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/__test/revoke-session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		liveMu.RLock()
		sessions := live.sessions
		liveMu.RUnlock()
		if err := sessions.RevokeAccount(r.Context(), filesAdminID); err != nil {
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
		shutdownOnce.Do(func() { close(shutdown) })
	})
	staticRoot, readyFile := os.Getenv(filesHarnessStatic), os.Getenv(filesHarnessReady)
	if info, err := os.Stat(staticRoot); err != nil || !info.IsDir() || readyFile == "" {
		t.Fatal("files harness paths are invalid")
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
		t.Fatal("files browser harness timed out")
	}
}

type filesSwitchingHandler struct {
	mu      sync.RWMutex
	handler http.Handler
}

func (value *filesSwitchingHandler) set(handler http.Handler) {
	value.mu.Lock()
	value.handler = handler
	value.mu.Unlock()
}
func (value *filesSwitchingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	value.mu.RLock()
	handler := value.handler
	value.mu.RUnlock()
	handler.ServeHTTP(w, r)
}
