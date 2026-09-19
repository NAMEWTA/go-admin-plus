//go:build desktop_native_e2e

package product

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

func TestDesktopNativeControlRequestIsExactAndBounded(t *testing.T) {
	for _, action := range []string{"scope-self", "scope-all", "permissions-off", "permissions-on", "session-revoke"} {
		request := httptest.NewRequest(http.MethodPost, "/__desktop/test-control", strings.NewReader(`{"action":"`+action+`"}`))
		request.Header.Set("Content-Type", "application/json")
		got, err := decodeDesktopNativeAction(request)
		if err != nil || got != action {
			t.Fatalf("action %q = %q, %v", action, got, err)
		}
	}
	for _, body := range []string{
		`{"action":"unknown"}`,
		`{"action":"scope-all","extra":true}`,
		`{"action":"scope-all"}{"action":"scope-self"}`,
		`{"action":"` + strings.Repeat("a", 130) + `"}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/__desktop/test-control", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if _, err := decodeDesktopNativeAction(request); err == nil {
			t.Fatalf("accepted invalid body %q", body)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/__desktop/test-control", strings.NewReader(`{"action":"scope-all"}`))
	if _, err := decodeDesktopNativeAction(request); err == nil {
		t.Fatal("accepted request without exact JSON content type")
	}
}

func TestDesktopNativeScopeControlEnforcesAndRestoresOwnership(t *testing.T) {
	ctx := context.Background()
	db, err := database.NewProcess().Open(ctx, database.Config{
		Profile: config.ProfileDesktopSQLite, SQLitePath: filepath.Join(t.TempDir(), "desktop.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runner, err := NewMigrationRunner()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	control := &desktopNativeControl{database: db, sessions: &session.Service{}}
	for _, action := range []string{"scope-self", "scope-self", "scope-all"} {
		if err := control.apply(ctx, action); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	var scope string
	if err := db.Bun().NewRaw(`SELECT data_scope FROM iam_roles WHERE id = ?`, "role-system-admin").Scan(ctx, &scope); err != nil || scope != "all" {
		t.Fatalf("scope = %q, %v", scope, err)
	}
	var sentinels int
	if err := db.Bun().NewRaw(`SELECT COUNT(*) FROM files_objects WHERE original_name = ?`, "foreign.txt").Scan(ctx, &sentinels); err != nil || sentinels != 1 {
		t.Fatalf("sentinels = %d, %v", sentinels, err)
	}
}

func TestDesktopNativeControlDispatchesFirstSetupAction(t *testing.T) {
	setup, closeDatabase := testDesktopSetup(t)
	defer closeDatabase()
	control := &desktopNativeControl{setup: setup}
	request := httptest.NewRequest(http.MethodPost, "/__desktop/test-control", strings.NewReader(`{"action":"first-setup-state"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	control.serveHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"required"`) {
		t.Fatalf("native setup state = %d %s", response.Code, response.Body.String())
	}
}
