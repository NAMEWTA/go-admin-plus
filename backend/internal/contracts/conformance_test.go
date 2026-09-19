package contracts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type probeHandler struct{}

var _ StrictServerInterface = probeHandler{}

func (probeHandler) ProbeContract(_ context.Context, request ProbeContractRequestObject) (ProbeContractResponseObject, error) {
	switch request.Body.Value {
	case "validation":
		return probeProblemResponse(Validation), nil
	case "authentication":
		return probeProblemResponse(Authentication), nil
	case "authorization":
		return probeProblemResponse(Authorization), nil
	case "not_found":
		return probeProblemResponse(NotFound), nil
	case "conflict":
		return probeProblemResponse(Conflict), nil
	case "internal":
		return nil, errors.New("pq: secret=database-password at /var/lib/app/database.go:42")
	}
	return ProbeContract200JSONResponse{Value: request.Body.Value}, nil
}

func probeProblemResponse(category ProblemCategory) ProbeContractResponseObject {
	problem, _ := NewStableProblem(category, "0123456789abcdef")
	switch category {
	case Validation:
		return ProbeContract400ApplicationProblemPlusJSONResponse{
			ValidationProblemApplicationProblemPlusJSONResponse: ValidationProblemApplicationProblemPlusJSONResponse(problem),
		}
	case Authentication:
		return ProbeContract401ApplicationProblemPlusJSONResponse{
			AuthenticationProblemApplicationProblemPlusJSONResponse: AuthenticationProblemApplicationProblemPlusJSONResponse(problem),
		}
	case Authorization:
		return ProbeContract403ApplicationProblemPlusJSONResponse{
			AuthorizationProblemApplicationProblemPlusJSONResponse: AuthorizationProblemApplicationProblemPlusJSONResponse(problem),
		}
	case NotFound:
		return ProbeContract404ApplicationProblemPlusJSONResponse{
			NotFoundProblemApplicationProblemPlusJSONResponse: NotFoundProblemApplicationProblemPlusJSONResponse(problem),
		}
	case Conflict:
		return ProbeContract409ApplicationProblemPlusJSONResponse{
			ConflictProblemApplicationProblemPlusJSONResponse: ConflictProblemApplicationProblemPlusJSONResponse(problem),
		}
	default:
		return ProbeContract500ApplicationProblemPlusJSONResponse{
			InternalProblemApplicationProblemPlusJSONResponse: InternalProblemApplicationProblemPlusJSONResponse(problem),
		}
	}
}

func fixedTraceID(*http.Request) string { return "0123456789abcdef" }

func decodeProblemResponse(t *testing.T, response *httptest.ResponseRecorder) (Problem, []byte) {
	t.Helper()
	raw := append([]byte(nil), response.Body.Bytes()...)
	var problem Problem
	if err := json.Unmarshal(raw, &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	return problem, raw
}

func assertNoSensitiveResponseData(t *testing.T, raw []byte) {
	t.Helper()
	lower := strings.ToLower(string(raw))
	for _, forbidden := range []string{
		"pq:", "select password from", "stack trace", "/var/", `c:\\`,
		"database.go", "secret", "password", "session=raw",
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("problem response leaked %q: %s", forbidden, raw)
		}
	}
}

func TestStableProblemCategories(t *testing.T) {
	tests := []struct {
		category ProblemCategory
		status   int
		code     string
		typeID   string
	}{
		{Validation, http.StatusBadRequest, "REQUEST_INVALID", "urn:go-admin-plus:problem:validation"},
		{Authentication, http.StatusUnauthorized, "SESSION_REQUIRED", "urn:go-admin-plus:problem:authentication"},
		{Authorization, http.StatusForbidden, "PERMISSION_DENIED", "urn:go-admin-plus:problem:authorization"},
		{NotFound, http.StatusNotFound, "RESOURCE_NOT_FOUND", "urn:go-admin-plus:problem:not-found"},
		{Conflict, http.StatusConflict, "RESOURCE_CONFLICT", "urn:go-admin-plus:problem:conflict"},
		{Internal, http.StatusInternalServerError, "INTERNAL_ERROR", "urn:go-admin-plus:problem:internal"},
	}

	for _, test := range tests {
		t.Run(string(test.category), func(t *testing.T) {
			problem, err := NewStableProblem(test.category, "unsafe trace value")
			if err != nil {
				t.Fatalf("construct stable problem: %v", err)
			}
			if problem.Status != test.status || problem.Code != test.code || problem.Type != test.typeID {
				t.Fatalf("problem = %#v, want status=%d code=%s type=%s", problem, test.status, test.code, test.typeID)
			}
			if problem.Category != test.category || problem.TraceId != fallbackTraceID {
				t.Fatalf("problem category/trace = %q/%q", problem.Category, problem.TraceId)
			}
		})
	}

	if _, err := NewStableProblem(ProblemCategory("database"), fixedTraceID(nil)); err == nil {
		t.Fatal("unsupported public category unexpectedly accepted")
	}
}

func TestGeneratedStrictHandlerUsesChiTransport(t *testing.T) {
	handler, err := NewHandler(probeHandler{}, fixedTraceID, nil)
	if err != nil {
		t.Fatalf("create handler: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/contract/probe?page=2&pageSize=40", strings.NewReader(`{"value":"roundtrip"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("content type = %q, want application/json", contentType)
	}
	var body ContractProbeResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Value != "roundtrip" {
		t.Fatalf("value = %q, want roundtrip", body.Value)
	}
}

func TestStrictTransportNormalizesRequestAndInternalFailures(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		body     string
		status   int
		category ProblemCategory
	}{
		{name: "invalid request", url: "/contract/probe", body: `{`, status: http.StatusBadRequest, category: Validation},
		{name: "invalid query", url: "/contract/probe?page=invalid", body: `{"value":"ok"}`, status: http.StatusBadRequest, category: Validation},
		{name: "internal failure", url: "/contract/probe", body: `{"value":"internal"}`, status: http.StatusInternalServerError, category: Internal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, err := NewHandler(probeHandler{}, fixedTraceID, nil)
			if err != nil {
				t.Fatalf("create handler: %v", err)
			}
			request := httptest.NewRequest(http.MethodPost, test.url, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.status, response.Body.String())
			}
			if contentType := response.Header().Get("Content-Type"); contentType != "application/problem+json" {
				t.Fatalf("content type = %q, want application/problem+json", contentType)
			}
			problem, raw := decodeProblemResponse(t, response)
			if problem.Category != test.category {
				t.Fatalf("category = %q, want %q", problem.Category, test.category)
			}
			assertNoSensitiveResponseData(t, raw)
		})
	}
}

func TestStrictTransportValidatesSchemaMediaTypeAndBodyLimit(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		contentType string
		body        string
	}{
		{name: "missing required value", url: "/contract/probe", contentType: "application/json", body: `{}`},
		{name: "empty value", url: "/contract/probe", contentType: "application/json", body: `{"value":""}`},
		{name: "unknown property", url: "/contract/probe", contentType: "application/json", body: `{"value":"ok","unknown":true}`},
		{name: "value exceeds schema maximum", url: "/contract/probe", contentType: "application/json", body: `{"value":"` + strings.Repeat("x", 65) + `"}`},
		{name: "page below minimum", url: "/contract/probe?page=0", contentType: "application/json", body: `{"value":"ok"}`},
		{name: "page size above maximum", url: "/contract/probe?pageSize=201", contentType: "application/json", body: `{"value":"ok"}`},
		{name: "wrong content type", url: "/contract/probe", contentType: "text/plain", body: `{"value":"ok"}`},
		{name: "body exceeds transport limit", url: "/contract/probe", contentType: "application/json", body: `{"value":"` + strings.Repeat("x", int(DefaultMaxRequestBodyBytes)) + `"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, err := NewHandler(probeHandler{}, fixedTraceID, nil)
			if err != nil {
				t.Fatalf("create handler: %v", err)
			}
			request := httptest.NewRequest(http.MethodPost, test.url, strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusBadRequest, response.Body.String())
			}
			var problem Problem
			if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
				t.Fatalf("decode problem: %v", err)
			}
			if problem.Category != Validation || problem.Code != "REQUEST_INVALID" {
				t.Fatalf("problem = %#v, want stable validation category", problem)
			}
		})
	}
}

func TestStrictTransportReturnsEveryDeclaredProblemCategory(t *testing.T) {
	tests := []struct {
		category ProblemCategory
		status   int
		code     string
	}{
		{Validation, http.StatusBadRequest, "REQUEST_INVALID"},
		{Authentication, http.StatusUnauthorized, "SESSION_REQUIRED"},
		{Authorization, http.StatusForbidden, "PERMISSION_DENIED"},
		{NotFound, http.StatusNotFound, "RESOURCE_NOT_FOUND"},
		{Conflict, http.StatusConflict, "RESOURCE_CONFLICT"},
		{Internal, http.StatusInternalServerError, "INTERNAL_ERROR"},
	}

	for _, test := range tests {
		t.Run(string(test.category), func(t *testing.T) {
			handler, err := NewHandler(probeHandler{}, fixedTraceID, nil)
			if err != nil {
				t.Fatalf("create handler: %v", err)
			}
			request := httptest.NewRequest(
				http.MethodPost,
				"/contract/probe",
				strings.NewReader(`{"value":"`+string(test.category)+`"}`),
			)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.status, response.Body.String())
			}
			if contentType := response.Header().Get("Content-Type"); contentType != "application/problem+json" {
				t.Fatalf("content type = %q, want application/problem+json", contentType)
			}
			problem, raw := decodeProblemResponse(t, response)
			if problem.Category != test.category || problem.Code != test.code || problem.Status != test.status {
				t.Fatalf("problem = %#v, want category=%s code=%s status=%d", problem, test.category, test.code, test.status)
			}
			assertNoSensitiveResponseData(t, raw)
		})
	}
}

func TestGeneratedProblemResponseKeepsStableCategory(t *testing.T) {
	response := httptest.NewRecorder()
	problem := ProbeContract500ApplicationProblemPlusJSONResponse{
		InternalProblemApplicationProblemPlusJSONResponse: InternalProblemApplicationProblemPlusJSONResponse{
			Type: "urn:go-admin-plus:problem:internal", Title: "Internal server error",
			Status: http.StatusInternalServerError, Category: Internal,
			Code: "INTERNAL_ERROR", TraceId: "0123456789abcdef",
		},
	}
	if err := problem.VisitProbeContractResponse(response); err != nil {
		t.Fatalf("write problem response: %v", err)
	}
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if strings.Contains(strings.ToLower(response.Body.String()), "sql") {
		t.Fatalf("problem response leaked internal detail: %s", response.Body.String())
	}
}
