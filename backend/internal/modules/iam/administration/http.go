package administration

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3filter"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/contracts"
	transport "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/administration/transport"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session"
)

//go:embed transport/openapi.json
var openAPIDocument []byte

var tracePattern = regexp.MustCompile(`^[a-f0-9]{16,64}$`)

type SessionAuthorizer interface {
	AuthorizeRequest(context.Context, string, string, bool) (session.Issued, error)
}

type httpContextKey struct{}
type httpContext struct {
	actorID, csrf, trace string
	cookie               *string
}

type HTTPServer struct {
	service   *Service
	deletions *DeletionService
}

type HTTPOption func(*HTTPServer)

func WithHTTPDeletionService(service *DeletionService) HTTPOption {
	return func(server *HTTPServer) { server.deletions = service }
}

func NewHTTPHandler(service *Service, sessions SessionAuthorizer, traceID contracts.TraceIDProvider, options ...HTTPOption) (http.Handler, error) {
	if service == nil || sessions == nil || traceID == nil {
		return nil, errors.New("iam administration HTTP dependencies are required")
	}
	server := &HTTPServer{service: service}
	for _, option := range options {
		if option != nil {
			option(server)
		}
	}
	strict := transport.NewStrictHandlerWithOptions(server, nil, transport.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeProblem(w, problem(transport.Validation, "REQUEST_INVALID", "Request validation failed", traceID(r), http.StatusBadRequest))
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeProblem(w, problem(transport.Internal, "INTERNAL_ERROR", "Internal server error", traceID(r), http.StatusInternalServerError))
		},
	})
	router := transport.HandlerWithOptions(strict, transport.ChiServerOptions{ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
		writeProblem(w, problem(transport.Validation, "REQUEST_INVALID", "Request validation failed", traceID(r), http.StatusBadRequest))
	}})
	validated, err := contracts.NewRequestValidator(openAPIDocument, router, traceID, contracts.RequestValidatorOptions{MaxBodyBytes: contracts.DefaultMaxRequestBodyBytes, AuthenticationFunc: openapi3filter.NoopAuthenticationFunc})
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		cookie, _ := r.Cookie(session.CookieName)
		token := ""
		if cookie != nil {
			token = cookie.Value
		}
		issued, authErr := sessions.AuthorizeRequest(r.Context(), token, r.Header.Get("X-CSRF-Token"), r.Method != http.MethodGet && r.Method != http.MethodHead)
		if authErr != nil {
			if errors.Is(authErr, session.ErrCSRF) {
				writeProblem(w, problem(transport.Authorization, "CSRF_REJECTED", "Request authorization failed", traceID(r), http.StatusForbidden))
				return
			}
			if errors.Is(authErr, session.ErrAuthentication) {
				writeProblem(w, problem(transport.Authentication, "SESSION_REQUIRED", "Authentication required", traceID(r), http.StatusUnauthorized))
				return
			}
			writeProblem(w, problem(transport.Internal, "INTERNAL_ERROR", "Internal server error", traceID(r), http.StatusInternalServerError))
			return
		}
		value := httpContext{actorID: issued.Profile.ID, csrf: issued.CSRF, trace: traceID(r), cookie: replacementCookie(issued)}
		w.Header().Set("X-CSRF-Token", value.csrf)
		if value.cookie != nil {
			w.Header().Set("Set-Cookie", *value.cookie)
		}
		ctx := context.WithValue(r.Context(), httpContextKey{}, value)
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if r.Method != "GET" && r.Method != "HEAD" && len(parts) >= 4 && parts[0] == "iam" && parts[1] == "administration" && parts[3] != "batch-delete" {
			table := map[string]string{"users": "iam_accounts", "roles": "iam_roles", "menus": "iam_menus"}[parts[2]]
			if table != "" {
				revision, err := strconv.ParseInt(strings.Trim(r.Header.Get("If-Match"), "\""), 10, 64)
				if err != nil || revision < 1 {
					writeProblem(w, problem(transport.Conflict, "REVISION_REQUIRED", "Refresh the record before editing", traceID(r), http.StatusConflict))
					return
				}
				ctx = context.WithValue(ctx, revisionKey{}, expectedRevision{table: table, id: parts[3], revision: revision})
			}
		}
		validated.ServeHTTP(w, r.WithContext(ctx))
	}), nil
}

func replacementCookie(issued session.Issued) *string {
	if !issued.Rotated {
		return nil
	}
	value := (&http.Cookie{Name: session.CookieName, Value: issued.Token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}).String()
	return &value
}

func requestHTTP(ctx context.Context) httpContext {
	value, _ := ctx.Value(httpContextKey{}).(httpContext)
	return value
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
func responseHeaders(ctx context.Context) (*string, *string) {
	value := requestHTTP(ctx)
	csrf := value.csrf
	return &csrf, value.cookie
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

func transportUser(value User) transport.User {
	roles := append([]string{}, value.RoleIDs...)
	return transport.User{Revision: ptr(int(value.Revision)), Id: value.ID, Username: value.Username, DisplayName: value.DisplayName, Email: openapi_types.Email(value.Email), Disabled: value.Disabled, RoleIds: roles}
}
func transportRole(value Role) transport.Role {
	return transport.Role{Revision: ptr(int(value.Revision)), Id: value.ID, Key: value.Key, Name: value.Name, DataScope: transport.DataScope(value.Scope), Enabled: value.Enabled, Protected: value.Protected, PermissionCodes: append([]string{}, value.PermissionCodes...), MenuIds: append([]string{}, value.MenuIDs...)}
}
func transportMenu(value Menu) transport.Menu {
	return transport.Menu{Id: value.ID, Key: value.Key, Label: value.Label, Path: value.Path, PermissionCode: value.PermissionCode, SortOrder: value.SortOrder, Protected: value.Protected, ParentId: &value.ParentID, Kind: ptr(transport.MenuKind(value.Kind)), RouteKey: &value.RouteKey, Icon: &value.Icon, Enabled: &value.Enabled, Hidden: &value.Hidden, Revision: ptr(int(value.Revision))}
}
func transportManifest(value authorization.Manifest) transport.CapabilityManifest {
	result := transport.CapabilityManifest{DataScope: transport.DataScope(value.Scope), PermissionCodes: append([]string{}, value.Permissions...), Menus: []transport.CapabilityMenu{}}
	for _, menu := range value.Menus {
		result.Menus = append(result.Menus, transport.CapabilityMenu{Id: &menu.ID, ParentId: &menu.ParentID, Kind: ptr(transport.CapabilityMenuKind(menu.Kind)), RouteKey: &menu.RouteKey, Icon: &menu.Icon, Key: menu.Key, Label: menu.Label, Path: menu.Path, PermissionCode: menu.PermissionCode, SortOrder: menu.SortOrder})
	}
	return result
}

func ptr[T any](v T) *T { return &v }
