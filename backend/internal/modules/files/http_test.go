package files

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testCSRF = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type requestAuthenticatorStub struct {
	identity RequestIdentity
	err      error
	calls    int
}

func (*requestAuthenticatorStub) CookieName() string { return "test-session" }
func (stub *requestAuthenticatorStub) AuthorizeRequest(_ context.Context, token, csrf string, mutation bool) (RequestIdentity, error) {
	stub.calls++
	if token != "opaque" {
		return RequestIdentity{}, ErrAuthentication
	}
	if mutation && csrf != testCSRF {
		return RequestIdentity{}, ErrCSRF
	}
	return stub.identity, stub.err
}

type trackingBody struct {
	reader io.Reader
	reads  int
}

func (body *trackingBody) Read(buffer []byte) (int, error) {
	body.reads++
	return body.reader.Read(buffer)
}
func (*trackingBody) Close() error { return nil }

func TestFilesHTTPAuthenticatesBeforeReadingMultipart(t *testing.T) {
	handler, _, _ := filesHTTPFixture(t, &requestAuthenticatorStub{err: ErrAuthentication})
	body := &trackingBody{reader: strings.NewReader("must-not-be-read")}
	request := httptest.NewRequest(http.MethodPost, "/files/objects", body)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || body.reads != 0 {
		t.Fatalf("authentication response=%d body reads=%d", recorder.Code, body.reads)
	}

	authenticator := &requestAuthenticatorStub{err: ErrCSRF}
	handler, _, _ = filesHTTPFixture(t, authenticator)
	body = &trackingBody{reader: strings.NewReader("must-not-be-read")}
	request = httptest.NewRequest(http.MethodPost, "/files/objects", body)
	request.AddCookie(&http.Cookie{Name: "test-session", Value: "opaque"})
	request.Header.Set("X-CSRF-Token", testCSRF)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || body.reads != 0 {
		t.Fatalf("csrf response=%d body reads=%d", recorder.Code, body.reads)
	}
}

func TestFilesHTTPStreamsUploadListDownloadAndFinalPermission(t *testing.T) {
	replacement := "test-session=" + strings.Repeat("r", 43) + "; Path=/; Secure; HttpOnly; SameSite=Strict"
	authenticator := &requestAuthenticatorStub{identity: RequestIdentity{ActorID: "account-a", CSRF: testCSRF, ReplacementCookie: &replacement}}
	handler, authorizer, root := filesHTTPFixture(t, authenticator)
	uploadBody, contentType := multipartBody(t, []multipartFixture{{name: "file", filename: "报告.txt", mediaType: "text/plain", content: "hello"}})
	recorder := serveFiles(t, handler, http.MethodPost, "/files/objects", uploadBody, contentType, true)
	if recorder.Code != http.StatusCreated || recorder.Header().Get("X-CSRF-Token") != testCSRF || recorder.Header().Get("Set-Cookie") != replacement {
		t.Fatalf("upload status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	var created struct {
		ID, OriginalName, SHA256 string
		SizeBytes                int64
		Revision                 int64
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil || created.ID == "" || created.OriginalName != "报告.txt" || created.SizeBytes != 5 || created.Revision != 1 {
		t.Fatalf("upload body=%s err=%v", recorder.Body.String(), err)
	}

	recorder = serveFiles(t, handler, http.MethodGet, "/files/objects?page=1&pageSize=20", nil, "", false)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), created.ID) {
		t.Fatalf("list status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = serveFiles(t, handler, http.MethodGet, "/files/objects/"+created.ID+"/content", nil, "", false)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "hello" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" || strings.Contains(recorder.Header().Get("Content-Disposition"), root) || strings.Contains(recorder.Header().Get("Content-Disposition"), "object-") {
		t.Fatalf("download status=%d headers=%v body=%q", recorder.Code, recorder.Header(), recorder.Body.String())
	}

	authorizer.err = ErrDenied
	recorder = serveFiles(t, handler, http.MethodGet, "/files/objects/"+created.ID+"/content", nil, "", false)
	if recorder.Code != http.StatusForbidden || recorder.Header().Get("X-CSRF-Token") != testCSRF || strings.Contains(recorder.Body.String(), "object-") {
		t.Fatalf("revoked download status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestFilesHTTPRejectsExtraMultipartPartWithoutStateChange(t *testing.T) {
	handler, _, root := filesHTTPFixture(t, &requestAuthenticatorStub{identity: RequestIdentity{ActorID: "account-a", CSRF: testCSRF}})
	body, contentType := multipartBody(t, []multipartFixture{
		{name: "file", filename: "one.txt", mediaType: "text/plain", content: "one"},
		{name: "unexpected", filename: "two.txt", mediaType: "text/plain", content: "two"},
	})
	recorder := serveFiles(t, handler, http.MethodPost, "/files/objects", body, contentType, true)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("extra multipart status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = serveFiles(t, handler, http.MethodGet, "/files/objects?page=1&pageSize=20", nil, "", false)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"total":0`) {
		t.Fatalf("extra multipart changed metadata: %d %s", recorder.Code, recorder.Body.String())
	}
	assertRootEntries(t, root, nil)
}

func TestFilesHTTPRejectsTraversalFilenameAndMediaMismatch(t *testing.T) {
	handler, _, _ := filesHTTPFixture(t, &requestAuthenticatorStub{identity: RequestIdentity{ActorID: "account-a", CSRF: testCSRF}})
	for _, fixture := range []multipartFixture{
		{name: "file", filename: "../outside.txt", mediaType: "text/plain", content: "outside"},
		{name: "file", filename: "image.png", mediaType: "image/png", content: "plain"},
	} {
		body, contentType := multipartBody(t, []multipartFixture{fixture})
		recorder := serveFiles(t, handler, http.MethodPost, "/files/objects", body, contentType, true)
		if recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("rejected upload status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
}

type multipartFixture struct{ name, filename, mediaType, content string }

func multipartBody(t *testing.T, fixtures []multipartFixture) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for _, fixture := range fixtures {
		header := make(map[string][]string)
		header["Content-Disposition"] = []string{`form-data; name="` + fixture.name + `"; filename="` + fixture.filename + `"`}
		header["Content-Type"] = []string{fixture.mediaType}
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, fixture.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.FormDataContentType()
}

func filesHTTPFixture(t *testing.T, authenticator *requestAuthenticatorStub) (http.Handler, *authorizerStub, string) {
	t.Helper()
	db := filesDatabase(t)
	root := canonicalTestRoot(t, "files")
	storage, err := NewLocalStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	authorizer := &authorizerStub{scope: ScopeAll}
	service, err := NewService(db, storage, authorizer, WithClock(fixedFilesClock))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHTTPHandler(service, authenticator, func(*http.Request) string { return "0123456789abcdef" })
	if err != nil {
		t.Fatal(err)
	}
	return handler, authorizer, root
}

func serveFiles(t *testing.T, handler http.Handler, method, path string, body io.Reader, contentType string, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, body)
	request.AddCookie(&http.Cookie{Name: "test-session", Value: "opaque"})
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if csrf {
		request.Header.Set("X-CSRF-Token", testCSRF)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestFilesHTTPProblemsNeverExposeInfrastructure(t *testing.T) {
	handler, authorizer, _ := filesHTTPFixture(t, &requestAuthenticatorStub{identity: RequestIdentity{ActorID: "account-a", CSRF: testCSRF}})
	authorizer.err = errors.New("postgresql://admin:secret@host/private SELECT storage_key")
	recorder := serveFiles(t, handler, http.MethodGet, "/files/objects?page=1&pageSize=20", nil, "", false)
	if recorder.Code != 500 || strings.Contains(recorder.Body.String(), "secret") || strings.Contains(recorder.Body.String(), "storage_key") {
		t.Fatalf("internal response=%d %s", recorder.Code, recorder.Body.String())
	}
}
