package files

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/google/uuid"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/contracts"
	transport "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/transport"
)

const maximumMultipartOverheadBytes int64 = 64 * 1024

//go:embed transport/openapi.json
var openAPIDocument []byte

var (
	tracePattern     = regexp.MustCompile(`^[a-f0-9]{16,64}$`)
	csrfTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

type RequestAuthenticator interface {
	CookieName() string
	AuthorizeRequest(context.Context, string, string, bool) (RequestIdentity, error)
}

type RequestIdentity struct {
	ActorID, CSRF     string
	ReplacementCookie *string
}

type requestContextKey struct{}
type requestContext struct {
	actorID, csrf, trace string
	cookie               *string
}

type HTTPServer struct{ service *Service }

func NewHTTPHandler(service *Service, authenticator RequestAuthenticator, traceID contracts.TraceIDProvider) (http.Handler, error) {
	if service == nil || authenticator == nil || authenticator.CookieName() == "" || traceID == nil {
		return nil, errors.New("files HTTP dependencies are required")
	}
	server := &HTTPServer{service: service}
	strict := transport.NewStrictHandlerWithOptions(server, nil, transport.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, request *http.Request, _ error) {
			writeProblem(w, makeProblem(transport.Validation, "REQUEST_INVALID", "Request validation failed", traceID(request), http.StatusBadRequest))
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, request *http.Request, _ error) {
			writeProblem(w, makeProblem(transport.Internal, "INTERNAL_ERROR", "Internal server error", traceID(request), http.StatusInternalServerError))
		},
	})
	router := transport.HandlerWithOptions(strict, transport.ChiServerOptions{ErrorHandlerFunc: func(w http.ResponseWriter, request *http.Request, _ error) {
		writeProblem(w, makeProblem(transport.Validation, "REQUEST_INVALID", "Request validation failed", traceID(request), http.StatusBadRequest))
	}})
	validated, err := contracts.NewRequestValidator(openAPIDocument, router, traceID, contracts.RequestValidatorOptions{
		MaxBodyBytes: contracts.DefaultMaxRequestBodyBytes, AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
	})
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		cookie, _ := request.Cookie(authenticator.CookieName())
		token := ""
		if cookie != nil {
			token = cookie.Value
		}
		mutation := request.Method != http.MethodGet && request.Method != http.MethodHead
		identity, authErr := authenticator.AuthorizeRequest(request.Context(), token, request.Header.Get("X-CSRF-Token"), mutation)
		if authErr != nil {
			writeAuthenticationFailure(w, authErr, traceID(request))
			return
		}
		if !validRequestIdentity(identity, authenticator.CookieName()) {
			writeProblem(w, makeProblem(transport.Internal, "INTERNAL_ERROR", "Internal server error", traceID(request), http.StatusInternalServerError))
			return
		}
		w.Header().Set("X-CSRF-Token", identity.CSRF)
		if identity.ReplacementCookie != nil {
			w.Header().Set("Set-Cookie", *identity.ReplacementCookie)
		}
		value := requestContext{actorID: identity.ActorID, csrf: identity.CSRF, cookie: identity.ReplacementCookie, trace: safeTrace(traceID(request))}
		request = request.WithContext(context.WithValue(request.Context(), requestContextKey{}, value))
		if request.Method == http.MethodPost && request.URL.Path == "/files/objects" {
			server.serveUpload(w, request)
			return
		}
		validated.ServeHTTP(w, request)
	}), nil
}

func (server *HTTPServer) ListFiles(ctx context.Context, request transport.ListFilesRequestObject) (transport.ListFilesResponseObject, error) {
	identity := requestValue(ctx)
	query := ListQuery{Page: request.Params.Page, PageSize: request.Params.PageSize}
	if request.Params.Search != nil {
		query.Search = *request.Params.Search
	}
	if request.Params.Sort != nil {
		query.Sort = string(*request.Params.Sort)
	}
	if request.Params.Direction != nil {
		query.Direction = string(*request.Params.Direction)
	}
	page, err := server.service.List(ctx, identity.actorID, query)
	if err != nil {
		return listProblem(err, identity), nil
	}
	return transport.ListFiles200JSONResponse{Body: transport.FilePage{Rows: transportMetadataRows(page.Rows), Total: page.Total}, Headers: transport.ListFiles200ResponseHeaders{XCSRFToken: identity.csrf, SetCookie: identity.cookie}}, nil
}

// The composed handler intercepts this operation before schema middleware to retain streaming. The
// strict method remains complete for generated transport conformance and module-level probes.
func (server *HTTPServer) UploadFile(ctx context.Context, request transport.UploadFileRequestObject) (transport.UploadFileResponseObject, error) {
	identity := requestValue(ctx)
	if request.Body == nil {
		return transport.UploadFile400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(identity)}, nil
	}
	created, err := server.uploadFromMultipart(ctx, identity, request.Body)
	if err == nil {
		return transport.UploadFile201JSONResponse{Body: transportMetadata(created), Headers: transport.UploadFile201ResponseHeaders{XCSRFToken: identity.csrf, SetCookie: identity.cookie}}, nil
	}
	switch {
	case errors.Is(err, ErrValidation):
		return transport.UploadFile400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(identity)}, nil
	case errors.Is(err, ErrSizeMismatch):
		return transport.UploadFile400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblemWithCode(identity, "FILE_SIZE_MISMATCH")}, nil
	case errors.Is(err, ErrDenied):
		return transport.UploadFile403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(identity)}, nil
	case errors.Is(err, ErrQuotaExceeded):
		return transport.UploadFile409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: capacityConflictProblem(identity)}, nil
	case errors.Is(err, ErrConflict):
		return transport.UploadFile409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(identity)}, nil
	case errors.Is(err, ErrContentTooLarge):
		return transport.UploadFile413ApplicationProblemPlusJSONResponse{ContentProblemApplicationProblemPlusJSONResponse: contentProblem(identity, "CONTENT_TOO_LARGE")}, nil
	case errors.Is(err, ErrMediaType):
		return transport.UploadFile415ApplicationProblemPlusJSONResponse{Body: makeProblem(transport.Validation, "MEDIA_TYPE_REJECTED", "File content rejected", identity.trace, 415), Headers: transport.UploadFile415ResponseHeaders{XCSRFToken: identity.csrf, SetCookie: identity.cookie}}, nil
	case errors.Is(err, ErrDiskCapacity):
		return transport.UploadFile507ApplicationProblemPlusJSONResponse{CapacityProblemApplicationProblemPlusJSONResponse: capacityProblem(identity)}, nil
	default:
		return transport.UploadFile500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(identity)}, nil
	}
}

func (server *HTTPServer) DeleteFiles(ctx context.Context, request transport.DeleteFilesRequestObject) (transport.DeleteFilesResponseObject, error) {
	identity := requestValue(ctx)
	if request.Body == nil {
		return transport.DeleteFiles400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(identity)}, nil
	}
	targets := make([]DeleteTarget, len(request.Body.Files))
	for index, target := range request.Body.Files {
		targets[index] = DeleteTarget{ID: target.Id.String(), Revision: target.Revision}
	}
	if err := server.service.Delete(ctx, identity.actorID, targets); err != nil {
		return deleteProblem(err, identity), nil
	}
	return transport.DeleteFiles204Response{Headers: transport.DeleteFiles204ResponseHeaders{XCSRFToken: identity.csrf, SetCookie: identity.cookie}}, nil
}

func (server *HTTPServer) GetFileMetadata(ctx context.Context, request transport.GetFileMetadataRequestObject) (transport.GetFileMetadataResponseObject, error) {
	identity := requestValue(ctx)
	metadata, err := server.service.GetMetadata(ctx, identity.actorID, request.FileId.String())
	if err != nil {
		return metadataProblem(err, identity), nil
	}
	return transport.GetFileMetadata200JSONResponse{Body: transportMetadata(metadata), Headers: transport.GetFileMetadata200ResponseHeaders{XCSRFToken: identity.csrf, SetCookie: identity.cookie}}, nil
}

func (server *HTTPServer) DownloadFile(ctx context.Context, request transport.DownloadFileRequestObject) (transport.DownloadFileResponseObject, error) {
	identity := requestValue(ctx)
	download, err := server.service.Download(ctx, identity.actorID, request.FileId.String())
	if err != nil {
		return downloadProblem(err, identity), nil
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": download.Metadata.OriginalName})
	if disposition == "" || len(disposition) > 4096 {
		disposition = "attachment"
	}
	return transport.DownloadFile200ApplicationoctetStreamResponse{Body: download.Content, ContentLength: download.Metadata.SizeBytes, Headers: transport.DownloadFile200ResponseHeaders{
		ContentDisposition: disposition, XContentTypeOptions: "nosniff", XCSRFToken: identity.csrf, SetCookie: identity.cookie,
	}}, nil
}

func (server *HTTPServer) serveUpload(w http.ResponseWriter, request *http.Request) {
	identity := requestValue(request.Context())
	request.Body = http.MaxBytesReader(w, request.Body, server.service.capacityPolicy.MaximumObjectBytes+maximumMultipartOverheadBytes)
	defer request.Body.Close()
	reader, err := request.MultipartReader()
	if err != nil {
		writeProblem(w, makeProblem(transport.Validation, "REQUEST_INVALID", "Request validation failed", identity.trace, http.StatusBadRequest))
		return
	}
	created, err := server.uploadFromMultipart(request.Context(), identity, reader)
	if err != nil {
		status, category, code, title := uploadError(err)
		writeProblem(w, makeProblem(category, code, title, identity.trace, status))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(transportMetadata(created))
}

func (server *HTTPServer) uploadFromMultipart(ctx context.Context, identity requestContext, reader *multipart.Reader) (Metadata, error) {
	part, err := reader.NextPart()
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return Metadata{}, ErrContentTooLarge
		}
		return Metadata{}, ErrValidation
	}
	defer part.Close()
	_, parameters, parseErr := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	originalName, valid := normalizeFilename(parameters["filename"])
	if parseErr != nil || part.FormName() != "file" || !valid {
		return Metadata{}, ErrValidation
	}
	return server.service.Upload(ctx, identity.actorID, UploadInput{OriginalName: originalName, DeclaredMediaType: part.Header.Get("Content-Type"), Content: &singleMultipartPart{part: part, reader: reader}})
}

type singleMultipartPart struct {
	part    io.Reader
	reader  *multipart.Reader
	checked bool
}

func (reader *singleMultipartPart) Read(buffer []byte) (int, error) {
	if reader.checked {
		return 0, io.EOF
	}
	n, err := reader.part.Read(buffer)
	if err == nil {
		return n, nil
	}
	if !errors.Is(err, io.EOF) {
		return n, err
	}
	if n > 0 {
		return n, nil
	}
	reader.checked = true
	next, nextErr := reader.reader.NextPart()
	if next != nil {
		_ = next.Close()
	}
	if errors.Is(nextErr, io.EOF) {
		return 0, io.EOF
	}
	if nextErr != nil {
		return 0, nextErr
	}
	return 0, ErrValidation
}

func requestValue(ctx context.Context) requestContext {
	value, _ := ctx.Value(requestContextKey{}).(requestContext)
	return value
}

func transportMetadataRows(values []Metadata) []transport.FileMetadata {
	rows := make([]transport.FileMetadata, len(values))
	for index, value := range values {
		rows[index] = transportMetadata(value)
	}
	return rows
}

func transportMetadata(value Metadata) transport.FileMetadata {
	return transport.FileMetadata{Id: uuid.MustParse(value.ID), OriginalName: value.OriginalName, MediaType: transport.FileMetadataMediaType(value.MediaType),
		SizeBytes: value.SizeBytes, Sha256: value.SHA256, Revision: value.Revision, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func safeTrace(value string) string {
	if tracePattern.MatchString(value) {
		return value
	}
	return "0000000000000000"
}

func validRequestIdentity(identity RequestIdentity, cookieName string) bool {
	if identity.ActorID == "" || len(identity.ActorID) > 64 || !csrfTokenPattern.MatchString(identity.CSRF) {
		return false
	}
	if identity.ReplacementCookie == nil {
		return true
	}
	if len(*identity.ReplacementCookie) > 4096 || strings.ContainsAny(*identity.ReplacementCookie, "\r\n") {
		return false
	}
	cookie, err := http.ParseSetCookie(*identity.ReplacementCookie)
	return err == nil && cookie.Name == cookieName && csrfTokenPattern.MatchString(cookie.Value) && cookie.Path == "/" && cookie.Secure && cookie.HttpOnly &&
		cookie.SameSite == http.SameSiteStrictMode && cookie.Domain == "" && len(cookie.Unparsed) == 0
}

func makeProblem(category transport.ProblemCategory, code, title, trace string, status int) transport.Problem {
	return transport.Problem{Type: "urn:go-admin-plus:problem:" + strings.ToLower(strings.ReplaceAll(code, "_", "-")), Title: title,
		Status: status, Category: category, Code: code, TraceId: safeTrace(trace)}
}

func writeProblem(w http.ResponseWriter, problem transport.Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(problem.Status)
	_ = json.NewEncoder(w).Encode(problem)
}

func writeAuthenticationFailure(w http.ResponseWriter, err error, trace string) {
	switch {
	case errors.Is(err, ErrAuthentication):
		writeProblem(w, makeProblem(transport.Authentication, "SESSION_REQUIRED", "Authentication required", trace, http.StatusUnauthorized))
	case errors.Is(err, ErrCSRF):
		writeProblem(w, makeProblem(transport.Authorization, "CSRF_REJECTED", "Request authorization failed", trace, http.StatusForbidden))
	default:
		writeProblem(w, makeProblem(transport.Internal, "INTERNAL_ERROR", "Internal server error", trace, http.StatusInternalServerError))
	}
}

func responseHeaders(identity requestContext) transport.ValidationProblemResponseHeaders {
	return transport.ValidationProblemResponseHeaders{XCSRFToken: identity.csrf, SetCookie: identity.cookie}
}
func validationProblem(identity requestContext) transport.ValidationProblemApplicationProblemPlusJSONResponse {
	return validationProblemWithCode(identity, "REQUEST_INVALID")
}
func validationProblemWithCode(identity requestContext, code string) transport.ValidationProblemApplicationProblemPlusJSONResponse {
	return transport.ValidationProblemApplicationProblemPlusJSONResponse{Body: makeProblem(transport.Validation, code, "Request validation failed", identity.trace, 400), Headers: responseHeaders(identity)}
}
func authorizationProblem(identity requestContext) transport.AuthorizationProblemApplicationProblemPlusJSONResponse {
	return transport.AuthorizationProblemApplicationProblemPlusJSONResponse{Body: makeProblem(transport.Authorization, "PERMISSION_DENIED", "Request authorization failed", identity.trace, 403), Headers: transport.AuthorizationProblemResponseHeaders{XCSRFToken: identity.csrf, SetCookie: identity.cookie}}
}
func notFoundProblem(identity requestContext) transport.NotFoundProblemApplicationProblemPlusJSONResponse {
	return transport.NotFoundProblemApplicationProblemPlusJSONResponse{Body: makeProblem(transport.NotFound, "RESOURCE_NOT_FOUND", "Resource not found", identity.trace, 404), Headers: transport.NotFoundProblemResponseHeaders{XCSRFToken: identity.csrf, SetCookie: identity.cookie}}
}
func conflictProblem(identity requestContext) transport.ConflictProblemApplicationProblemPlusJSONResponse {
	return transport.ConflictProblemApplicationProblemPlusJSONResponse{Body: makeProblem(transport.Conflict, "RESOURCE_CONFLICT", "Resource conflict", identity.trace, 409), Headers: transport.ConflictProblemResponseHeaders{XCSRFToken: identity.csrf, SetCookie: identity.cookie}}
}
func capacityConflictProblem(identity requestContext) transport.ConflictProblemApplicationProblemPlusJSONResponse {
	return transport.ConflictProblemApplicationProblemPlusJSONResponse{Body: makeProblem(transport.Conflict, "FILES_QUOTA_EXCEEDED", "File capacity unavailable", identity.trace, 409), Headers: transport.ConflictProblemResponseHeaders{XCSRFToken: identity.csrf, SetCookie: identity.cookie}}
}
func capacityProblem(identity requestContext) transport.CapacityProblemApplicationProblemPlusJSONResponse {
	return transport.CapacityProblemApplicationProblemPlusJSONResponse{Body: makeProblem(transport.Conflict, "FILES_CAPACITY_UNAVAILABLE", "File capacity unavailable", identity.trace, 507), Headers: transport.CapacityProblemResponseHeaders{XCSRFToken: identity.csrf, SetCookie: identity.cookie}}
}
func internalProblem(identity requestContext) transport.InternalProblemApplicationProblemPlusJSONResponse {
	return transport.InternalProblemApplicationProblemPlusJSONResponse{Body: makeProblem(transport.Internal, "INTERNAL_ERROR", "Internal server error", identity.trace, 500), Headers: transport.InternalProblemResponseHeaders{XCSRFToken: identity.csrf, SetCookie: identity.cookie}}
}
func contentProblem(identity requestContext, code string) transport.ContentProblemApplicationProblemPlusJSONResponse {
	return transport.ContentProblemApplicationProblemPlusJSONResponse{Body: makeProblem(transport.Validation, code, "File content rejected", identity.trace, 413), Headers: transport.ContentProblemResponseHeaders{XCSRFToken: identity.csrf, SetCookie: identity.cookie}}
}

func listProblem(err error, identity requestContext) transport.ListFilesResponseObject {
	switch {
	case errors.Is(err, ErrValidation):
		return transport.ListFiles400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(identity)}
	case errors.Is(err, ErrDenied):
		return transport.ListFiles403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(identity)}
	default:
		return transport.ListFiles500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(identity)}
	}
}

func deleteProblem(err error, identity requestContext) transport.DeleteFilesResponseObject {
	switch {
	case errors.Is(err, ErrValidation):
		return transport.DeleteFiles400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(identity)}
	case errors.Is(err, ErrDenied):
		return transport.DeleteFiles403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(identity)}
	case errors.Is(err, ErrNotFound):
		return transport.DeleteFiles404ApplicationProblemPlusJSONResponse{NotFoundProblemApplicationProblemPlusJSONResponse: notFoundProblem(identity)}
	case errors.Is(err, ErrConflict):
		return transport.DeleteFiles409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(identity)}
	default:
		return transport.DeleteFiles500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(identity)}
	}
}

func metadataProblem(err error, identity requestContext) transport.GetFileMetadataResponseObject {
	switch {
	case errors.Is(err, ErrValidation):
		return transport.GetFileMetadata400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(identity)}
	case errors.Is(err, ErrDenied):
		return transport.GetFileMetadata403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(identity)}
	case errors.Is(err, ErrNotFound):
		return transport.GetFileMetadata404ApplicationProblemPlusJSONResponse{NotFoundProblemApplicationProblemPlusJSONResponse: notFoundProblem(identity)}
	default:
		return transport.GetFileMetadata500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(identity)}
	}
}

func downloadProblem(err error, identity requestContext) transport.DownloadFileResponseObject {
	switch {
	case errors.Is(err, ErrValidation):
		return transport.DownloadFile400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(identity)}
	case errors.Is(err, ErrDenied):
		return transport.DownloadFile403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(identity)}
	case errors.Is(err, ErrNotFound):
		return transport.DownloadFile404ApplicationProblemPlusJSONResponse{NotFoundProblemApplicationProblemPlusJSONResponse: notFoundProblem(identity)}
	default:
		return transport.DownloadFile500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(identity)}
	}
}

func uploadError(err error) (int, transport.ProblemCategory, string, string) {
	switch {
	case errors.Is(err, ErrValidation):
		return 400, transport.Validation, "REQUEST_INVALID", "Request validation failed"
	case errors.Is(err, ErrSizeMismatch):
		return 400, transport.Validation, "FILE_SIZE_MISMATCH", "Request validation failed"
	case errors.Is(err, ErrDenied):
		return 403, transport.Authorization, "PERMISSION_DENIED", "Request authorization failed"
	case errors.Is(err, ErrQuotaExceeded):
		return 409, transport.Conflict, "FILES_QUOTA_EXCEEDED", "File capacity unavailable"
	case errors.Is(err, ErrConflict):
		return 409, transport.Conflict, "RESOURCE_CONFLICT", "Resource conflict"
	case errors.Is(err, ErrContentTooLarge):
		return 413, transport.Validation, "CONTENT_TOO_LARGE", "File content rejected"
	case errors.Is(err, ErrMediaType):
		return 415, transport.Validation, "MEDIA_TYPE_REJECTED", "File content rejected"
	case errors.Is(err, ErrDiskCapacity):
		return 507, transport.Conflict, "FILES_CAPACITY_UNAVAILABLE", "File capacity unavailable"
	default:
		return 500, transport.Internal, "INTERNAL_ERROR", "Internal server error"
	}
}
