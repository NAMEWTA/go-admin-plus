package files_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/app/adapters"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files"
	filesmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/migrations/0010-files"
	capacitymigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/migrations/0020-capacity"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/account"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	administrationmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0020-administration-schema"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
)

const postgresDSNEnvironment = "GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN"

func TestFilesServerAndDesktopSQLiteContract(t *testing.T) {
	for _, profile := range []config.Profile{config.ProfileServerSQLite, config.ProfileDesktopSQLite} {
		t.Run(string(profile), func(t *testing.T) {
			db, cleanup := openSQLite(t, profile)
			defer cleanup()
			runFilesContract(t, db, canonicalContentRoot(t))
		})
	}
}

func TestFilesPostgresContract(t *testing.T) {
	if os.Getenv(postgresDSNEnvironment) == "" {
		t.Skip(postgresDSNEnvironment + " is not configured")
	}
	db, cleanup, schema := openPostgres(t)
	defer cleanup()
	var current string
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT current_schema()`).Scan(&current); err != nil || current != schema {
		t.Fatalf("files PostgreSQL schema=%q err=%v", current, err)
	}
	runFilesContract(t, db, canonicalContentRoot(t))
}

func runFilesContract(t *testing.T, db *database.Database, root string) {
	t.Helper()
	ctx := context.Background()
	runner, err := migrations.NewRunner(sessionmigration.Provider{}, administrationmigration.Provider{}, filesmigration.Provider{}, capacitymigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	seedAccount(t, db, "account-files-owner", "files-owner")
	seedAccount(t, db, "account-files-foreign", "files-foreign")
	registry, err := authorization.NewCapabilityRegistry(db)
	if err != nil || files.RegisterCapabilities(ctx, registry) != nil {
		t.Fatal("files capability registry failed")
	}
	for _, id := range []string{"account-files-owner", "account-files-foreign"} {
		if _, err := db.Bun().ExecContext(ctx, `INSERT INTO iam_account_roles(account_id, role_id) VALUES (?, ?)`, id, "role-system-admin"); err != nil {
			t.Fatal(err)
		}
	}
	authorizationAdapters, err := adapters.NewAuthorization(db)
	if err != nil {
		t.Fatal(err)
	}
	storage, err := files.NewLocalStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	service, err := files.NewService(db, storage, authorizationAdapters.Files())
	if err != nil {
		t.Fatal(err)
	}
	fixtures := []struct{ owner, name, content string }{
		{"account-files-owner", "% literal.txt", "percent"},
		{"account-files-owner", "_ literal.txt", "underscore"},
		{"account-files-owner", "<:@ collision.txt", "ascii"},
		{"account-files-owner", "ä collision.txt", "unicode"},
		{"account-files-foreign", "foreign.txt", "foreign"},
	}
	created := make([]files.Metadata, 0, len(fixtures))
	for _, fixture := range fixtures {
		value, err := service.Upload(ctx, fixture.owner, files.UploadInput{OriginalName: fixture.name, DeclaredMediaType: "text/plain", Content: strings.NewReader(fixture.content)})
		if err != nil {
			t.Fatalf("upload %q=%v", fixture.name, err)
		}
		created = append(created, value)
	}
	for search, name := range map[string]string{"%": "% literal.txt", "_": "_ literal.txt", "<:@": "<:@ collision.txt", "ä": "ä collision.txt"} {
		page, err := service.List(ctx, "account-files-owner", files.ListQuery{Search: search, Page: 1, PageSize: 20, Sort: "name", Direction: "ascending"})
		if err != nil || page.Total != 1 || len(page.Rows) != 1 || page.Rows[0].OriginalName != name {
			t.Fatalf("literal search %q=%#v err=%v", search, page, err)
		}
	}
	page, err := service.List(ctx, "account-files-owner", files.ListQuery{Page: 1, PageSize: 20, Sort: "name", Direction: "ascending"})
	want := []string{"% literal.txt", "<:@ collision.txt", "_ literal.txt", "foreign.txt", "ä collision.txt"}
	if err != nil || len(page.Rows) != len(want) {
		t.Fatalf("deterministic page=%#v err=%v", page, err)
	}
	for index, name := range want {
		if page.Rows[index].OriginalName != name {
			t.Fatalf("deterministic order[%d]=%q want=%q", index, page.Rows[index].OriginalName, name)
		}
	}
	if err := service.Delete(ctx, "account-files-owner", []files.DeleteTarget{{ID: created[0].ID, Revision: created[0].Revision}, {ID: created[1].ID, Revision: 99}}); !errors.Is(err, files.ErrConflict) {
		t.Fatalf("atomic stale delete=%v", err)
	}
	if _, err := service.GetMetadata(ctx, "account-files-owner", created[0].ID); err != nil {
		t.Fatalf("atomic delete removed first row: %v", err)
	}
	if _, err := db.Bun().ExecContext(ctx, `UPDATE iam_roles SET data_scope = ? WHERE id = ?`, "self", "role-system-admin"); err != nil {
		t.Fatal(err)
	}
	page, err = service.List(ctx, "account-files-owner", files.ListQuery{Page: 1, PageSize: 20})
	if err != nil || page.Total != 4 {
		t.Fatalf("self scope page=%#v err=%v", page, err)
	}
	if _, err := service.Download(ctx, "account-files-owner", created[len(created)-1].ID); !errors.Is(err, files.ErrDenied) {
		t.Fatalf("foreign self-scope download=%v", err)
	}
	if _, err := db.Bun().ExecContext(ctx, `UPDATE iam_roles SET data_scope = ? WHERE id = ?`, "all", "role-system-admin"); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	storage, err = files.NewLocalStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	service, err = files.NewService(db, storage, authorizationAdapters.Files())
	if err != nil {
		t.Fatal(err)
	}
	download, err := service.Download(ctx, "account-files-owner", created[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	content, readErr := io.ReadAll(download.Content)
	closeErr := download.Content.Close()
	if readErr != nil || closeErr != nil || string(content) != "ascii" {
		t.Fatalf("restart download=%q read=%v close=%v", content, readErr, closeErr)
	}
	if db.Dialect() == database.DialectPostgres {
		failureStorage := &publishFailureStorage{Storage: storage}
		failedService, err := files.NewService(db, failureStorage, authorizationAdapters.Files(), files.WithCapacityProbe(files.NewDiskCapacityProbe(root)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := failedService.Upload(ctx, "account-files-owner", files.UploadInput{OriginalName: "recover-concurrently.txt", DeclaredMediaType: "text/plain", Content: strings.NewReader("recover")}); !errors.Is(err, files.ErrInternal) {
			t.Fatalf("injected publish failure=%v", err)
		}
		firstRecovery, _ := files.NewService(db, storage, authorizationAdapters.Files())
		secondRecovery, _ := files.NewService(db, storage, authorizationAdapters.Files())
		errorsChannel := make(chan error, 2)
		go func() { errorsChannel <- firstRecovery.Reconcile(ctx) }()
		go func() { errorsChannel <- secondRecovery.Reconcile(ctx) }()
		for range 2 {
			if err := <-errorsChannel; err != nil {
				t.Fatalf("concurrent PostgreSQL recovery=%v", err)
			}
		}
		page, err := service.List(ctx, "account-files-owner", files.ListQuery{Search: "recover-concurrently", Page: 1, PageSize: 20})
		if err != nil || page.Total != 1 {
			t.Fatalf("claimed recovery page=%#v err=%v", page, err)
		}
	}
	if _, err := db.Bun().ExecContext(ctx, `DELETE FROM iam_role_permissions WHERE role_id = ? AND permission_code = ?`, "role-system-admin", files.PermissionFilesWrite); err != nil {
		t.Fatal(err)
	}
	reader := &countingReader{}
	if _, err := service.Upload(ctx, "account-files-owner", files.UploadInput{OriginalName: "denied.txt", DeclaredMediaType: "text/plain", Content: reader}); !errors.Is(err, files.ErrDenied) || reader.reads != 0 {
		t.Fatalf("revoked upload=%v reads=%d", err, reader.reads)
	}
}

type countingReader struct{ reads int }

func (reader *countingReader) Read([]byte) (int, error) { reader.reads++; return 0, io.EOF }

type publishFailureStorage struct {
	files.Storage
	once sync.Once
}

func (storage *publishFailureStorage) Publish(ctx context.Context, temporaryKey, storageKey string) error {
	failed := false
	storage.once.Do(func() { failed = true })
	if failed {
		return errors.New("injected publish failure")
	}
	return storage.Storage.Publish(ctx, temporaryKey, storageKey)
}

func seedAccount(t *testing.T, db *database.Database, id, username string) {
	t.Helper()
	hash, err := account.HashPassword("files contract password")
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithinTx(context.Background(), func(ctx context.Context, tx database.Tx) error {
		return account.NewRepository(db.Dialect()).Create(ctx, tx, account.Credential{Profile: account.Profile{ID: id, Username: username, DisplayName: username, Email: username + "@example.test"}, PasswordHash: hash}, time.Now().UTC())
	})
	if err != nil {
		t.Fatal(err)
	}
}

func openSQLite(t *testing.T, profile config.Profile) (*database.Database, func()) {
	t.Helper()
	db, err := database.NewProcess().Open(context.Background(), database.Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "files.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	return db, func() { _ = db.Close() }
}

func canonicalContentRoot(t *testing.T) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(parent, "content")
}

func openPostgres(t *testing.T) (*database.Database, func(), string) {
	t.Helper()
	ctx := context.Background()
	dsn := os.Getenv(postgresDSNEnvironment)
	admin, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: dsn})
	if err != nil {
		t.Fatal("files PostgreSQL administrator failed")
	}
	schema := "t13_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.SQL().ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		_ = admin.Close()
		t.Fatal("files PostgreSQL schema failed")
	}
	var db *database.Database
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			if db != nil {
				_ = db.Close()
			}
			cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := admin.SQL().ExecContext(cleanupContext, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
				t.Error("files PostgreSQL cleanup failed")
			} else {
				var exists bool
				if err := admin.Bun().QueryRowContext(cleanupContext, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = ?)`, schema).Scan(&exists); err != nil || exists {
					t.Error("files PostgreSQL schema residue detected")
				}
			}
			_ = admin.Close()
		})
	}
	t.Cleanup(cleanup)
	parsed, err := url.Parse(dsn)
	if err != nil {
		cleanup()
		t.Fatal("files PostgreSQL DSN invalid")
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err = database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: parsed.String()})
	if err != nil {
		cleanup()
		t.Fatal(fmt.Errorf("files isolated PostgreSQL open failed: %w", err))
	}
	return db, cleanup, schema
}
