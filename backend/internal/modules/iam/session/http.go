package session

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/contracts"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/account"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session/protection"
	transport "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session/transport"
)

const CookieName = "__Host-go-admin-session"

var validTraceID = regexp.MustCompile(`^[a-f0-9]{16,64}$`)

//go:embed transport/openapi.json
var openAPIDocument []byte

type requestKey struct{}

type requestCredentials struct {
	token, csrf, trace string
	source             protection.Source
}

type HTTPServer struct{ service *Service }

func NewHTTPHandler(service *Service, traceID contracts.TraceIDProvider) (http.Handler, error) {
	if service == nil || traceID == nil {
		return nil, errors.New("iam handler dependencies are required")
	}
	server := &HTTPServer{service: service}
	middleware := func(next transport.StrictHandlerFunc, _ string) transport.StrictHandlerFunc {
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
			cookie, _ := r.Cookie(CookieName)
			credentials := requestCredentials{csrf: r.Header.Get("X-CSRF-Token"), trace: traceID(r)}
			// 默认只信任连接来源。可信反向代理由宿主在进入处理器前解析。
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}
			credentials.source, _ = protection.NewSource(host)
			if cookie != nil {
				credentials.token = cookie.Value
			}
			return next(context.WithValue(ctx, requestKey{}, credentials), w, r, request)
		}
	}
	strict := transport.NewStrictHandlerWithOptions(server, []transport.StrictMiddlewareFunc{middleware}, transport.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeHTTPProblem(w, transport.Problem(validationProblem(traceID(r))))
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeHTTPProblem(w, transport.Problem(internalProblem(traceID(r))))
		},
	})
	router := transport.HandlerWithOptions(strict, transport.ChiServerOptions{
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeHTTPProblem(w, transport.Problem(validationProblem(traceID(r))))
		},
	})
	validated, err := contracts.NewRequestValidator(openAPIDocument, router, traceID, contracts.RequestValidatorOptions{
		MaxBodyBytes: contracts.DefaultMaxRequestBodyBytes,
		// The service owns credential validation so missing CSRF remains a 403 instead of the
		// validator's generic 401 for every failed security scheme.
		AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
	})
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		validated.ServeHTTP(w, r)
	}), nil
}

func (s *HTTPServer) LoginIamSession(ctx context.Context, request transport.LoginIamSessionRequestObject) (transport.LoginIamSessionResponseObject, error) {
	if request.Body == nil {
		return loginProblem(ErrValidation, trace(ctx)), nil
	}
	issued, err := s.service.LoginFrom(ctx, LoginCommand{Username: request.Body.Username, Password: request.Body.Password, Source: credentials(ctx).source})
	if err != nil {
		return loginProblem(err, trace(ctx)), nil
	}
	cookie := sessionCookie(issued.Token, false)
	return transport.LoginIamSession200JSONResponse{Body: sessionResponse(issued), Headers: transport.LoginIamSession200ResponseHeaders{SetCookie: &cookie, XCSRFToken: issued.CSRF}}, nil
}

func (s *HTTPServer) GetCurrentIamSession(ctx context.Context, _ transport.GetCurrentIamSessionRequestObject) (transport.GetCurrentIamSessionResponseObject, error) {
	credentials := credentials(ctx)
	issued, err := s.service.Current(ctx, credentials.token)
	if errors.Is(err, ErrAuthentication) {
		return transport.GetCurrentIamSession401ApplicationProblemPlusJSONResponse{AuthenticationProblemApplicationProblemPlusJSONResponse: authProblem(credentials.trace)}, nil
	}
	if err != nil {
		return transport.GetCurrentIamSession500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(credentials.trace)}, nil
	}
	headers := transport.GetCurrentIamSession200ResponseHeaders{XCSRFToken: issued.CSRF}
	return transport.GetCurrentIamSession200JSONResponse{Body: sessionResponse(issued), Headers: headers}, nil
}

func (s *HTTPServer) HeartbeatIamSession(ctx context.Context, _ transport.HeartbeatIamSessionRequestObject) (transport.HeartbeatIamSessionResponseObject, error) {
	c := credentials(ctx)
	issued, err := s.service.Heartbeat(ctx, c.token, c.csrf)
	if errors.Is(err, ErrCSRF) {
		return transport.HeartbeatIamSession403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(c.trace)}, nil
	}
	if errors.Is(err, ErrAuthentication) {
		return transport.HeartbeatIamSession401ApplicationProblemPlusJSONResponse{AuthenticationProblemApplicationProblemPlusJSONResponse: authProblem(c.trace)}, nil
	}
	if err != nil {
		return transport.HeartbeatIamSession500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(c.trace)}, nil
	}
	return transport.HeartbeatIamSession200JSONResponse{
		Body: sessionResponse(issued),
		Headers: transport.HeartbeatIamSession200ResponseHeaders{
			XCSRFToken: issued.CSRF,
			SetCookie:  replacementCookie(issued),
		},
	}, nil
}

func (s *HTTPServer) RenewIamSession(ctx context.Context, _ transport.RenewIamSessionRequestObject) (transport.RenewIamSessionResponseObject, error) {
	c := credentials(ctx)
	issued, err := s.service.Renew(ctx, c.token, c.csrf)
	if errors.Is(err, ErrCSRF) {
		return transport.RenewIamSession403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(c.trace)}, nil
	}
	if errors.Is(err, ErrAuthentication) {
		return transport.RenewIamSession401ApplicationProblemPlusJSONResponse{AuthenticationProblemApplicationProblemPlusJSONResponse: authProblem(c.trace)}, nil
	}
	if err != nil {
		return transport.RenewIamSession500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(c.trace)}, nil
	}
	return transport.RenewIamSession200JSONResponse{
		Body: sessionResponse(issued),
		Headers: transport.RenewIamSession200ResponseHeaders{
			XCSRFToken: issued.CSRF,
			SetCookie:  replacementCookie(issued),
		},
	}, nil
}

func (s *HTTPServer) LogoutIamSession(ctx context.Context, _ transport.LogoutIamSessionRequestObject) (transport.LogoutIamSessionResponseObject, error) {
	c := credentials(ctx)
	err := s.service.Logout(ctx, c.token, c.csrf)
	if errors.Is(err, ErrCSRF) {
		return transport.LogoutIamSession403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(c.trace)}, nil
	}
	if errors.Is(err, ErrAuthentication) {
		return transport.LogoutIamSession401ApplicationProblemPlusJSONResponse{AuthenticationProblemApplicationProblemPlusJSONResponse: authProblem(c.trace)}, nil
	}
	if err != nil {
		return transport.LogoutIamSession500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(c.trace)}, nil
	}
	cookie := sessionCookie("", true)
	return transport.LogoutIamSession204Response{Headers: transport.LogoutIamSession204ResponseHeaders{SetCookie: &cookie}}, nil
}

func (s *HTTPServer) GetIamAccountProfile(ctx context.Context, _ transport.GetIamAccountProfileRequestObject) (transport.GetIamAccountProfileResponseObject, error) {
	c := credentials(ctx)
	issued, err := s.service.Profile(ctx, c.token)
	if errors.Is(err, ErrAuthentication) {
		return transport.GetIamAccountProfile401ApplicationProblemPlusJSONResponse{AuthenticationProblemApplicationProblemPlusJSONResponse: authProblem(c.trace)}, nil
	}
	if err != nil {
		return transport.GetIamAccountProfile500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(c.trace)}, nil
	}
	return transport.GetIamAccountProfile200JSONResponse{
		Body:    transportProfile(issued.Profile),
		Headers: transport.GetIamAccountProfile200ResponseHeaders{XCSRFToken: issued.CSRF, SetCookie: replacementCookie(issued)},
	}, nil
}

func (s *HTTPServer) UpdateIamAccountProfile(ctx context.Context, request transport.UpdateIamAccountProfileRequestObject) (transport.UpdateIamAccountProfileResponseObject, error) {
	c := credentials(ctx)
	if request.Body == nil {
		return transport.UpdateIamAccountProfile400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(c.trace)}, nil
	}
	issued, err := s.service.UpdateProfile(ctx, c.token, c.csrf, request.Body.DisplayName, string(request.Body.Email), request.Body.AvatarRef)
	if errors.Is(err, ErrValidation) {
		return transport.UpdateIamAccountProfile400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(c.trace)}, nil
	}
	if errors.Is(err, ErrCSRF) {
		return transport.UpdateIamAccountProfile403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(c.trace)}, nil
	}
	if errors.Is(err, ErrConflict) {
		return transport.UpdateIamAccountProfile409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(c.trace)}, nil
	}
	if errors.Is(err, ErrAuthentication) {
		return transport.UpdateIamAccountProfile401ApplicationProblemPlusJSONResponse{AuthenticationProblemApplicationProblemPlusJSONResponse: authProblem(c.trace)}, nil
	}
	if err != nil {
		return transport.UpdateIamAccountProfile500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(c.trace)}, nil
	}
	return transport.UpdateIamAccountProfile200JSONResponse{
		Body:    transportProfile(issued.Profile),
		Headers: transport.UpdateIamAccountProfile200ResponseHeaders{XCSRFToken: issued.CSRF, SetCookie: replacementCookie(issued)},
	}, nil
}

func (s *HTTPServer) ChangeIamAccountPassword(ctx context.Context, request transport.ChangeIamAccountPasswordRequestObject) (transport.ChangeIamAccountPasswordResponseObject, error) {
	c := credentials(ctx)
	if request.Body == nil {
		return transport.ChangeIamAccountPassword400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(c.trace)}, nil
	}
	err := s.service.ChangePassword(ctx, c.token, c.csrf, request.Body.CurrentPassword, request.Body.NewPassword)
	if errors.Is(err, ErrValidation) || errors.Is(err, ErrCredentials) {
		return transport.ChangeIamAccountPassword400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(c.trace)}, nil
	}
	if errors.Is(err, ErrCSRF) {
		return transport.ChangeIamAccountPassword403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(c.trace)}, nil
	}
	if errors.Is(err, ErrAuthentication) {
		return transport.ChangeIamAccountPassword401ApplicationProblemPlusJSONResponse{AuthenticationProblemApplicationProblemPlusJSONResponse: authProblem(c.trace)}, nil
	}
	if errors.Is(err, ErrConflict) {
		return transport.ChangeIamAccountPassword409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(c.trace)}, nil
	}
	if err != nil {
		return transport.ChangeIamAccountPassword500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(c.trace)}, nil
	}
	cookie := sessionCookie("", true)
	return transport.ChangeIamAccountPassword204Response{Headers: transport.ChangeIamAccountPassword204ResponseHeaders{SetCookie: &cookie}}, nil
}

func credentials(ctx context.Context) requestCredentials {
	value, _ := ctx.Value(requestKey{}).(requestCredentials)
	return value
}
func trace(ctx context.Context) string { return credentials(ctx).trace }

func sessionCookie(value string, clear bool) string {
	cookie := &http.Cookie{Name: CookieName, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}
	if clear {
		cookie.MaxAge = -1
	}
	return cookie.String()
}

func replacementCookie(issued Issued) *string {
	if !issued.Rotated {
		return nil
	}
	cookie := sessionCookie(issued.Token, false)
	return &cookie
}

func sessionResponse(value Issued) transport.SessionResponse {
	return transport.SessionResponse{CsrfToken: value.CSRF, Profile: transportProfile(value.Profile)}
}
func transportProfile(value account.Profile) transport.Profile {
	return transport.Profile{Id: value.ID, Username: value.Username, DisplayName: value.DisplayName, Email: openapi_types.Email(value.Email), AvatarRef: value.AvatarRef}
}

func problem(category transport.ProblemCategory, code, title, trace string, status int) transport.Problem {
	if !validTraceID.MatchString(trace) {
		trace = "0000000000000000"
	}
	return transport.Problem{Type: "urn:go-admin-plus:problem:" + code, Title: title, Status: status, Category: category, Code: strings.ToUpper(strings.ReplaceAll(code, "-", "_")), TraceId: trace}
}
func writeHTTPProblem(w http.ResponseWriter, value transport.Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(value.Status)
	_ = json.NewEncoder(w).Encode(value)
}
func authProblem(trace string) transport.AuthenticationProblemApplicationProblemPlusJSONResponse {
	value := problem(transport.Authentication, "authentication", "Authentication required", trace, 401)
	value.Code = "SESSION_REQUIRED"
	return transport.AuthenticationProblemApplicationProblemPlusJSONResponse(value)
}
func authorizationProblem(trace string) transport.AuthorizationProblemApplicationProblemPlusJSONResponse {
	value := problem(transport.Authorization, "authorization", "Request authorization failed", trace, 403)
	value.Code = "CSRF_REJECTED"
	return transport.AuthorizationProblemApplicationProblemPlusJSONResponse(value)
}
func validationProblem(trace string) transport.ValidationProblemApplicationProblemPlusJSONResponse {
	value := problem(transport.Validation, "validation", "Request validation failed", trace, 400)
	value.Code = "REQUEST_INVALID"
	return transport.ValidationProblemApplicationProblemPlusJSONResponse(value)
}
func conflictProblem(trace string) transport.ConflictProblemApplicationProblemPlusJSONResponse {
	value := problem(transport.Conflict, "conflict", "Resource conflict", trace, 409)
	value.Code = "RESOURCE_CONFLICT"
	return transport.ConflictProblemApplicationProblemPlusJSONResponse(value)
}
func internalProblem(trace string) transport.InternalProblemApplicationProblemPlusJSONResponse {
	value := problem(transport.Internal, "internal", "Internal server error", trace, 500)
	value.Code = "INTERNAL_ERROR"
	return transport.InternalProblemApplicationProblemPlusJSONResponse(value)
}

func loginProblem(err error, trace string) transport.LoginIamSessionResponseObject {
	if errors.Is(err, ErrValidation) {
		return transport.LoginIamSession400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(trace)}
	}
	if errors.Is(err, ErrCredentials) {
		return transport.LoginIamSession401ApplicationProblemPlusJSONResponse{AuthenticationProblemApplicationProblemPlusJSONResponse: authProblem(trace)}
	}
	if retryAfter, limited := RetryAfter(err); limited {
		seconds := int((retryAfter + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		if seconds > 3600 {
			seconds = 3600
		}
		value := problem(transport.Authentication, "login-rate-limited", "Login temporarily unavailable", trace, http.StatusTooManyRequests)
		value.Code = "LOGIN_RATE_LIMITED"
		return transport.LoginIamSession429ApplicationProblemPlusJSONResponse{
			RateLimitProblemApplicationProblemPlusJSONResponse: transport.RateLimitProblemApplicationProblemPlusJSONResponse{
				Body: value, Headers: transport.RateLimitProblemResponseHeaders{RetryAfter: seconds},
			},
		}
	}
	return transport.LoginIamSession500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(trace)}
}
