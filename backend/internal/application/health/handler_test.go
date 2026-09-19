package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/application"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/application/health"
)

func TestLiveReadyAndCapabilitiesHaveDistinctPublicSemantics(t *testing.T) {
	var state atomic.Value
	state.Store(application.Snapshot{State: application.StateStarting})
	dependencyErr := atomic.Bool{}

	handler, err := health.New(
		func() application.Snapshot { return state.Load().(application.Snapshot) },
		health.Capabilities{
			Profile:       "server-sqlite",
			Version:       "test-version",
			Database:      "sqlite",
			Desktop:       false,
			Offline:       false,
			NativeDialogs: false,
		},
		health.Checker{Name: "database", Check: func(context.Context) error {
			if dependencyErr.Load() {
				return errors.New("postgres://operator:secret@example.invalid/private")
			}
			return nil
		}},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server := httptest.NewServer(handler.Wrap(http.NotFoundHandler()))
	t.Cleanup(server.Close)

	assertStatus(t, server.URL+"/health/live", http.StatusOK, `{"status":"live"}`)
	assertStatus(t, server.URL+"/health/ready", http.StatusServiceUnavailable, `{"status":"not_ready"}`)

	state.Store(application.Snapshot{State: application.StateReady})
	assertStatus(t, server.URL+"/health/ready", http.StatusOK, `{"status":"ready"}`)

	dependencyErr.Store(true)
	response := assertStatus(t, server.URL+"/health/ready", http.StatusServiceUnavailable, `{"status":"not_ready"}`)
	if strings.Contains(response, "secret") || strings.Contains(response, "postgres://") {
		t.Fatalf("readiness leaked dependency details: %s", response)
	}
	assertStatus(t, server.URL+"/health/live", http.StatusOK, `{"status":"live"}`)

	response = assertStatus(t, server.URL+"/api/v1/runtime/capabilities", http.StatusOK, "")
	var capabilities health.Capabilities
	if err := json.Unmarshal([]byte(response), &capabilities); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if capabilities.Profile != "server-sqlite" || capabilities.Version != "test-version" || capabilities.Database != "sqlite" || capabilities.Desktop || capabilities.Offline || capabilities.NativeDialogs {
		t.Fatalf("capabilities = %#v", capabilities)
	}
}

func TestHealthRejectsInvalidConfigurationAndMethods(t *testing.T) {
	if _, err := health.New(nil, health.Capabilities{}); err == nil {
		t.Fatal("New accepted a nil application state function")
	}
	if _, err := health.New(
		func() application.Snapshot { return application.Snapshot{State: application.StateReady} },
		health.Capabilities{Profile: "server-sqlite", Version: "test-version", Database: "sqlite"},
		health.Checker{Name: "", Check: func(context.Context) error { return nil }},
	); err == nil {
		t.Fatal("New accepted an unnamed readiness checker")
	}

	handler, err := health.New(
		func() application.Snapshot { return application.Snapshot{State: application.StateReady} },
		health.Capabilities{Profile: "server-sqlite", Version: "test-version", Database: "sqlite"},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/health/live", nil)
	response := httptest.NewRecorder()
	handler.Wrap(http.NotFoundHandler()).ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /health/live status = %d, want 405", response.Code)
	}
}

func TestMetricsAndRuntimeStatusExposeReadinessState(t *testing.T) {
	var state atomic.Value
	state.Store(application.Snapshot{State: application.StateReady})
	dependencyErr := atomic.Bool{}
	handler, err := health.New(
		func() application.Snapshot { return state.Load().(application.Snapshot) },
		health.Capabilities{Profile: "server-sqlite", Version: "test-version", Database: "sqlite"},
		health.Checker{Name: "database", Check: func(context.Context) error {
			if dependencyErr.Load() {
				return errors.New("private dependency failure")
			}
			return nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler.Wrap(nil))
	t.Cleanup(server.Close)

	assertStatus(t, server.URL+"/api/v1/runtime/status", http.StatusOK, `{"database":"sqlite","profile":"server-sqlite","ready":true,"state":"ready","version":"test-version"}`)
	metrics := assertText(t, server.URL+"/metrics", http.StatusOK)
	if !strings.Contains(metrics, `go_admin_runtime_ready 1`) || !strings.Contains(metrics, `go_admin_runtime_state{state="ready"} 1`) {
		t.Fatalf("ready metrics = %q", metrics)
	}

	dependencyErr.Store(true)
	assertStatus(t, server.URL+"/api/v1/runtime/status", http.StatusOK, `{"database":"sqlite","profile":"server-sqlite","ready":false,"state":"dependency-failed","version":"test-version"}`)
	state.Store(application.Snapshot{State: application.StateStopping})
	assertStatus(t, server.URL+"/api/v1/runtime/status", http.StatusOK, `{"database":"sqlite","profile":"server-sqlite","ready":false,"state":"draining","version":"test-version"}`)
}

func TestNonGETOperationsDoNotReadSnapshotOrCheckers(t *testing.T) {
	var snapshots, checks atomic.Int32
	handler, err := health.New(
		func() application.Snapshot {
			snapshots.Add(1)
			return application.Snapshot{State: application.StateReady}
		},
		health.Capabilities{Profile: "server-postgres", Version: "test-version", Database: "postgres"},
		health.Checker{Name: "database", Check: func(context.Context) error {
			checks.Add(1)
			return nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := handler.Wrap(nil)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodOptions} {
		for _, path := range []string{health.LivePath, health.ReadyPath, health.MetricsPath, health.CapabilitiesPath, health.StatusPath} {
			request := httptest.NewRequest(method, path, nil)
			response := httptest.NewRecorder()
			wrapped.ServeHTTP(response, request)
			if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s %s response = %d headers=%v", method, path, response.Code, response.Header())
			}
		}
	}
	if snapshots.Load() != 0 || checks.Load() != 0 {
		t.Fatalf("non-GET operations read snapshots=%d checks=%d", snapshots.Load(), checks.Load())
	}
}

func TestCapabilitiesAndCheckerNamesAreStrict(t *testing.T) {
	snapshot := func() application.Snapshot { return application.Snapshot{State: application.StateReady} }
	valid := []health.Capabilities{
		{Profile: "server-postgres", Version: "v1", Database: "postgres"},
		{Profile: "server-sqlite", Version: "v1", Database: "sqlite"},
		{Profile: "desktop-sqlite", Version: "v1", Database: "sqlite", Desktop: true, Offline: true, NativeDialogs: true},
	}
	for _, capabilities := range valid {
		if _, err := health.New(snapshot, capabilities); err != nil {
			t.Fatalf("valid capabilities %#v: %v", capabilities, err)
		}
	}
	invalid := []health.Capabilities{
		{Profile: "server-postgres", Version: "v1", Database: "sqlite"},
		{Profile: "server-sqlite", Version: "v1", Database: "sqlite", Desktop: true},
		{Profile: "desktop-sqlite", Version: "v1", Database: "sqlite", Desktop: true, Offline: true},
		{Profile: "unknown", Version: "v1", Database: "sqlite"},
	}
	for _, capabilities := range invalid {
		if _, err := health.New(snapshot, capabilities); err == nil {
			t.Fatalf("accepted invalid capabilities %#v", capabilities)
		}
	}
	checker := health.Checker{Name: "database", Check: func(context.Context) error { return nil }}
	if _, err := health.New(snapshot, valid[0], checker, checker); err == nil {
		t.Fatal("accepted duplicate readiness checker names")
	}
}

func assertStatus(t *testing.T, url string, wantStatus int, wantBody string) string {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer response.Body.Close()
	var decoded any
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	body, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("GET %s status = %d, want %d; body = %s", url, response.StatusCode, wantStatus, body)
	}
	if wantBody != "" && string(body) != wantBody {
		t.Fatalf("GET %s body = %s, want %s", url, body, wantBody)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("GET %s Cache-Control = %q, want no-store", url, response.Header.Get("Cache-Control"))
	}
	return string(body)
}

func assertText(t *testing.T, url string, wantStatus int) string {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("GET %s status = %d, want %d", url, response.StatusCode, wantStatus)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("GET %s Cache-Control = %q, want no-store", url, response.Header.Get("Cache-Control"))
	}
	return string(contents)
}
