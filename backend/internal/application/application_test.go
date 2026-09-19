package application

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type fakeModule struct {
	id         string
	migrations []Migration
	register   func(http.Handler) error
	start      func(context.Context) error
	stop       func(context.Context) error
}

func (m *fakeModule) ID() string { return m.id }
func (m *fakeModule) RegisterRoutes(handler http.Handler) error {
	if m.register != nil {
		return m.register(handler)
	}
	return nil
}
func (m *fakeModule) Migrations() []Migration {
	return append([]Migration(nil), m.migrations...)
}
func (m *fakeModule) Start(ctx context.Context) error {
	if m.start != nil {
		return m.start(ctx)
	}
	return nil
}
func (m *fakeModule) Stop(ctx context.Context) error {
	if m.stop != nil {
		return m.stop(ctx)
	}
	return nil
}

func TestBuildExposesHandlerStateAndMigrations(t *testing.T) {
	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	module := &fakeModule{id: "admin", migrations: []Migration{{ID: "admin-001"}}}

	app, err := Build(Config{Name: "test"}, Dependencies{Handler: handler}, NewModuleSet(module))
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("handler status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if snapshot := app.State(); snapshot.State != StateConstructed || len(snapshot.StartedModules) != 0 {
		t.Fatalf("State() = %#v, want constructed with no started modules", snapshot)
	}
	if got := app.Migrations(); !reflect.DeepEqual(got, []Migration{{ID: "admin-001"}}) {
		t.Fatalf("Migrations() = %#v", got)
	}
}

func TestBuildRejectsMissingDependenciesAndDuplicateModules(t *testing.T) {
	if _, err := Build(Config{}, Dependencies{}, NewModuleSet()); !errors.Is(err, ErrMissingHandler) {
		t.Fatalf("missing handler error = %v", err)
	}

	handler := http.NewServeMux()
	if _, err := Build(Config{}, Dependencies{Handler: handler}, NewModuleSet(
		&fakeModule{id: "admin"},
		&fakeModule{id: "admin"},
	)); err == nil || !strings.Contains(err.Error(), "duplicate module ID") {
		t.Fatalf("duplicate module error = %v", err)
	}
}

func TestBuildConvertsRouteConflictPanicToError(t *testing.T) {
	module := &fakeModule{
		id: "conflicting",
		register: func(handler http.Handler) error {
			engine := handler.(*http.ServeMux)
			engine.HandleFunc("GET /api/v1/example", func(http.ResponseWriter, *http.Request) {})
			engine.HandleFunc("GET /api/v1/example", func(http.ResponseWriter, *http.Request) {})
			return nil
		},
	}

	_, err := Build(Config{}, Dependencies{Handler: http.NewServeMux()}, NewModuleSet(module))
	if err == nil || !strings.Contains(err.Error(), "conflicts with pattern") {
		t.Fatalf("Build() error = %v", err)
	}
}

func TestApplicationStartsInOrderAndStopsInReverse(t *testing.T) {
	var events []string
	newModule := func(id string) *fakeModule {
		return &fakeModule{
			id: id,
			start: func(context.Context) error {
				events = append(events, "start:"+id)
				return nil
			},
			stop: func(context.Context) error {
				events = append(events, "stop:"+id)
				return nil
			},
		}
	}
	app, err := Build(Config{}, Dependencies{Handler: http.NewServeMux()}, NewModuleSet(newModule("admin"), newModule("jobs")))
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if snapshot := app.State(); snapshot.State != StateReady || !reflect.DeepEqual(snapshot.StartedModules, []string{"admin", "jobs"}) {
		t.Fatalf("ready State() = %#v", snapshot)
	}
	if err := app.Start(context.Background()); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("second Start() error = %v", err)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if snapshot := app.State(); snapshot.State != StateStopped || len(snapshot.StartedModules) != 0 {
		t.Fatalf("stopped State() = %#v", snapshot)
	}
	if err := app.Stop(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("second Stop() error = %v", err)
	}

	want := []string{"start:admin", "start:jobs", "stop:jobs", "stop:admin"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestApplicationCleansUpStartedModulesWhenStartFails(t *testing.T) {
	var events []string
	first := &fakeModule{
		id: "first",
		start: func(context.Context) error {
			events = append(events, "start:first")
			return nil
		},
		stop: func(context.Context) error {
			events = append(events, "stop:first")
			return nil
		},
	}
	second := &fakeModule{
		id: "second",
		start: func(context.Context) error {
			events = append(events, "start:second")
			return errors.New("boom")
		},
	}
	app, err := Build(Config{}, Dependencies{Handler: http.NewServeMux()}, NewModuleSet(first, second))
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	err = app.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "start module \"second\": boom") {
		t.Fatalf("Start() error = %v", err)
	}
	if snapshot := app.State(); snapshot.State != StateFailed || len(snapshot.StartedModules) != 0 {
		t.Fatalf("State() = %#v, want failed with no started modules", snapshot)
	}
	want := []string{"start:first", "start:second", "stop:first"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}
