package authorization_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/account"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/administration"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	administrationmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0020-administration-schema"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
)

const postgresDSNEnvironment = "GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN"

// TestPostgresRevocationWaitsForFinalAuthorizationFence is default-skipped because it creates and
// drops an isolated schema. Lead runs it against the disposable candidate PostgreSQL profile.
func TestPostgresRevocationWaitsForFinalAuthorizationFence(t *testing.T) {
	dsn := os.Getenv(postgresDSNEnvironment)
	if dsn == "" {
		t.Skip(postgresDSNEnvironment + " is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	setup := openPostgresAuthorizationDB(t, ctx, withApplicationName(t, dsn, "t07_setup"))
	schema := "t07_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := setup.SQL().ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal("create isolated schema failed")
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := setup.SQL().ExecContext(cleanup, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Errorf("isolated schema cleanup failed")
		}
	})
	serviceDB := openPostgresAuthorizationDB(t, ctx, withSearchPath(t, withApplicationName(t, dsn, "t07_service"), schema))
	revokerDB := openPostgresAuthorizationDB(t, ctx, withSearchPath(t, withApplicationName(t, dsn, "t07_revoker"), schema))
	runner, err := migrations.NewRunner(sessionmigration.Provider{}, administrationmigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(ctx, serviceDB); err != nil {
		t.Fatal("isolated migrations failed")
	}
	if _, err := serviceDB.Bun().ExecContext(ctx, `INSERT INTO iam_roles(id, role_key, name, data_scope, enabled, protected, created_at, updated_at) VALUES (?, ?, ?, 'all', TRUE, FALSE, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, "role-invalid-key1", "invalid key", "Invalid"); err == nil {
		t.Fatal("PostgreSQL role key constraint accepted invalid key")
	}
	if _, err := serviceDB.Bun().ExecContext(ctx, `INSERT INTO iam_menus(id, menu_key, label, path, permission_code, sort_order, protected, created_at, updated_at) VALUES (?, ?, ?, ?, NULL, 1, FALSE, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, "menu-invalid-null", "valid-menu", "Invalid", "/iam/invalid-null"); err == nil {
		t.Fatal("PostgreSQL menu permission accepted NULL")
	}
	hash, err := account.HashPassword("administrator password")
	if err != nil {
		t.Fatal(err)
	}
	repository := account.NewRepository(database.DialectPostgres)
	if err := serviceDB.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		return repository.Create(ctx, tx, account.Credential{Profile: account.Profile{ID: adminID, Username: "admin", DisplayName: "Administrator", Email: "admin@example.test"}, PasswordHash: hash}, time.Now().UTC())
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := serviceDB.Bun().ExecContext(ctx, `INSERT INTO iam_account_roles(account_id, role_id) VALUES (?, ?)`, adminID, "role-system-admin"); err != nil {
		t.Fatal(err)
	}

	locked, release := make(chan struct{}), make(chan struct{})
	service, err := administration.NewService(serviceDB, administration.WithAuthorizationProbe(func(permission string) {
		if permission == authorization.PermissionRolesWrite {
			close(locked)
			<-release
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	command := make(chan error, 1)
	go func() {
		_, commandErr := service.CreateRole(ctx, adminID, "fenced-role", "Fenced role", authorization.ScopeAll)
		command <- commandErr
	}()
	<-locked
	revoked := make(chan error, 1)
	go func() {
		_, revokeErr := revokerDB.Bun().ExecContext(ctx, `DELETE FROM iam_role_permissions WHERE role_id = ? AND permission_code = ?`, "role-system-admin", authorization.PermissionRolesWrite)
		revoked <- revokeErr
	}()
	waitForPostgresLock(t, ctx, setup, "t07_revoker")
	close(release)
	if err := <-command; err != nil {
		t.Fatalf("already-authorized command failed: %v", err)
	}
	if err := <-revoked; err != nil {
		t.Fatal("revocation failed")
	}
	plain, _ := administration.NewService(serviceDB)
	if _, err := plain.CreateRole(ctx, adminID, "after-revoke", "After revoke", authorization.ScopeAll); !errors.Is(err, authorization.ErrDenied) {
		t.Fatalf("post-revoke command = %v", err)
	}

	toggle := make(chan error, 1)
	go func() {
		for index := 0; index < 100; index++ {
			if _, err := revokerDB.Bun().ExecContext(ctx, `DELETE FROM iam_role_permissions WHERE role_id = ? AND permission_code = ?`, "role-system-admin", authorization.PermissionUsersRead); err != nil {
				toggle <- err
				return
			}
			if _, err := revokerDB.Bun().ExecContext(ctx, `INSERT INTO iam_role_permissions(role_id, permission_code) VALUES (?, ?)`, "role-system-admin", authorization.PermissionUsersRead); err != nil {
				toggle <- err
				return
			}
		}
		toggle <- nil
	}()
	manifestService := authorization.NewService(serviceDB)
	for index := 0; index < 200; index++ {
		manifest, err := manifestService.Manifest(ctx, adminID)
		if err != nil {
			t.Fatalf("concurrent manifest = %v", err)
		}
		hasPermission, hasMenu := false, false
		for _, permission := range manifest.Permissions {
			hasPermission = hasPermission || permission == authorization.PermissionUsersRead
		}
		for _, menu := range manifest.Menus {
			hasMenu = hasMenu || menu.PermissionCode == authorization.PermissionUsersRead
		}
		if hasPermission != hasMenu {
			t.Fatal("manifest combined permission and menu from different snapshots")
		}
	}
	if err := <-toggle; err != nil {
		t.Fatal("manifest grant toggle failed")
	}
}

func waitForPostgresLock(t *testing.T, ctx context.Context, db *database.Database, applicationName string) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := db.SQL().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE application_name = $1 AND wait_event_type = 'Lock')`, applicationName).Scan(&waiting)
		if err != nil {
			t.Fatal("lock observation failed")
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("revocation never reached database lock wait")
		case <-ticker.C:
		}
	}
}

func openPostgresAuthorizationDB(t *testing.T, ctx context.Context, dsn string) *database.Database {
	t.Helper()
	db, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: dsn})
	if err != nil {
		t.Fatal("postgres open failed")
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
func withApplicationName(t *testing.T, dsn, name string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("postgres DSN is invalid")
	}
	query := parsed.Query()
	query.Set("application_name", name)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
func withSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	if !strings.HasPrefix(schema, "t07_") {
		t.Fatal("isolated schema name is invalid")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("postgres DSN is invalid")
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func TestPostgresHarnessDiagnosticsDoNotContainDSN(t *testing.T) {
	const secret = "postgres://user:password@private.example/db"
	output := fmt.Sprintf("%v", database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: secret})
	if strings.Contains(output, "password") || strings.Contains(output, "private.example") {
		t.Fatal("database config exposed DSN")
	}
}
