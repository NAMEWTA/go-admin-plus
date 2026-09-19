package scheduler

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/google/uuid"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/contracts"
	transport "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/scheduler/transport"
)

//go:embed transport/openapi.json
var openAPIDocument []byte

var tracePattern = regexp.MustCompile(`^[a-f0-9]{16,64}$`)

var (
	ErrAuthentication = errors.New("scheduler authentication required")
	ErrCSRF           = errors.New("scheduler csrf rejected")
)

type RequestIdentity struct {
	ActorID, CSRF     string
	ReplacementCookie *string
}

type RequestAuthenticator interface {
	CookieName() string
	AuthorizeRequest(context.Context, string, string, bool) (RequestIdentity, error)
}

type httpContextKey struct{}
type httpContext struct {
	actorID string
	csrf    string
	trace   string
	cookie  *string
}

type HTTPServer struct{ service *Service }

func NewHTTPHandler(service *Service, authenticator RequestAuthenticator, traceID contracts.TraceIDProvider) (http.Handler, error) {
	if service == nil || authenticator == nil || authenticator.CookieName() == "" || traceID == nil {
		return nil, errors.New("scheduler HTTP dependencies are required")
	}
	server := &HTTPServer{service: service}
	strict := transport.NewStrictHandlerWithOptions(server, nil, transport.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeProblem(w, problem(transport.Validation, "REQUEST_INVALID", "Request validation failed", traceID(r), 400))
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeProblem(w, problem(transport.Internal, "INTERNAL_ERROR", "Internal server error", traceID(r), 500))
		},
	})
	router := transport.HandlerWithOptions(strict, transport.ChiServerOptions{ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
		writeProblem(w, problem(transport.Validation, "REQUEST_INVALID", "Request validation failed", traceID(r), 400))
	}})
	validated, err := contracts.NewRequestValidator(openAPIDocument, router, traceID, contracts.RequestValidatorOptions{MaxBodyBytes: contracts.DefaultMaxRequestBodyBytes, AuthenticationFunc: openapi3filter.NoopAuthenticationFunc})
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		cookie, _ := r.Cookie(authenticator.CookieName())
		token := ""
		if cookie != nil {
			token = cookie.Value
		}
		identity, authErr := authenticator.AuthorizeRequest(r.Context(), token, r.Header.Get("X-CSRF-Token"), r.Method != http.MethodGet && r.Method != http.MethodHead)
		if authErr != nil {
			switch {
			case errors.Is(authErr, ErrCSRF):
				writeProblem(w, problem(transport.Authorization, "CSRF_REJECTED", "Request authorization failed", traceID(r), 403))
			case errors.Is(authErr, ErrAuthentication):
				writeProblem(w, problem(transport.Authentication, "SESSION_REQUIRED", "Authentication required", traceID(r), 401))
			default:
				writeProblem(w, problem(transport.Internal, "INTERNAL_ERROR", "Internal server error", traceID(r), 500))
			}
			return
		}
		value := httpContext{actorID: identity.ActorID, csrf: identity.CSRF, trace: traceID(r), cookie: identity.ReplacementCookie}
		w.Header().Set("X-CSRF-Token", value.csrf)
		if value.cookie != nil {
			w.Header().Set("Set-Cookie", *value.cookie)
		}
		validated.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), httpContextKey{}, value)))
	}), nil
}

func requestHTTP(ctx context.Context) httpContext {
	value, _ := ctx.Value(httpContextKey{}).(httpContext)
	return value
}
func responseHeaders(ctx context.Context) (*string, *string) {
	value := requestHTTP(ctx)
	csrf := value.csrf
	return &csrf, value.cookie
}
func problem(category transport.ProblemCategory, code, title, trace string, status int) transport.Problem {
	if !tracePattern.MatchString(trace) {
		trace = "0000000000000000"
	}
	return transport.Problem{Type: "urn:go-admin-plus:problem:" + strings.ToLower(strings.ReplaceAll(code, "_", "-")), Title: title, Status: status, Category: category, Code: code, TraceId: trace}
}
func writeProblem(w http.ResponseWriter, value transport.Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(value.Status)
	_ = json.NewEncoder(w).Encode(value)
}
func validationProblem(ctx context.Context) transport.ValidationProblemApplicationProblemPlusJSONResponse {
	csrf, cookie := responseHeaders(ctx)
	return transport.ValidationProblemApplicationProblemPlusJSONResponse{Body: problem(transport.Validation, "REQUEST_INVALID", "Request validation failed", requestHTTP(ctx).trace, 400), Headers: transport.ValidationProblemResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}
}
func authorizationProblem(ctx context.Context) transport.AuthorizationProblemApplicationProblemPlusJSONResponse {
	csrf, cookie := responseHeaders(ctx)
	return transport.AuthorizationProblemApplicationProblemPlusJSONResponse{Body: problem(transport.Authorization, "PERMISSION_DENIED", "Request authorization failed", requestHTTP(ctx).trace, 403), Headers: transport.AuthorizationProblemResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}
}
func notFoundProblem(ctx context.Context) transport.NotFoundProblemApplicationProblemPlusJSONResponse {
	csrf, cookie := responseHeaders(ctx)
	return transport.NotFoundProblemApplicationProblemPlusJSONResponse{Body: problem(transport.NotFound, "RESOURCE_NOT_FOUND", "Resource not found", requestHTTP(ctx).trace, 404), Headers: transport.NotFoundProblemResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}
}
func conflictProblem(ctx context.Context) transport.ConflictProblemApplicationProblemPlusJSONResponse {
	csrf, cookie := responseHeaders(ctx)
	return transport.ConflictProblemApplicationProblemPlusJSONResponse{Body: problem(transport.Conflict, "RESOURCE_CONFLICT", "Resource conflict", requestHTTP(ctx).trace, 409), Headers: transport.ConflictProblemResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}
}
func internalProblem(ctx context.Context) transport.InternalProblemApplicationProblemPlusJSONResponse {
	csrf, cookie := responseHeaders(ctx)
	return transport.InternalProblemApplicationProblemPlusJSONResponse{Body: problem(transport.Internal, "INTERNAL_ERROR", "Internal server error", requestHTTP(ctx).trace, 500), Headers: transport.InternalProblemResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}
}

func input(name, taskType string, schedule transport.Schedule, parameters transport.Parameters) (DefinitionInput, error) {
	raw, err := json.Marshal(parameters)
	if err != nil {
		return DefinitionInput{}, ErrValidation
	}
	var values map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&values); err != nil {
		return DefinitionInput{}, ErrValidation
	}
	return DefinitionInput{Name: name, TaskType: taskType, Schedule: Schedule{Cron: schedule.Cron, Timezone: schedule.Timezone}, Parameters: values}, nil
}

func transportDefinition(value Definition) (transport.Definition, error) {
	raw, err := json.Marshal(value.Parameters)
	if err != nil {
		return transport.Definition{}, err
	}
	var parameters transport.Parameters
	if err := json.Unmarshal(raw, &parameters); err != nil {
		return transport.Definition{}, err
	}
	parsed, err := uuid.Parse(value.ID)
	if err != nil {
		return transport.Definition{}, err
	}
	return transport.Definition{Id: parsed, Name: value.Name, TaskType: value.TaskType, Schedule: transport.Schedule{Cron: value.Schedule.Cron, Timezone: value.Schedule.Timezone}, Parameters: parameters, Enabled: value.Enabled, Revision: int(value.Revision), NextRunAt: value.NextRunAt, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}, nil
}
