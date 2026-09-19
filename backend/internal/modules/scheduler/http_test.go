package scheduler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

const schedulerTestCookie = "scheduler-test-session"

type sessionStub struct {
	identity RequestIdentity
	err      error
	mutation bool
	csrf     string
}

func (*sessionStub) CookieName() string { return schedulerTestCookie }

func (stub *sessionStub) AuthorizeRequest(_ context.Context, _, csrf string, mutation bool) (RequestIdentity, error) {
	stub.csrf, stub.mutation = csrf, mutation
	return stub.identity, stub.err
}

func TestSchedulerHTTPContractLifecycle(t *testing.T) {
	db := schedulerDatabase(t)
	registry := schedulerRegistry(t, func(context.Context, database.Tx, testParameters) error { return nil })
	authorizer := &authorizerStub{scope: ScopeAll}
	service, err := NewService(db, authorizer, registry, ClockFunc(func() time.Time { return time.Date(2026, 8, 27, 10, 0, 30, 0, time.UTC) }))
	if err != nil {
		t.Fatal(err)
	}
	sessions := &sessionStub{identity: RequestIdentity{ActorID: "actor", CSRF: "csrf-next"}}
	handler, err := NewHTTPHandler(service, sessions, func(*http.Request) string { return "0123456789abcdef" })
	if err != nil {
		t.Fatal(err)
	}
	response := schedulerRequest(handler, http.MethodGet, "/scheduler/task-types", "", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"key":"test.effect"`) {
		t.Fatalf("task types: %d %s", response.Code, response.Body.String())
	}
	body := `{"name":"Daily","taskType":"test.effect","schedule":{"cron":"0 1 * 1 *","timezone":"UTC"},"parameters":{"message":"ok","count":1,"enabled":true}}`
	response = schedulerRequest(handler, http.MethodPost, "/scheduler/definitions", body, "csrf-current")
	if response.Code != 201 || !sessions.mutation || sessions.csrf != "csrf-current" {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	response = schedulerRequest(handler, http.MethodPost, "/scheduler/definitions", body, "csrf-current")
	assertSchedulerProblem(t, response, 409, "conflict")
	response = schedulerRequest(handler, http.MethodPost, "/scheduler/definitions", `{"name":"invalid"}`, "csrf-current")
	assertSchedulerProblem(t, response, 400, "validation")
	authorizer.err = errors.New("private SQL password=secret")
	response = schedulerRequest(handler, http.MethodGet, "/scheduler/definitions", "", "")
	assertSchedulerProblem(t, response, 500, "internal")
	if strings.Contains(response.Body.String(), "private SQL") || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("diagnostic leak: %s", response.Body.String())
	}
}

func schedulerRequest(handler http.Handler, method, path, body, csrf string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	request.AddCookie(&http.Cookie{Name: schedulerTestCookie, Value: "session-token"})
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertSchedulerProblem(t *testing.T, response *httptest.ResponseRecorder, status int, category string) {
	t.Helper()
	if response.Code != status || !strings.Contains(response.Header().Get("Content-Type"), "application/problem+json") || !strings.Contains(response.Body.String(), `"category":"`+category+`"`) || !strings.Contains(response.Body.String(), `"traceId":"0123456789abcdef"`) {
		t.Fatalf("problem: %d %s", response.Code, response.Body.String())
	}
}
