package contracts

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"
)

const fallbackTraceID = "0000000000000000"

// DefaultMaxRequestBodyBytes bounds generated JSON transport requests before schema validation.
const DefaultMaxRequestBodyBytes int64 = 1 << 20

//go:embed openapi.json
var canonicalOpenAPI []byte

var validTraceID = regexp.MustCompile(`^[a-f0-9]{16,64}$`)

// TraceIDProvider returns the safe correlation identifier exposed in a Problem response.
type TraceIDProvider func(*http.Request) string

// RequestValidatorOptions keeps module authentication pluggable without exposing unsafe error writers.
type RequestValidatorOptions struct {
	MaxBodyBytes       int64
	AuthenticationFunc openapi3filter.AuthenticationFunc
}

type stableProblemDefinition struct {
	status int
	typeID string
	title  string
	code   string
}

var stableProblems = map[ProblemCategory]stableProblemDefinition{
	Validation:     {http.StatusBadRequest, "urn:go-admin-plus:problem:validation", "Request validation failed", "REQUEST_INVALID"},
	Authentication: {http.StatusUnauthorized, "urn:go-admin-plus:problem:authentication", "Authentication required", "SESSION_REQUIRED"},
	Authorization:  {http.StatusForbidden, "urn:go-admin-plus:problem:authorization", "Permission denied", "PERMISSION_DENIED"},
	NotFound:       {http.StatusNotFound, "urn:go-admin-plus:problem:not-found", "Resource not found", "RESOURCE_NOT_FOUND"},
	Conflict:       {http.StatusConflict, "urn:go-admin-plus:problem:conflict", "Resource conflict", "RESOURCE_CONFLICT"},
	Internal:       {http.StatusInternalServerError, "urn:go-admin-plus:problem:internal", "Internal server error", "INTERNAL_ERROR"},
}

// NewStableProblem constructs a declared public failure without accepting raw internal detail.
func NewStableProblem(category ProblemCategory, traceID string) (Problem, error) {
	definition, ok := stableProblems[category]
	if !ok {
		return Problem{}, errors.New("unsupported public problem category")
	}
	return Problem{
		Type: definition.typeID, Title: definition.title, Status: definition.status,
		Category: category, Code: definition.code, TraceId: safeTraceID(traceID),
	}, nil
}

// NewHandler builds the generated Chi and strict layers with the stable Problem contract.
func NewHandler(
	implementation StrictServerInterface,
	traceID TraceIDProvider,
	middlewares []StrictMiddlewareFunc,
) (http.Handler, error) {
	if implementation == nil {
		return nil, errors.New("strict server implementation is required")
	}
	if traceID == nil {
		return nil, errors.New("trace ID provider is required")
	}

	strict := NewStrictHandlerWithOptions(implementation, middlewares, StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeValidationProblem(w, safeTraceID(traceID(r)))
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			problem, _ := NewStableProblem(Internal, traceID(r))
			writeProblem(w, problem)
		},
	})

	handler := HandlerWithOptions(strict, ChiServerOptions{
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeValidationProblem(w, safeTraceID(traceID(r)))
		},
	})

	return NewRequestValidator(canonicalOpenAPI, handler, traceID, RequestValidatorOptions{
		MaxBodyBytes: DefaultMaxRequestBodyBytes,
	})
}

// NewRequestValidator enforces a generated OpenAPI document before a module transport runs.
func NewRequestValidator(
	document []byte,
	handler http.Handler,
	traceID TraceIDProvider,
	options RequestValidatorOptions,
) (http.Handler, error) {
	if len(document) == 0 {
		return nil, errors.New("OpenAPI contract is required")
	}
	if handler == nil {
		return nil, errors.New("validated HTTP handler is required")
	}
	if traceID == nil {
		return nil, errors.New("trace ID provider is required")
	}
	if options.MaxBodyBytes < 1 {
		return nil, errors.New("request body limit must be positive")
	}

	specification, err := openapi3.NewLoader().LoadFromData(document)
	if err != nil {
		return nil, errors.New("load generated OpenAPI contract")
	}
	validator := nethttpmiddleware.OapiRequestValidatorWithOptions(specification, &nethttpmiddleware.Options{
		DoNotValidateServers: true,
		Options: openapi3filter.Options{
			AuthenticationFunc: options.AuthenticationFunc,
		},
		ErrorHandlerWithOpts: func(_ context.Context, _ error, w http.ResponseWriter, r *http.Request, options nethttpmiddleware.ErrorHandlerOpts) {
			category := Validation
			switch options.StatusCode {
			case http.StatusUnauthorized:
				category = Authentication
			case http.StatusForbidden:
				category = Authorization
			case http.StatusNotFound:
				category = NotFound
			}
			problem, _ := NewStableProblem(category, traceID(r))
			writeProblem(w, problem)
		},
	})(handler)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, options.MaxBodyBytes)
		}
		validator.ServeHTTP(w, r)
	}), nil
}

func safeTraceID(traceID string) string {
	if validTraceID.MatchString(traceID) {
		return traceID
	}
	return fallbackTraceID
}

func writeValidationProblem(w http.ResponseWriter, traceID string) {
	problem, _ := NewStableProblem(Validation, traceID)
	detail := "Request does not match the API contract."
	problem.Detail = &detail
	writeProblem(w, problem)
}

func writeProblem(w http.ResponseWriter, problem Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(problem.Status)
	_ = json.NewEncoder(w).Encode(problem)
}
