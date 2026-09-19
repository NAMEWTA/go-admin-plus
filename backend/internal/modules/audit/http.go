package audit

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/getkin/kin-openapi/openapi3filter"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/contracts"
	transport "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/audit/transport"
)

//go:embed transport/openapi.json
var openAPIDocument []byte

var (
	validTraceID = regexp.MustCompile(`^[a-f0-9]{16,64}$`)
	validCSRF    = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

type RequestFailure string

const (
	RequestAuthorized           RequestFailure = "authorized"
	RequestAuthenticationFailed RequestFailure = "authentication"
	RequestAuthorizationFailed  RequestFailure = "authorization"
	RequestInternalFailed       RequestFailure = "internal"
)

// AuthorizedRequest carries response-only session rotation material without exposing it to
// serialization, formatting, structured logs, Audit facts, or observers.
type AuthorizedRequest struct {
	Principal Principal
	csrf      string
	cookie    *string
}

func NewAuthorizedRequest(principal Principal, csrf string, replacementCookie *string) (AuthorizedRequest, error) {
	if validatePrincipal(principal) != nil || !validCSRF.MatchString(csrf) || replacementCookie != nil && !validCookieHeader(*replacementCookie) {
		return AuthorizedRequest{}, ErrInvalidArgument
	}
	return AuthorizedRequest{Principal: principal, csrf: csrf, cookie: cloneString(replacementCookie)}, nil
}

func (request AuthorizedRequest) String() string {
	return fmt.Sprintf("audit.AuthorizedRequest{Principal:%q, CSRF:[redacted], ReplacementCookie:[redacted]}", request.Principal.ID)
}

func (request AuthorizedRequest) GoString() string { return request.String() }

func (request AuthorizedRequest) LogValue() slog.Value {
	return slog.GroupValue(slog.String("principal", request.Principal.ID), slog.String("csrf", "[redacted]"), slog.String("replacement_cookie", "[redacted]"))
}

// RequestAuthorizer authenticates the session, validates mutation CSRF, and returns only a fixed
// failure category. Implementations must never return raw credential or infrastructure errors.
type RequestAuthorizer interface {
	AuthorizeRequest(context.Context, *http.Request) (AuthorizedRequest, RequestFailure)
}

type httpIdentity struct {
	authorized AuthorizedRequest
	failure    RequestFailure
	trace      string
}

type httpIdentityKey struct{}

type HTTPServer struct{ service *Service }

func NewHTTPHandler(service *Service, authorizer RequestAuthorizer, traceID contracts.TraceIDProvider) (http.Handler, error) {
	if service == nil || authorizer == nil || traceID == nil {
		return nil, errors.New("audit handler dependencies are required")
	}
	server := &HTTPServer{service: service}
	middleware := func(next transport.StrictHandlerFunc, _ string) transport.StrictHandlerFunc {
		return func(ctx context.Context, w http.ResponseWriter, request *http.Request, input any) (any, error) {
			authorized, failure := authorizer.AuthorizeRequest(ctx, request)
			if !validFailure(failure) || failure == RequestAuthorized && !authorized.valid() ||
				failure != RequestAuthorized && authorized != (AuthorizedRequest{}) && !authorized.valid() {
				failure = RequestInternalFailed
				authorized = AuthorizedRequest{}
			}
			identity := httpIdentity{authorized: authorized, failure: failure, trace: safeTrace(traceID(request))}
			return next(context.WithValue(ctx, httpIdentityKey{}, identity), w, request, input)
		}
	}
	strict := transport.NewStrictHandlerWithOptions(server, []transport.StrictMiddlewareFunc{middleware}, transport.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, request *http.Request, _ error) {
			writeHTTPProblem(w, problem(transport.Validation, "REQUEST_INVALID", "Request validation failed", 400, safeTrace(traceID(request))))
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, request *http.Request, _ error) {
			writeHTTPProblem(w, problem(transport.Internal, "INTERNAL_ERROR", "Internal server error", 500, safeTrace(traceID(request))))
		},
	})
	router := transport.HandlerWithOptions(strict, transport.ChiServerOptions{ErrorHandlerFunc: func(w http.ResponseWriter, request *http.Request, _ error) {
		writeHTTPProblem(w, problem(transport.Validation, "REQUEST_INVALID", "Request validation failed", 400, safeTrace(traceID(request))))
	}})
	validated, err := contracts.NewRequestValidator(openAPIDocument, router, traceID, contracts.RequestValidatorOptions{MaxBodyBytes: contracts.DefaultMaxRequestBodyBytes, AuthenticationFunc: openapi3filter.NoopAuthenticationFunc})
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		validated.ServeHTTP(w, request)
	}), nil
}

func (server *HTTPServer) ListAuditRecords(ctx context.Context, request transport.ListAuditRecordsRequestObject) (transport.ListAuditRecordsResponseObject, error) {
	identity := requestIdentity(ctx)
	if response := listFailure(identity); response != nil {
		return response, nil
	}
	filter := Filter{Page: request.Params.Page, PageSize: request.Params.PageSize}
	if request.Params.Kind != nil {
		filter.Kind = Kind(*request.Params.Kind)
	}
	if request.Params.Action != nil {
		filter.Action = string(*request.Params.Action)
	}
	if request.Params.Outcome != nil {
		filter.Outcome = Outcome(*request.Params.Outcome)
	}
	if request.Params.Source != nil {
		filter.Source = Source(*request.Params.Source)
	}
	if request.Params.From != nil {
		filter.From = *request.Params.From
	}
	if request.Params.To != nil {
		filter.To = *request.Params.To
	}
	page, err := server.service.List(ctx, identity.authorized.Principal, filter)
	if errors.Is(err, ErrInvalidArgument) {
		return transport.ListAuditRecords400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(identity.trace)}, nil
	}
	if errors.Is(err, ErrForbidden) {
		return transport.ListAuditRecords403ApplicationProblemPlusJSONResponse{AuthorizedAuthorizationProblemApplicationProblemPlusJSONResponse: authorizedAuthorizationProblem(identity)}, nil
	}
	if err != nil {
		return transport.ListAuditRecords500ApplicationProblemPlusJSONResponse{AuthorizedInternalProblemApplicationProblemPlusJSONResponse: authorizedInternalProblem(identity)}, nil
	}
	return transport.ListAuditRecords200JSONResponse{Body: transportPage(page), Headers: transport.ListAuditRecords200ResponseHeaders{XCSRFToken: identity.authorized.csrf, SetCookie: cloneString(identity.authorized.cookie)}}, nil
}

func (server *HTTPServer) GetAuditRecord(ctx context.Context, request transport.GetAuditRecordRequestObject) (transport.GetAuditRecordResponseObject, error) {
	identity := requestIdentity(ctx)
	if response := detailFailure(identity); response != nil {
		return response, nil
	}
	fact, err := server.service.Detail(ctx, identity.authorized.Principal, request.Id)
	if errors.Is(err, ErrInvalidArgument) {
		return transport.GetAuditRecord400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(identity.trace)}, nil
	}
	if errors.Is(err, ErrForbidden) {
		return transport.GetAuditRecord403ApplicationProblemPlusJSONResponse{AuthorizedAuthorizationProblemApplicationProblemPlusJSONResponse: authorizedAuthorizationProblem(identity)}, nil
	}
	if errors.Is(err, ErrNotFound) {
		return transport.GetAuditRecord404ApplicationProblemPlusJSONResponse{NotFoundProblemApplicationProblemPlusJSONResponse: notFoundProblem(identity.trace)}, nil
	}
	if err != nil {
		return transport.GetAuditRecord500ApplicationProblemPlusJSONResponse{AuthorizedInternalProblemApplicationProblemPlusJSONResponse: authorizedInternalProblem(identity)}, nil
	}
	return transport.GetAuditRecord200JSONResponse{Body: transportFact(fact), Headers: transport.GetAuditRecord200ResponseHeaders{XCSRFToken: identity.authorized.csrf, SetCookie: cloneString(identity.authorized.cookie)}}, nil
}

func (server *HTTPServer) CleanupAuditRecords(ctx context.Context, request transport.CleanupAuditRecordsRequestObject) (transport.CleanupAuditRecordsResponseObject, error) {
	identity := requestIdentity(ctx)
	if response := cleanupFailure(identity); response != nil {
		return response, nil
	}
	if request.Body == nil {
		return transport.CleanupAuditRecords400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(identity.trace)}, nil
	}
	result, err := server.service.Cleanup(ctx, identity.authorized.Principal, CleanupCommand{Before: request.Body.Before, Confirmation: string(request.Body.Confirmation)})
	if errors.Is(err, ErrInvalidArgument) {
		return transport.CleanupAuditRecords400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(identity.trace)}, nil
	}
	if errors.Is(err, ErrForbidden) {
		return transport.CleanupAuditRecords403ApplicationProblemPlusJSONResponse{AuthorizedAuthorizationProblemApplicationProblemPlusJSONResponse: authorizedAuthorizationProblem(identity)}, nil
	}
	if errors.Is(err, ErrRetentionProtected) {
		return transport.CleanupAuditRecords409ApplicationProblemPlusJSONResponse{AuthorizedConflictProblemApplicationProblemPlusJSONResponse: authorizedConflictProblem(identity)}, nil
	}
	if err != nil {
		return transport.CleanupAuditRecords500ApplicationProblemPlusJSONResponse{AuthorizedInternalProblemApplicationProblemPlusJSONResponse: authorizedInternalProblem(identity)}, nil
	}
	return transport.CleanupAuditRecords200JSONResponse{Body: transport.CleanupAuditResponse{Deleted: result.Deleted, MoreEligible: result.MoreEligible}, Headers: transport.CleanupAuditRecords200ResponseHeaders{XCSRFToken: identity.authorized.csrf, SetCookie: cloneString(identity.authorized.cookie)}}, nil
}

func listFailure(identity httpIdentity) transport.ListAuditRecordsResponseObject {
	switch identity.failure {
	case RequestAuthorized:
		return nil
	case RequestAuthenticationFailed:
		return transport.ListAuditRecords401ApplicationProblemPlusJSONResponse{AuthenticationProblemApplicationProblemPlusJSONResponse: authProblem(identity.trace)}
	case RequestAuthorizationFailed:
		return transport.ListAuditRecords403ApplicationProblemPlusJSONResponse{AuthorizedAuthorizationProblemApplicationProblemPlusJSONResponse: csrfAuthorizationProblem(identity)}
	default:
		return transport.ListAuditRecords500ApplicationProblemPlusJSONResponse{AuthorizedInternalProblemApplicationProblemPlusJSONResponse: authorizedInternalProblem(identity)}
	}
}

func detailFailure(identity httpIdentity) transport.GetAuditRecordResponseObject {
	switch identity.failure {
	case RequestAuthorized:
		return nil
	case RequestAuthenticationFailed:
		return transport.GetAuditRecord401ApplicationProblemPlusJSONResponse{AuthenticationProblemApplicationProblemPlusJSONResponse: authProblem(identity.trace)}
	case RequestAuthorizationFailed:
		return transport.GetAuditRecord403ApplicationProblemPlusJSONResponse{AuthorizedAuthorizationProblemApplicationProblemPlusJSONResponse: csrfAuthorizationProblem(identity)}
	default:
		return transport.GetAuditRecord500ApplicationProblemPlusJSONResponse{AuthorizedInternalProblemApplicationProblemPlusJSONResponse: authorizedInternalProblem(identity)}
	}
}

func cleanupFailure(identity httpIdentity) transport.CleanupAuditRecordsResponseObject {
	switch identity.failure {
	case RequestAuthorized:
		return nil
	case RequestAuthenticationFailed:
		return transport.CleanupAuditRecords401ApplicationProblemPlusJSONResponse{AuthenticationProblemApplicationProblemPlusJSONResponse: authProblem(identity.trace)}
	case RequestAuthorizationFailed:
		return transport.CleanupAuditRecords403ApplicationProblemPlusJSONResponse{AuthorizedAuthorizationProblemApplicationProblemPlusJSONResponse: csrfAuthorizationProblem(identity)}
	default:
		return transport.CleanupAuditRecords500ApplicationProblemPlusJSONResponse{AuthorizedInternalProblemApplicationProblemPlusJSONResponse: authorizedInternalProblem(identity)}
	}
}

func requestIdentity(ctx context.Context) httpIdentity {
	value, _ := ctx.Value(httpIdentityKey{}).(httpIdentity)
	return value
}

func (request AuthorizedRequest) valid() bool {
	return validatePrincipal(request.Principal) == nil && validCSRF.MatchString(request.csrf) && (request.cookie == nil || validCookieHeader(*request.cookie))
}

func validCookieHeader(value string) bool {
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	cookie, err := http.ParseSetCookie(value)
	if err != nil || cookie.Name != "__Host-go-admin-session" || !validCSRF.MatchString(cookie.Value) ||
		cookie.Path != "/" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode ||
		cookie.Domain != "" || len(cookie.Unparsed) != 0 {
		return false
	}
	seen := make(map[string]struct{}, 4)
	parts := strings.Split(value, ";")
	if len(parts) != 5 || !strings.HasPrefix(parts[0], "__Host-go-admin-session=") {
		return false
	}
	for _, raw := range parts[1:] {
		attribute := strings.TrimSpace(raw)
		name := attribute
		if index := strings.IndexByte(attribute, '='); index >= 0 {
			name = attribute[:index]
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if _, duplicate := seen[name]; duplicate {
			return false
		}
		seen[name] = struct{}{}
		switch name {
		case "path":
			if attribute != "Path=/" {
				return false
			}
		case "secure":
			if attribute != "Secure" {
				return false
			}
		case "httponly":
			if attribute != "HttpOnly" {
				return false
			}
		case "samesite":
			if attribute != "SameSite=Strict" {
				return false
			}
		default:
			return false
		}
	}
	return len(seen) == 4
}

func validFailure(failure RequestFailure) bool {
	return failure == RequestAuthorized || failure == RequestAuthenticationFailed || failure == RequestAuthorizationFailed || failure == RequestInternalFailed
}

func transportPage(value Page) transport.AuditPage {
	records := make([]transport.AuditFact, 0, len(value.Records))
	for _, fact := range value.Records {
		records = append(records, transportFact(fact))
	}
	return transport.AuditPage{Records: records, Total: value.Total, Page: value.Page, PageSize: value.PageSize}
}

func transportFact(value Fact) transport.AuditFact {
	return transport.AuditFact{EventId: value.EventID, TraceId: value.TraceID, Operation: value.Operation, Id: value.ID, Kind: transport.AuditKind(value.Kind), Action: transport.AuditAction(value.Action), Outcome: transport.AuditOutcome(value.Outcome), ActorType: transport.ActorType(value.ActorType), Source: transport.AuditSource(value.Source), Subject: value.Subject, ActorRef: cloneString(value.ActorRef), OccurredAt: value.OccurredAt}
}

func authorizedAuthorizationProblem(identity httpIdentity) transport.AuthorizedAuthorizationProblemApplicationProblemPlusJSONResponse {
	return transport.AuthorizedAuthorizationProblemApplicationProblemPlusJSONResponse{Body: problem(transport.Authorization, "PERMISSION_DENIED", "Permission denied", 403, identity.trace), Headers: authorizedAuthorizationHeaders(identity)}
}

func csrfAuthorizationProblem(identity httpIdentity) transport.AuthorizedAuthorizationProblemApplicationProblemPlusJSONResponse {
	return transport.AuthorizedAuthorizationProblemApplicationProblemPlusJSONResponse{Body: problem(transport.Authorization, "CSRF_REJECTED", "Request authorization failed", 403, identity.trace), Headers: authorizedAuthorizationHeaders(identity)}
}

func authorizedAuthorizationHeaders(identity httpIdentity) transport.AuthorizedAuthorizationProblemResponseHeaders {
	if !identity.authorized.valid() {
		return transport.AuthorizedAuthorizationProblemResponseHeaders{}
	}
	return transport.AuthorizedAuthorizationProblemResponseHeaders{XCSRFToken: cloneString(&identity.authorized.csrf), SetCookie: cloneString(identity.authorized.cookie)}
}

func authorizedConflictProblem(identity httpIdentity) transport.AuthorizedConflictProblemApplicationProblemPlusJSONResponse {
	return transport.AuthorizedConflictProblemApplicationProblemPlusJSONResponse{Body: problem(transport.Conflict, "AUDIT_RETENTION_PROTECTED", "Audit retention policy prevents cleanup", 409, identity.trace), Headers: transport.AuthorizedConflictProblemResponseHeaders{XCSRFToken: identity.authorized.csrf, SetCookie: cloneString(identity.authorized.cookie)}}
}

func authorizedInternalProblem(identity httpIdentity) transport.AuthorizedInternalProblemApplicationProblemPlusJSONResponse {
	var csrf *string
	if identity.authorized.valid() {
		csrf = cloneString(&identity.authorized.csrf)
	}
	return transport.AuthorizedInternalProblemApplicationProblemPlusJSONResponse{Body: problem(transport.Internal, "INTERNAL_ERROR", "Internal server error", 500, identity.trace), Headers: transport.AuthorizedInternalProblemResponseHeaders{XCSRFToken: csrf, SetCookie: cloneString(identity.authorized.cookie)}}
}

func safeTrace(trace string) string {
	if validTraceID.MatchString(trace) {
		return trace
	}
	return "0000000000000000"
}

func problem(category transport.ProblemCategory, code, title string, status int, trace string) transport.Problem {
	return transport.Problem{Type: "urn:go-admin-plus:problem:" + strings.ToLower(strings.ReplaceAll(code, "_", "-")), Title: title, Status: status, Category: category, Code: code, TraceId: safeTrace(trace)}
}

func authProblem(trace string) transport.AuthenticationProblemApplicationProblemPlusJSONResponse {
	return transport.AuthenticationProblemApplicationProblemPlusJSONResponse(problem(transport.Authentication, "SESSION_REQUIRED", "Authentication required", 401, trace))
}

func validationProblem(trace string) transport.ValidationProblemApplicationProblemPlusJSONResponse {
	return transport.ValidationProblemApplicationProblemPlusJSONResponse(problem(transport.Validation, "REQUEST_INVALID", "Request validation failed", 400, trace))
}

func notFoundProblem(trace string) transport.NotFoundProblemApplicationProblemPlusJSONResponse {
	return transport.NotFoundProblemApplicationProblemPlusJSONResponse(problem(transport.NotFound, "AUDIT_FACT_NOT_FOUND", "Audit fact not found", 404, trace))
}

func writeHTTPProblem(w http.ResponseWriter, value transport.Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(value.Status)
	_ = json.NewEncoder(w).Encode(value)
}
