package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/contracts/capabilities"
	filesmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/migrations/0010-files"
	capacitymigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/migrations/0020-capacity"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
)

type authorizerStub struct {
	scope Scope
	err   error
	calls int
}

type observerCapture struct{ values []Observation }

func (capture *observerCapture) Observe(value Observation) {
	capture.values = append(capture.values, value)
}

type revokingAuthorizer struct{ calls int }

func (authorizer *revokingAuthorizer) RequireInTx(context.Context, database.Tx, string, string) (Scope, error) {
	authorizer.calls++
	if authorizer.calls == 1 {
		return ScopeAll, nil
	}
	return "", ErrDenied
}

func (stub *authorizerStub) RequireInTx(context.Context, database.Tx, string, string) (Scope, error) {
	stub.calls++
	return stub.scope, stub.err
}

type countingReader struct{ reads int }

func (reader *countingReader) Read([]byte) (int, error) {
	reader.reads++
	return 0, io.EOF
}

func TestFilesUploadAuthorizesBeforeReadingAndAgainBeforeInsert(t *testing.T) {
	db := filesDatabase(t)
	storage, err := NewLocalStorage(canonicalTestRoot(t, "files"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	authorizer := &authorizerStub{scope: ScopeAll, err: ErrDenied}
	service, err := NewService(db, storage, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	content := &countingReader{}
	if _, err := service.Upload(context.Background(), "account-a", UploadInput{OriginalName: "denied.txt", DeclaredMediaType: "text/plain", Content: content}); !errors.Is(err, ErrDenied) {
		t.Fatalf("denied upload = %v", err)
	}
	if content.reads != 0 || authorizer.calls != 1 {
		t.Fatalf("denied upload reads=%d authorization calls=%d", content.reads, authorizer.calls)
	}

	authorizer.err = nil
	if _, err := service.Upload(context.Background(), "account-a", UploadInput{OriginalName: "allowed.txt", DeclaredMediaType: "text/plain", Content: strings.NewReader("allowed")}); err != nil {
		t.Fatal(err)
	}
	if authorizer.calls != 3 {
		t.Fatalf("authorized upload authorization calls=%d, want preflight+final", authorizer.calls)
	}
}

func TestFilesUploadReturnsThePersistedReadyTimestamp(t *testing.T) {
	db := filesDatabase(t)
	storage, err := NewLocalStorage(canonicalTestRoot(t, "files"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	createdAt := time.Date(2026, time.August, 27, 10, 0, 0, 0, time.UTC)
	readyAt := createdAt.Add(3 * time.Second)
	times := []time.Time{createdAt, readyAt}
	clock := func() time.Time {
		value := times[0]
		if len(times) > 1 {
			times = times[1:]
		}
		return value
	}
	service, err := NewService(db, storage, &authorizerStub{scope: ScopeAll}, WithClock(clock))
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Upload(context.Background(), "account-a", UploadInput{OriginalName: "ready.txt", DeclaredMediaType: "text/plain", Content: strings.NewReader("ready")})
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.List(context.Background(), "account-a", ListQuery{Page: 1, PageSize: 20})
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("ready projection=%#v err=%v", page, err)
	}
	if !created.CreatedAt.Equal(createdAt) || !created.UpdatedAt.Equal(readyAt) || !sameMetadata(created, page.Rows[0]) {
		t.Fatalf("upload response=%#v persisted=%#v", created, page.Rows[0])
	}
}

func TestFilesObserverEmitsOnlyStableNonSensitiveSignals(t *testing.T) {
	db := filesDatabase(t)
	storage, err := NewLocalStorage(canonicalTestRoot(t, "files"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	observer := &observerCapture{}
	authorizer := &authorizerStub{scope: ScopeAll, err: ErrDenied}
	service, err := NewService(db, storage, authorizer, WithObserver(observer))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = service.Upload(context.Background(), "sensitive-account", UploadInput{OriginalName: "secret-name.txt", DeclaredMediaType: "text/plain", Content: strings.NewReader("secret-content")})
	authorizer.err = nil
	_, _ = service.List(context.Background(), "sensitive-account", ListQuery{Page: 1, PageSize: 20})
	if want := []Observation{{Operation: "upload", Outcome: "rejected"}, {Operation: "list", Outcome: "succeeded"}}; !reflect.DeepEqual(observer.values, want) {
		t.Fatalf("observations = %#v", observer.values)
	}
	for _, value := range observer.values {
		if strings.Contains(fmt.Sprint(value), "sensitive") || strings.Contains(fmt.Sprint(value), "secret") {
			t.Fatalf("observation leaked request material: %#v", value)
		}
	}
}

func TestFilesUploadRepeatsAuthorizationInInsertTransactionAfterStaging(t *testing.T) {
	db := filesDatabase(t)
	root := canonicalTestRoot(t, "files")
	storage, err := NewLocalStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	authorizer := &revokingAuthorizer{}
	service, err := NewService(db, storage, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Upload(context.Background(), "account-a", UploadInput{OriginalName: "revoked.txt", DeclaredMediaType: "text/plain", Content: strings.NewReader("content")}); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked final authorization = %v", err)
	}
	if authorizer.calls != 2 {
		t.Fatalf("authorization calls = %d", authorizer.calls)
	}
	assertRootEntries(t, root, nil)
	var count int
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM files_objects`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked final authorization state count=%d err=%v", count, err)
	}
}

func TestFilesUploadListDownloadAndDeleteUseFinalAuthorization(t *testing.T) {
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

	created, err := service.Upload(context.Background(), "account-a", UploadInput{OriginalName: " notes.txt ", DeclaredMediaType: "text/plain", Content: strings.NewReader("hello")})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.OriginalName != "notes.txt" || created.SizeBytes != 5 || created.Revision != 1 || created.SHA256 == "" {
		t.Fatalf("created metadata = %#v", created)
	}
	page, err := service.List(context.Background(), "account-a", ListQuery{Page: 1, PageSize: 20, Sort: "name", Direction: "ascending"})
	if err != nil || page.Total != 1 || len(page.Rows) != 1 || !sameMetadata(page.Rows[0], created) {
		t.Fatalf("page = %#v, err=%v", page, err)
	}
	download, err := service.Download(context.Background(), "account-a", created.ID)
	if err != nil {
		t.Fatal(err)
	}
	content, readErr := io.ReadAll(download.Content)
	closeErr := download.Content.Close()
	if readErr != nil || closeErr != nil || string(content) != "hello" || !sameMetadata(download.Metadata, created) {
		t.Fatalf("download content=%q metadata=%#v read=%v close=%v", content, download.Metadata, readErr, closeErr)
	}

	authorizer.err = ErrDenied
	if _, err := service.Download(context.Background(), "account-a", created.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked download = %v", err)
	}
	authorizer.err = nil
	if err := service.Delete(context.Background(), "account-a", []DeleteTarget{{ID: created.ID, Revision: created.Revision}}); err != nil {
		t.Fatal(err)
	}
	page, err = service.List(context.Background(), "account-a", ListQuery{Page: 1, PageSize: 20})
	if err != nil || page.Total != 0 || len(page.Rows) != 0 {
		t.Fatalf("deleted page = %#v, err=%v", page, err)
	}
}

func TestFilesScopeAndBatchConflictFailClosedWithoutPartialDelete(t *testing.T) {
	db := filesDatabase(t)
	storage, err := NewLocalStorage(canonicalTestRoot(t, "files"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	authorizer := &authorizerStub{scope: ScopeAll}
	service, err := NewService(db, storage, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	a, err := service.Upload(context.Background(), "account-a", UploadInput{OriginalName: "a.txt", DeclaredMediaType: "text/plain", Content: strings.NewReader("a")})
	if err != nil {
		t.Fatal(err)
	}
	b, err := service.Upload(context.Background(), "account-b", UploadInput{OriginalName: "b.txt", DeclaredMediaType: "text/plain", Content: strings.NewReader("b")})
	if err != nil {
		t.Fatal(err)
	}
	authorizer.scope = ScopeSelf
	page, err := service.List(context.Background(), "account-a", ListQuery{Page: 1, PageSize: 20})
	if err != nil || page.Total != 1 || page.Rows[0].ID != a.ID {
		t.Fatalf("self page = %#v, err=%v", page, err)
	}
	if err := service.Delete(context.Background(), "account-a", []DeleteTarget{{ID: a.ID, Revision: a.Revision}, {ID: b.ID, Revision: b.Revision}}); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign batch delete = %v", err)
	}
	authorizer.scope = ScopeAll
	page, err = service.List(context.Background(), "account-a", ListQuery{Page: 1, PageSize: 20})
	if err != nil || page.Total != 2 {
		t.Fatalf("failed batch changed projection = %#v, err=%v", page, err)
	}
	if err := service.Delete(context.Background(), "account-a", []DeleteTarget{{ID: a.ID, Revision: a.Revision + 1}, {ID: b.ID, Revision: b.Revision}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale batch delete = %v", err)
	}
	page, _ = service.List(context.Background(), "account-a", ListQuery{Page: 1, PageSize: 20})
	if page.Total != 2 {
		t.Fatalf("stale batch partially deleted rows = %#v", page.Rows)
	}
	authorizer.scope = Scope("unexpected")
	if _, err := service.Upload(context.Background(), "account-a", UploadInput{OriginalName: "unknown.txt", DeclaredMediaType: "text/plain", Content: strings.NewReader("unknown")}); !errors.Is(err, ErrDenied) {
		t.Fatalf("unknown scope upload = %v", err)
	}
}

func TestFilesReconcileCompletesPendingAndDeletingAfterRestart(t *testing.T) {
	db := filesDatabase(t)
	root := canonicalTestRoot(t, "files")
	storage, err := NewLocalStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &authorizerStub{scope: ScopeAll}
	service, err := NewService(db, storage, authorizer, WithClock(fixedFilesClock))
	if err != nil {
		t.Fatal(err)
	}
	staged, err := storage.Stage(context.Background(), "text/plain", strings.NewReader("recover"))
	if err != nil {
		t.Fatal(err)
	}
	pending := fileRecord{ID: "00000000-0000-4000-8000-000000000013", OwnerAccountID: "account-a", OriginalName: "recover.txt", NameKey: "recover.txt", MediaType: staged.MediaType, SizeBytes: staged.SizeBytes, SHA256: staged.SHA256, StorageKey: NewStorageKey(), TemporaryKey: &staged.TemporaryKey, State: statePending, Revision: 1, CreatedAt: fixedFilesClock(), UpdatedAt: fixedFilesClock()}
	if err := db.WithinTx(context.Background(), func(ctx context.Context, tx database.Tx) error { return service.repository.insert(ctx, tx, pending) }); err != nil {
		t.Fatal(err)
	}
	linkedStage, err := storage.Stage(context.Background(), "text/plain", strings.NewReader("linked-before-cleanup"))
	if err != nil {
		t.Fatal(err)
	}
	linkedPending := fileRecord{ID: "00000000-0000-4000-8000-000000000014", OwnerAccountID: "account-a", OriginalName: "linked.txt", NameKey: "linked.txt", MediaType: linkedStage.MediaType, SizeBytes: linkedStage.SizeBytes, SHA256: linkedStage.SHA256, StorageKey: NewStorageKey(), TemporaryKey: &linkedStage.TemporaryKey, State: statePending, Revision: 1, CreatedAt: fixedFilesClock(), UpdatedAt: fixedFilesClock()}
	// Model a crash after the durable no-replace link but before stage removal and metadata ready.
	if err := os.Link(filepath.Join(root, linkedStage.TemporaryKey), filepath.Join(root, linkedPending.StorageKey)); err != nil {
		t.Fatal(err)
	}
	if err := db.WithinTx(context.Background(), func(ctx context.Context, tx database.Tx) error {
		return service.repository.insert(ctx, tx, linkedPending)
	}); err != nil {
		t.Fatal(err)
	}
	deleting, err := service.Upload(context.Background(), "account-a", UploadInput{OriginalName: "delete.txt", DeclaredMediaType: "text/plain", Content: strings.NewReader("delete")})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinTx(context.Background(), func(ctx context.Context, tx database.Tx) error {
		_, err := service.repository.markDeleting(ctx, tx, []DeleteTarget{{ID: deleting.ID, Revision: deleting.Revision}}, "account-a", ScopeAll, fixedFilesClock())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	storage, err = NewLocalStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	service, err = NewService(db, storage, authorizer, WithClock(fixedFilesClock))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, err := service.List(context.Background(), "account-a", ListQuery{Page: 1, PageSize: 20})
	if err != nil || page.Total != 2 {
		t.Fatalf("reconciled page = %#v, err=%v", page, err)
	}
	if exists, err := storage.TemporaryExists(context.Background(), linkedStage.TemporaryKey); err != nil || exists {
		t.Fatalf("published stage remained after reconcile: exists=%v err=%v", exists, err)
	}
	download, err := service.Download(context.Background(), "account-a", pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(download.Content)
	_ = download.Content.Close()
	if string(content) != "recover" {
		t.Fatalf("reconciled content = %q", content)
	}
}

type capabilityCapture struct {
	capabilities capabilities.ModuleCapabilities
}

func (capture *capabilityCapture) Register(_ context.Context, capabilities capabilities.ModuleCapabilities) error {
	capture.capabilities = capabilities
	return nil
}

func TestFilesDeclaresStableCapabilities(t *testing.T) {
	capture := &capabilityCapture{}
	if err := RegisterCapabilities(context.Background(), capture); err != nil {
		t.Fatal(err)
	}
	want := []string{PermissionFilesRead, PermissionFilesWrite, PermissionFilesDelete}
	actual := make([]string, len(capture.capabilities.Permissions))
	for index, permission := range capture.capabilities.Permissions {
		actual[index] = permission.Code
	}
	if !reflect.DeepEqual(actual, want) || len(capture.capabilities.Menus) != 1 || capture.capabilities.Menus[0].PermissionCode != PermissionFilesRead {
		t.Fatalf("capabilities = %#v", capture.capabilities)
	}
}

func filesDatabase(t *testing.T) *database.Database {
	t.Helper()
	db, err := database.NewProcess().Open(context.Background(), database.Config{Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "files.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner, err := migrations.NewRunner(filesmigration.Provider{}, capacitymigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.Up(context.Background(), db)
	if err != nil || first.Applied != 2 {
		t.Fatalf("migration = %#v, %v", first, err)
	}
	second, err := runner.Up(context.Background(), db)
	if err != nil || second.Applied != 0 {
		t.Fatalf("idempotent migration = %#v, %v", second, err)
	}
	return db
}

func fixedFilesClock() time.Time { return time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC) }

func sameMetadata(left, right Metadata) bool {
	return left.ID == right.ID && left.OriginalName == right.OriginalName && left.MediaType == right.MediaType &&
		left.SHA256 == right.SHA256 && left.SizeBytes == right.SizeBytes && left.Revision == right.Revision &&
		left.CreatedAt.Equal(right.CreatedAt) && left.UpdatedAt.Equal(right.UpdatedAt)
}
