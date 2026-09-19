package authorization_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/administration"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

type authorizationHTTPLoginFactNoop struct{}

func (authorizationHTTPLoginFactNoop) RecordLoginFact(context.Context, database.Tx, session.LoginFact) error {
	return nil
}

func TestHTTPAuthorizationResponsesUseStableCSRFFromReadOnlySessions(t *testing.T) {
	for _, test := range []struct {
		name, method, path, body string
		prepare                  func(*testing.T, *database.Database)
		want                     int
	}{
		{name: "authorization", method: http.MethodGet, path: "/iam/administration/manifest", prepare: func(t *testing.T, db *database.Database) {
			mustSQL(t, db, `DELETE FROM iam_role_permissions WHERE role_id = ? AND permission_code = ?`, "role-system-admin", authorization.PermissionManifestRead)
		}, want: http.StatusForbidden},
		{name: "conflict", method: http.MethodDelete, path: "/iam/administration/roles/role-system-admin", want: http.StatusConflict},
		{name: "validation", method: http.MethodPost, path: "/iam/administration/users", body: `{"username":"x"}`, want: http.StatusBadRequest},
		{name: "role-key", method: http.MethodPost, path: "/iam/administration/roles", body: `{"key":"Bad Key","name":"Invalid","dataScope":"all"}`, want: http.StatusBadRequest},
		{name: "menu-key", method: http.MethodPost, path: "/iam/administration/menus", body: `{"key":"-invalid","label":"Invalid","path":"/iam/invalid","permissionCode":"iam.users.read","sortOrder":1}`, want: http.StatusBadRequest},
		{name: "page-maximum", method: http.MethodGet, path: "/iam/administration/users?page=1000001", want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, administrationService := newAdministrationFixture(t)
			clock := time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC)
			policy, err := config.NewSessionPolicy(2*time.Hour, 8*time.Hour, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			sessions, err := session.NewService(db, policy, session.WithClock(func() time.Time { return clock }), session.WithLoginFactPort(authorizationHTTPLoginFactNoop{}))
			if err != nil {
				t.Fatal(err)
			}
			issued, err := sessions.Login(context.Background(), "admin", "administrator password")
			if err != nil {
				t.Fatal(err)
			}
			if test.prepare != nil {
				test.prepare(t, db)
			}
			clock = clock.Add(61 * time.Minute)
			handler, err := administration.NewHTTPHandler(administrationService, sessions, func(*http.Request) string { return "0123456789abcdef" })
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-CSRF-Token", issued.CSRF)
			request.AddCookie(&http.Cookie{Name: session.CookieName, Value: issued.Token})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			cookie, csrf := response.Header().Get("Set-Cookie"), response.Header().Get("X-CSRF-Token")
			if cookie != "" || csrf != issued.CSRF {
				t.Fatalf("stable headers mismatch: cookie=%t csrf=%q", cookie != "", csrf)
			}
			if strings.Contains(response.Body.String(), issued.Token) || strings.Contains(response.Body.String(), issued.CSRF) {
				t.Fatal("credential leaked in error body")
			}
			if _, err := sessions.Current(context.Background(), issued.Token); err != nil {
				t.Fatalf("read-only authorization replaced the original token: %v", err)
			}
		})
	}
}

func TestDirectUnauthorizedAPIProducesNoStateChange(t *testing.T) {
	db, administrationService := newAdministrationFixture(t)
	target, err := administrationService.CreateUser(context.Background(), adminID, administration.CreateUser{Username: "ordinary", DisplayName: "Ordinary", Email: "ordinary@example.test", Password: "ordinary password value"})
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := config.NewSessionPolicy(time.Hour, 8*time.Hour, 30*time.Minute)
	sessions, err := session.NewService(db, policy, session.WithLoginFactPort(authorizationHTTPLoginFactNoop{}))
	if err != nil {
		t.Fatal(err)
	}
	issued, err := sessions.Login(context.Background(), target.Username, "ordinary password value")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := administration.NewHTTPHandler(administrationService, sessions, func(*http.Request) string { return "0123456789abcdef" })
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM iam_accounts`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/iam/administration/users", strings.NewReader(`{"username":"forbidden","displayName":"Forbidden","email":"forbidden@example.test","password":"forbidden password"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", issued.CSRF)
	request.AddCookie(&http.Cookie{Name: session.CookieName, Value: issued.Token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var after int
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM iam_accounts`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("unauthorized API changed accounts: %d -> %d", before, after)
	}
}

func mustSQL(t *testing.T, db *database.Database, query string, args ...any) {
	t.Helper()
	if _, err := db.Bun().ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}
