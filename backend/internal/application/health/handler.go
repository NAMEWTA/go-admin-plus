// Package health exposes the canonical host-neutral operational HTTP contract.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/application"
)

const (
	LivePath         = "/health/live"
	ReadyPath        = "/health/ready"
	MetricsPath      = "/metrics"
	CapabilitiesPath = "/api/v1/runtime/capabilities"
	StatusPath       = "/api/v1/runtime/status"
)

// Capabilities contains only non-sensitive runtime features.
type Capabilities struct {
	Profile       string `json:"profile"`
	Version       string `json:"version"`
	Database      string `json:"database"`
	Desktop       bool   `json:"desktop"`
	Offline       bool   `json:"offline"`
	NativeDialogs bool   `json:"nativeDialogs"`
}

// Checker verifies one dependency required to accept business requests. Names
// and errors remain in-process and never enter public responses or metrics.
type Checker struct {
	Name  string
	Check func(context.Context) error
}

// Handler wraps business routing with the five canonical operational paths.
type Handler struct {
	snapshot     func() application.Snapshot
	capabilities Capabilities
	checkers     []Checker
}

// New validates and snapshots the operational contract.
func New(snapshot func() application.Snapshot, capabilities Capabilities, checkers ...Checker) (*Handler, error) {
	if snapshot == nil {
		return nil, errors.New("application snapshot function is required")
	}
	if err := capabilities.Validate(); err != nil {
		return nil, err
	}
	owned := append([]Checker(nil), checkers...)
	seen := make(map[string]struct{}, len(owned))
	for index := range owned {
		owned[index].Name = strings.TrimSpace(owned[index].Name)
		if owned[index].Name == "" || owned[index].Check == nil {
			return nil, fmt.Errorf("readiness checker at index %d requires name and function", index)
		}
		if _, exists := seen[owned[index].Name]; exists {
			return nil, fmt.Errorf("duplicate readiness checker %q", owned[index].Name)
		}
		seen[owned[index].Name] = struct{}{}
	}
	return &Handler{snapshot: snapshot, capabilities: capabilities, checkers: owned}, nil
}

// Validate rejects capability combinations outside the three supported profiles.
func (capabilities Capabilities) Validate() error {
	if strings.TrimSpace(capabilities.Version) == "" {
		return errors.New("runtime version is required")
	}
	switch capabilities.Profile {
	case "server-postgres":
		if capabilities.Database != "postgres" || capabilities.Desktop || capabilities.Offline || capabilities.NativeDialogs {
			return errors.New("server-postgres capabilities are inconsistent")
		}
	case "server-sqlite":
		if capabilities.Database != "sqlite" || capabilities.Desktop || capabilities.Offline || capabilities.NativeDialogs {
			return errors.New("server-sqlite capabilities are inconsistent")
		}
	case "desktop-sqlite":
		if capabilities.Database != "sqlite" || !capabilities.Desktop || !capabilities.Offline || !capabilities.NativeDialogs {
			return errors.New("desktop-sqlite capabilities are inconsistent")
		}
	default:
		return errors.New("runtime profile is unsupported")
	}
	return nil
}

// Wrap preserves business routing while reserving every operational path.
func (handler *Handler) Wrap(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if isOperationalPath(request.URL.Path) && request.Method != http.MethodGet {
			serveMethodNotAllowed(response)
			return
		}
		switch request.URL.Path {
		case LivePath:
			handler.serveJSON(response, http.StatusOK, stateResponse{Status: "live"})
		case ReadyPath:
			status := handler.runtimeStatus(request.Context())
			if status.Ready {
				handler.serveJSON(response, http.StatusOK, stateResponse{Status: "ready"})
				return
			}
			handler.serveJSON(response, http.StatusServiceUnavailable, stateResponse{Status: "not_ready"})
		case MetricsPath:
			handler.serveMetrics(response, request.Context())
		case CapabilitiesPath:
			handler.serveJSON(response, http.StatusOK, handler.capabilities)
		case StatusPath:
			handler.serveJSON(response, http.StatusOK, handler.runtimeStatus(request.Context()))
		default:
			next.ServeHTTP(response, request)
		}
	})
}

func isOperationalPath(value string) bool {
	switch value {
	case LivePath, ReadyPath, MetricsPath, CapabilitiesPath, StatusPath:
		return true
	default:
		return false
	}
}

func serveMethodNotAllowed(response http.ResponseWriter) {
	setNoStore(response)
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Allow", http.MethodGet)
	response.WriteHeader(http.StatusMethodNotAllowed)
	_ = json.NewEncoder(response).Encode(stateResponse{Status: "method_not_allowed"})
}

func (handler *Handler) runtimeStatus(ctx context.Context) statusResponse {
	snapshot := handler.snapshot()
	state := publicState(snapshot.State)
	ready := state == "ready"
	if ready {
		for _, checker := range handler.checkers {
			if err := checker.Check(ctx); err != nil {
				state = "dependency-failed"
				ready = false
				break
			}
		}
	}
	return statusResponse{
		Profile:  handler.capabilities.Profile,
		Version:  handler.capabilities.Version,
		Database: handler.capabilities.Database,
		State:    state,
		Ready:    ready,
	}
}

func publicState(state application.State) string {
	switch state {
	case application.StateConstructed, application.StateStarting:
		return "starting"
	case application.StateReady:
		return "ready"
	case application.StateStopping, application.StateStopped:
		return "draining"
	case application.StateFailed:
		return "dependency-failed"
	default:
		return "dependency-failed"
	}
}

func (handler *Handler) serveJSON(response http.ResponseWriter, status int, body any) {
	setNoStore(response)
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}

func (handler *Handler) serveMetrics(response http.ResponseWriter, ctx context.Context) {
	setNoStore(response)
	response.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	status := handler.runtimeStatus(ctx)
	ready := 0
	if status.Ready {
		ready = 1
	}
	_, _ = fmt.Fprintf(response,
		"# TYPE go_admin_runtime_info gauge\n"+
			"go_admin_runtime_info{profile=\"%s\",version=\"%s\",database=\"%s\"} 1\n"+
			"# TYPE go_admin_runtime_ready gauge\n"+
			"go_admin_runtime_ready %d\n"+
			"# TYPE go_admin_runtime_state gauge\n"+
			"go_admin_runtime_state{state=\"%s\"} 1\n",
		metricLabel(status.Profile), metricLabel(status.Version), metricLabel(status.Database), ready, metricLabel(status.State))
}

func setNoStore(response http.ResponseWriter) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Pragma", "no-cache")
}

func metricLabel(value string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"").Replace(value)
}

type stateResponse struct {
	Status string `json:"status"`
}

type statusResponse struct {
	Profile  string `json:"profile"`
	Version  string `json:"version"`
	Database string `json:"database"`
	State    string `json:"state"`
	Ready    bool   `json:"ready"`
}
