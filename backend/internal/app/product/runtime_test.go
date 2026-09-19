package product_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/app/product"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/audit"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/account"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/coordination"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

func TestBuildAssemblesEveryHTTPModuleAndWorkerLifecycle(t *testing.T) {
	ctx := context.Background()
	db, err := database.NewProcess().Open(ctx, database.Config{
		Profile:    config.ProfileServerSQLite,
		SQLitePath: filepath.Join(t.TempDir(), "runtime.sqlite3"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	dataRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := product.Build(ctx, db, product.Options{
		SessionPolicy:     config.DefaultSessionPolicy(),
		FilesRoot:         filepath.Join(dataRoot, "files"),
		WorkerOwner:       "product-test-worker",
		WorkerInterval:    time.Hour,
		AuditRetentionAge: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Application.Start(ctx); err != nil {
		t.Fatal(err)
	}

	paths := []string{
		"/api/runtime/identity",
		"/api/runtime/navigation",
		"/api/iam/session/current",
		"/api/iam/account/profile",
		"/api/iam/administration/manifest",
		"/api/audit/records?page=1&pageSize=20",
		"/api/scheduler/task-types",
		"/api/files/objects?page=1&pageSize=20",
	}
	for _, path := range paths {
		response := httptest.NewRecorder()
		runtime.Application.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Errorf("GET %s status = %d, want authentication rejection", path, response.Code)
		}
	}

	passwordHash, err := account.HashPassword("runtime identity password")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO iam_roles(id, role_key, name, data_scope, enabled, protected, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, "role-runtime-self", "runtime-self", "Runtime self scope", "self", true, false); err != nil {
			return err
		}
		for _, permission := range []string{"files.objects.read", "iam.manifest.read"} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO iam_role_permissions(role_id, permission_code) VALUES (?, ?)`, "role-runtime-self", permission); err != nil {
				return err
			}
		}
		if err := account.NewRepository(db.Dialect()).Create(ctx, tx, account.Credential{
			Profile:      account.Profile{ID: "account-runtime-self", Username: "runtime-self", DisplayName: "Runtime Self", Email: "runtime-self@example.test"},
			PasswordHash: passwordHash,
		}, time.Now().UTC()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO iam_account_roles(account_id, role_id) VALUES (?, ?)`, "account-runtime-self", "role-runtime-self")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	issued, err := runtime.Sessions.Login(ctx, "runtime-self", "runtime identity password")
	if err != nil {
		t.Fatal(err)
	}
	var loginFacts int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_facts WHERE topic = ? AND actor_ref = ?`, audit.TopicLoginSucceeded, "account:account-runtime-self").Scan(&loginFacts); err != nil {
		t.Fatal(err)
	}
	if loginFacts != 1 {
		t.Fatalf("successful login audit facts = %d, want 1", loginFacts)
	}
	identityRequest := httptest.NewRequest(http.MethodGet, "/api/runtime/identity", nil)
	identityRequest.AddCookie(&http.Cookie{Name: session.CookieName, Value: issued.Token})
	identityResponse := httptest.NewRecorder()
	runtime.Application.Handler().ServeHTTP(identityResponse, identityRequest)
	if identityResponse.Code != http.StatusOK {
		t.Fatalf("identity status = %d, body = %s", identityResponse.Code, identityResponse.Body.String())
	}
	var identity struct {
		Kind      string `json:"kind"`
		SubjectID string `json:"subjectId"`
		DataScope string `json:"dataScope"`
	}
	if err := json.Unmarshal(identityResponse.Body.Bytes(), &identity); err != nil {
		t.Fatal(err)
	}
	if identity.Kind != "authenticated" || identity.SubjectID != "account-runtime-self" || identity.DataScope != "self" {
		t.Fatalf("identity = %#v", identity)
	}

	if err := runtime.Application.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	lease, err := coordination.Acquire(ctx, db, coordination.Config{Owner: "product-test-after-stop"})
	if err != nil {
		t.Fatalf("worker lease remained owned after stop: %v", err)
	}
	if err := lease.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestBuildRejectsIncompleteProductOptionsBeforeAssembly(t *testing.T) {
	if _, err := product.Build(context.Background(), nil, product.Options{}); err == nil {
		t.Fatal("Build accepted incomplete dependencies")
	}
}

func TestBuildPreparedAPIRoleDoesNotOwnWorkerLease(t *testing.T) {
	ctx := context.Background()
	db, err := database.NewProcess().Open(ctx, database.Config{
		Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "api.sqlite3"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := product.PrepareRuntimeSchema(ctx, db, true); err != nil {
		t.Fatal(err)
	}
	dataRoot := t.TempDir()
	built, err := product.BuildPrepared(ctx, db, product.Options{
		SessionPolicy: config.DefaultSessionPolicy(), FilesRoot: filepath.Join(dataRoot, "files"),
		WorkerOwner: "api-without-workers", WorkerInterval: time.Hour, AuditRetentionAge: 30 * 24 * time.Hour,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Readiness) != 1 || built.Readiness[0].Name != "database" {
		t.Fatalf("API readiness = %#v", built.Readiness)
	}
	if err := built.Application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	lease, err := coordination.Acquire(ctx, db, coordination.Config{Owner: "independent-worker"})
	if err != nil {
		t.Fatalf("API role owned worker lease: %v", err)
	}
	if err := lease.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := built.Application.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
