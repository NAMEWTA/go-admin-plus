package authorization_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	administrationmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0020-administration-schema"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
)

func TestAuthorizationReadsDatabaseFactsOnEveryDecision(t *testing.T) {
	db := authorizationDatabase(t)
	seedAccount(t, db, "account-reader-01", "reader")
	mustExec(t, db, `INSERT INTO iam_roles(id, role_key, name, data_scope, enabled, protected, created_at, updated_at) VALUES (?, ?, ?, 'self', ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, "role-reader-0001", "reader", "Reader", true, false)
	mustExec(t, db, `INSERT INTO iam_role_permissions(role_id, permission_code) VALUES (?, ?)`, "role-reader-0001", authorization.PermissionUsersRead)
	mustExec(t, db, `INSERT INTO iam_account_roles(account_id, role_id) VALUES (?, ?)`, "account-reader-01", "role-reader-0001")

	service := authorization.NewService(db)
	decision, err := service.Require(context.Background(), "account-reader-01", authorization.PermissionUsersRead)
	if err != nil || decision.Scope != authorization.ScopeSelf {
		t.Fatalf("initial decision = %#v, %v", decision, err)
	}
	mustExec(t, db, `UPDATE iam_roles SET enabled = ? WHERE id = ?`, false, "role-reader-0001")
	if _, err := service.Require(context.Background(), "account-reader-01", authorization.PermissionUsersRead); !errors.Is(err, authorization.ErrDenied) {
		t.Fatalf("disabled role remained authorized: %v", err)
	}
	mustExec(t, db, `UPDATE iam_roles SET enabled = ? WHERE id = ?`, true, "role-reader-0001")
	mustExec(t, db, `DELETE FROM iam_role_permissions WHERE role_id = ?`, "role-reader-0001")
	if _, err := service.Require(context.Background(), "account-reader-01", authorization.PermissionUsersRead); !errors.Is(err, authorization.ErrDenied) {
		t.Fatalf("revoked permission remained authorized: %v", err)
	}
}

func TestManifestIsDatabaseProjectionAndOmitsUnauthorizedMenus(t *testing.T) {
	db := authorizationDatabase(t)
	seedAccount(t, db, "account-admin-001", "admin")
	mustExec(t, db, `INSERT INTO iam_account_roles(account_id, role_id) VALUES (?, ?)`, "account-admin-001", "role-system-admin")
	service := authorization.NewService(db)
	manifest, err := service.Manifest(context.Background(), "account-admin-001")
	if err != nil || len(manifest.Permissions) != 13 || len(manifest.Menus) != 3 || manifest.Scope != authorization.ScopeAll {
		t.Fatalf("admin manifest = %#v, %v", manifest, err)
	}
	mustExec(t, db, `DELETE FROM iam_role_permissions WHERE role_id = ? AND permission_code = ?`, "role-system-admin", authorization.PermissionUsersRead)
	manifest, err = service.Manifest(context.Background(), "account-admin-001")
	if err != nil {
		t.Fatal(err)
	}
	for _, menu := range manifest.Menus {
		if menu.Key == "iam-users" {
			t.Fatal("menu remained visible without its permission code")
		}
	}
}

func TestManifestCombinesMenuAndPermissionAcrossEnabledRoles(t *testing.T) {
	db := authorizationDatabase(t)
	seedAccount(t, db, "account-union-001", "union")
	for _, role := range []struct{ id, key string }{{"role-menu-000001", "menu-role"}, {"role-perm-000001", "permission-role"}} {
		mustExec(t, db, `INSERT INTO iam_roles(id, role_key, name, data_scope, enabled, protected, created_at, updated_at) VALUES (?, ?, ?, 'self', ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, role.id, role.key, role.key, true, false)
		mustExec(t, db, `INSERT INTO iam_account_roles(account_id, role_id) VALUES (?, ?)`, "account-union-001", role.id)
	}
	mustExec(t, db, `INSERT INTO iam_role_menus(role_id, menu_id) VALUES (?, ?)`, "role-menu-000001", "menu-iam-users-01")
	mustExec(t, db, `INSERT INTO iam_role_permissions(role_id, permission_code) VALUES (?, ?)`, "role-perm-000001", authorization.PermissionUsersRead)
	mustExec(t, db, `INSERT INTO iam_role_permissions(role_id, permission_code) VALUES (?, ?)`, "role-perm-000001", authorization.PermissionManifestRead)
	manifest, err := authorization.NewService(db).Manifest(context.Background(), "account-union-001")
	if err != nil || len(manifest.Menus) != 1 || manifest.Menus[0].Key != "iam-users" {
		t.Fatalf("cross-role capability union = %#v, %v", manifest, err)
	}
}

func TestPermissionDecisionUsesOnlyGrantingRoles(t *testing.T) {
	db := authorizationDatabase(t)
	seedAccount(t, db, "account-scope-001", "scope-user")
	for _, role := range []struct{ id, key, scope string }{{"role-scope-all01", "scope-all", "all"}, {"role-scope-self1", "scope-self", "self"}} {
		mustExec(t, db, `INSERT INTO iam_roles(id, role_key, name, data_scope, enabled, protected, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, role.id, role.key, role.key, role.scope, true, false)
		mustExec(t, db, `INSERT INTO iam_account_roles(account_id, role_id) VALUES (?, ?)`, "account-scope-001", role.id)
	}
	mustExec(t, db, `INSERT INTO iam_role_permissions(role_id, permission_code) VALUES (?, ?)`, "role-scope-self1", authorization.PermissionUsersRead)
	mustExec(t, db, `INSERT INTO iam_role_permissions(role_id, permission_code) VALUES (?, ?)`, "role-scope-self1", authorization.PermissionManifestRead)
	service := authorization.NewService(db)
	decision, err := service.Require(context.Background(), "account-scope-001", authorization.PermissionUsersRead)
	if err != nil || decision.Scope != authorization.ScopeSelf {
		t.Fatalf("decision = %#v, %v", decision, err)
	}
	manifest, err := service.Manifest(context.Background(), "account-scope-001")
	if err != nil || manifest.Scope != authorization.ScopeSelf {
		t.Fatalf("manifest = %#v, %v", manifest, err)
	}
}

func TestManifestRequiresItsStablePermissionCode(t *testing.T) {
	db := authorizationDatabase(t)
	seedAccount(t, db, "account-no-manifest", "no-manifest")
	mustExec(t, db, `INSERT INTO iam_roles(id, role_key, name, data_scope, enabled, protected, created_at, updated_at) VALUES (?, ?, ?, 'self', ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, "role-no-manifest", "no-manifest", "No manifest", true, false)
	mustExec(t, db, `INSERT INTO iam_role_permissions(role_id, permission_code) VALUES (?, ?)`, "role-no-manifest", authorization.PermissionUsersRead)
	mustExec(t, db, `INSERT INTO iam_account_roles(account_id, role_id) VALUES (?, ?)`, "account-no-manifest", "role-no-manifest")
	if _, err := authorization.NewService(db).Manifest(context.Background(), "account-no-manifest"); !errors.Is(err, authorization.ErrDenied) {
		t.Fatalf("manifest leaked without permission: %v", err)
	}
}

func authorizationDatabase(t *testing.T) *database.Database {
	t.Helper()
	db, err := database.NewProcess().Open(context.Background(), database.Config{Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "authorization.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner, err := migrations.NewRunner(sessionmigration.Provider{}, administrationmigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedAccount(t *testing.T, db *database.Database, id, username string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO iam_accounts(id, username, display_name, email, password_hash, password_changed_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, id, username, username, username+"@example.test", "$argon2id$v=19$m=65536,t=3,p=4$placeholder$placeholder")
}

func mustExec(t *testing.T, db *database.Database, query string, args ...any) {
	t.Helper()
	if _, err := db.Bun().ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}
