package product

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

type desktopSetupLoginFacts struct{}

func (desktopSetupLoginFacts) RecordLoginFact(context.Context, database.Tx, session.LoginFact) error {
	return nil
}

func testDesktopSetup(t *testing.T) (*desktopSetup, func()) {
	t.Helper()
	ctx := context.Background()
	db, err := database.NewProcess().Open(ctx, database.Config{
		Profile: config.ProfileDesktopSQLite, SQLitePath: filepath.Join(t.TempDir(), "desktop.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := PrepareRuntimeSchema(ctx, db, true); err != nil {
		db.Close()
		t.Fatal(err)
	}
	sessions, err := session.NewService(db, config.DefaultSessionPolicy(), session.WithLoginFactPort(desktopSetupLoginFacts{}))
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return newDesktopSetup(db, sessions), func() { _ = db.Close() }
}

func setupRequest(t *testing.T, setup *desktopSetup, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, desktopSetupPath, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	setup.serveHTTP(response, request)
	return response
}

func TestDesktopFirstSetupCreatesAdminAndFirstSessionOnce(t *testing.T) {
	setup, closeDatabase := testDesktopSetup(t)
	defer closeDatabase()

	state := setupRequest(t, setup, `{"action":"first-setup-state"}`)
	if state.Code != http.StatusOK || !strings.Contains(state.Body.String(), `"state":"required"`) {
		t.Fatalf("initial state = %d %s", state.Code, state.Body.String())
	}
	created := setupRequest(t, setup, `{"action":"first-setup-submit","username":"admin","displayName":"Administrator","email":"admin@example.test","password":"correct horse battery staple"}`)
	if created.Code != http.StatusOK {
		t.Fatalf("created = %d %s", created.Code, created.Body.String())
	}
	var result desktopSetupResponse
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.State != "complete" || result.Profile == nil || result.Profile.Username != "admin" || len(result.SessionToken) != 43 || len(result.CSRFToken) != 43 {
		t.Fatal("desktop setup did not return a valid public profile and native session")
	}
	repeated := setupRequest(t, setup, `{"action":"first-setup-submit","username":"second","displayName":"Second","email":"second@example.test","password":"correct horse battery staple"}`)
	if repeated.Code != http.StatusConflict || !strings.Contains(repeated.Body.String(), `"state":"login-required"`) {
		t.Fatalf("repeat = %d %s", repeated.Code, repeated.Body.String())
	}
}

func TestDesktopFirstSetupKeepsCommittedAdminWhenSessionFails(t *testing.T) {
	setup, closeDatabase := testDesktopSetup(t)
	defer closeDatabase()
	setup.login = func(context.Context, string, string) (session.Issued, error) {
		return session.Issued{}, session.ErrInternal
	}

	created := setupRequest(t, setup, `{"action":"first-setup-submit","username":"admin","displayName":"Administrator","email":"admin@example.test","password":"correct horse battery staple"}`)
	if created.Code != http.StatusConflict || !strings.Contains(created.Body.String(), `"state":"login-required"`) {
		t.Fatalf("partial result = %d %s", created.Code, created.Body.String())
	}
	restarted := setupRequest(t, setup, `{"action":"first-setup-state"}`)
	if restarted.Code != http.StatusOK || !strings.Contains(restarted.Body.String(), `"state":"login-required"`) {
		t.Fatalf("restart state = %d %s", restarted.Code, restarted.Body.String())
	}
}

func TestDesktopFirstSetupInputIsStrictAndBounded(t *testing.T) {
	for _, body := range []string{
		`{"action":"first-setup-state","password":"hidden password"}`,
		`{"action":"first-setup-submit","username":"admin","displayName":"Admin","email":"admin@example.test","password":"valid password","extra":true}`,
		`{"action":"unknown"}`,
		`{"action":"first-setup-state"}{"action":"first-setup-state"}`,
		strings.Repeat("x", desktopSetupMaximumBytes+1),
	} {
		request := httptest.NewRequest(http.MethodPost, desktopSetupPath, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		(&desktopSetup{}).serveHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("accepted invalid body with status %d", response.Code)
		}
	}
}

func TestDesktopFirstSetupProductionRouteIsPrivateAndExact(t *testing.T) {
	route := newDesktopSetupPrivateRoute(nil, nil, desktopSetupPath)
	if route == nil || route.Pattern != "POST "+desktopSetupPath || route.Handler == nil {
		t.Fatalf("unexpected desktop setup route: %#v", route)
	}
}
