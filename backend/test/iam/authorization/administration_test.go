package authorization_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/account"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/administration"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	administrationmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0020-administration-schema"
	sessionprotectionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0040-session-protection"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
)

func TestAdministrationConstructorOwnsAuthorizationDatabase(t *testing.T) {
	constructor := reflect.TypeOf(administration.NewService)
	options := reflect.TypeOf([]administration.Option{})
	if !constructor.IsVariadic() || constructor.NumIn() != 2 || constructor.In(1) != options {
		t.Fatalf("constructor permits a split authorization owner: %v", constructor)
	}
}

func TestAdministrationClosesRolePermissionAndDataScopeLoop(t *testing.T) {
	_, service := newAdministrationFixture(t)
	ctx := context.Background()
	created, err := service.CreateUser(ctx, adminID, administration.CreateUser{Username: "reader", DisplayName: "Reader", Email: "reader@example.test", Password: "reader password value"})
	if err != nil {
		t.Fatal(err)
	}
	role, err := service.CreateRole(ctx, adminID, "reader", "Reader", authorization.ScopeSelf)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetRoleGrants(ctx, adminID, role.ID, []string{authorization.PermissionUsersRead, authorization.PermissionManifestRead}, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.SetUserRoles(ctx, adminID, created.ID, []string{role.ID}); err != nil {
		t.Fatal(err)
	}

	page, err := service.ListUsers(ctx, created.ID, "", 1, 20)
	if err != nil || page.Total != 1 || len(page.Rows) != 1 || page.Rows[0].ID != created.ID {
		t.Fatalf("self-scoped list = %#v, %v", page, err)
	}
	if _, err := service.GetUser(ctx, created.ID, adminID); !errors.Is(err, administration.ErrDenied) {
		t.Fatalf("self-scoped detail escaped: %v", err)
	}
	if _, err := service.CreateUser(ctx, created.ID, administration.CreateUser{Username: "forbidden", DisplayName: "Forbidden", Email: "forbidden@example.test", Password: "forbidden password"}); !errors.Is(err, administration.ErrDenied) {
		t.Fatalf("self-scoped write escaped: %v", err)
	}

	role.Enabled = false
	if err := service.UpdateRole(ctx, adminID, role); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListUsers(ctx, created.ID, "", 1, 20); !errors.Is(err, administration.ErrDenied) {
		t.Fatalf("disabled role remained effective: %v", err)
	}
}

func TestProtectedReferencesAndDuplicateCommandsAreAtomic(t *testing.T) {
	db, service := newAdministrationFixture(t)
	ctx := context.Background()
	if err := service.DeleteRole(ctx, adminID, "role-system-admin"); !errors.Is(err, administration.ErrConflict) {
		t.Fatalf("protected role deletion = %v", err)
	}
	if err := service.DeleteMenu(ctx, adminID, "menu-iam-users-01"); !errors.Is(err, administration.ErrConflict) {
		t.Fatalf("protected menu deletion = %v", err)
	}

	role, err := service.CreateRole(ctx, adminID, "operator", "Operator", authorization.ScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateRole(ctx, adminID, "operator", "Duplicate", authorization.ScopeAll); !errors.Is(err, administration.ErrConflict) {
		t.Fatalf("duplicate role = %v", err)
	}
	if err := service.SetRoleGrants(ctx, adminID, role.ID, []string{authorization.PermissionUsersRead, authorization.PermissionUsersRead}, nil); !errors.Is(err, administration.ErrConflict) {
		t.Fatalf("duplicate grant = %v", err)
	}
	var grantCount int
	if err := db.Bun().QueryRowContext(ctx, `SELECT COUNT(*) FROM iam_role_permissions WHERE role_id = ?`, role.ID).Scan(&grantCount); err != nil {
		t.Fatal(err)
	}
	if grantCount != 0 {
		t.Fatal("rejected duplicate command partially changed grants")
	}

	if _, err := db.Bun().ExecContext(ctx, `INSERT INTO iam_registered_pages(route_key,path,permission_code) VALUES ('operator','/iam/operator','iam.users.read')`); err != nil {
		t.Fatal(err)
	}
	menu, err := service.CreateMenu(ctx, adminID, administration.Menu{Key: "operator", Label: "Operator", Path: "/iam/operator", PermissionCode: authorization.PermissionUsersRead, SortOrder: 50})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetRoleGrants(ctx, adminID, role.ID, []string{authorization.PermissionUsersRead}, []string{menu.ID}); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteMenu(ctx, adminID, menu.ID); !errors.Is(err, administration.ErrConflict) {
		t.Fatalf("referenced menu deletion = %v", err)
	}
	target, err := service.CreateUser(ctx, adminID, administration.CreateUser{Username: "role-target", DisplayName: "Role Target", Email: "role-target@example.test", Password: "role target password"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetUserRoles(ctx, adminID, target.ID, []string{role.ID}); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteRole(ctx, adminID, role.ID); !errors.Is(err, administration.ErrConflict) {
		t.Fatalf("referenced role deletion = %v", err)
	}
}

func TestProtectedAdministratorRolesCannotBeCleared(t *testing.T) {
	db, service := newAdministrationFixture(t)
	ctx := context.Background()

	if err := service.SetUserRoles(ctx, adminID, adminID, nil); !errors.Is(err, administration.ErrConflict) {
		t.Fatalf("protected role assignment = %v", err)
	}
	var count int
	if err := db.Bun().QueryRowContext(ctx, `SELECT COUNT(*) FROM iam_account_roles WHERE account_id = ? AND role_id = ?`, adminID, "role-system-admin").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("protected role assignment changed join rows: %d", count)
	}
}

func TestMenuUpdateNormalizesAndValidatesBeforeMutation(t *testing.T) {
	db, service := newAdministrationFixture(t)
	ctx := context.Background()
	if _, err := db.Bun().ExecContext(ctx, `INSERT INTO iam_registered_pages(route_key,path,permission_code) VALUES ('reports','/iam/reports','iam.users.read'), ('reports-updated','/iam/reports-updated','iam.users.read')`); err != nil {
		t.Fatal(err)
	}
	menu, err := service.CreateMenu(ctx, adminID, administration.Menu{
		Key: "reports", Label: "Reports", Path: "/iam/reports",
		PermissionCode: authorization.PermissionUsersRead, SortOrder: 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	menu.Key = "reports-updated"
	menu.Label = "  Updated Reports  "
	menu.Path = "  /iam/reports-updated  "
	menu.PermissionCode = "  " + authorization.PermissionUsersRead + "  "
	menu.SortOrder = 21
	if err := service.UpdateMenu(ctx, adminID, menu); err != nil {
		t.Fatal(err)
	}
	var key, label, path, permission string
	var sortOrder int
	if err := db.Bun().QueryRowContext(ctx, `SELECT menu_key, label, path, permission_code, sort_order FROM iam_menus WHERE id = ?`, menu.ID).Scan(&key, &label, &path, &permission, &sortOrder); err != nil {
		t.Fatal(err)
	}
	if key != "reports-updated" || label != "Updated Reports" || path != "/iam/reports-updated" || permission != authorization.PermissionUsersRead || sortOrder != 21 {
		t.Fatalf("normalized menu = %q %q %q %q %d", key, label, path, permission, sortOrder)
	}

	for _, invalid := range []administration.Menu{
		{ID: menu.ID, Key: "Reports Updated", Label: "Updated Reports", Path: "/iam/reports-updated", PermissionCode: authorization.PermissionUsersRead, SortOrder: 21},
		{ID: menu.ID, Key: "reports-updated", Label: "Updated Reports", Path: "IAM/UPPER", PermissionCode: authorization.PermissionUsersRead, SortOrder: 21},
		{ID: menu.ID, Key: "reports-updated", Label: "Updated Reports", Path: "/iam/reports-updated", PermissionCode: authorization.PermissionUsersRead, SortOrder: -1},
		{ID: menu.ID, Key: "reports-updated", Label: "Updated Reports", Path: "/iam/reports-updated", PermissionCode: authorization.PermissionUsersRead, SortOrder: 100001},
	} {
		if err := service.UpdateMenu(ctx, adminID, invalid); !errors.Is(err, administration.ErrValidation) {
			t.Fatalf("invalid menu update = %v", err)
		}
	}
	if err := db.Bun().QueryRowContext(ctx, `SELECT menu_key, path, sort_order FROM iam_menus WHERE id = ?`, menu.ID).Scan(&key, &path, &sortOrder); err != nil {
		t.Fatal(err)
	}
	if key != "reports-updated" || path != "/iam/reports-updated" || sortOrder != 21 {
		t.Fatalf("invalid update changed menu = %q %q %d", key, path, sortOrder)
	}
}

func TestStableKeysBoundedPaginationAndMigrationConstraints(t *testing.T) {
	db, service := newAdministrationFixture(t)
	ctx := context.Background()
	if _, err := service.ListUsers(ctx, adminID, "", 1_000_001, 20); !errors.Is(err, administration.ErrValidation) {
		t.Fatalf("unbounded page = %v", err)
	}
	for _, key := range []string{"Bad Key", "-leading", "upper" + strings.ToUpper("case")} {
		if _, err := service.CreateRole(ctx, adminID, key, "Invalid role", authorization.ScopeAll); !errors.Is(err, administration.ErrValidation) {
			t.Fatalf("invalid role key %q = %v", key, err)
		}
		if _, err := service.CreateMenu(ctx, adminID, administration.Menu{Key: key, Label: "Invalid menu", Path: "/iam/invalid", PermissionCode: authorization.PermissionUsersRead}); !errors.Is(err, administration.ErrValidation) {
			t.Fatalf("invalid menu key %q = %v", key, err)
		}
	}
	if _, err := db.Bun().ExecContext(ctx, `INSERT INTO iam_roles(id, role_key, name, data_scope, enabled, protected, created_at, updated_at) VALUES (?, ?, ?, 'all', 1, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, "role-invalid-key1", "invalid key", "Invalid"); err == nil {
		t.Fatal("SQLite role key constraint accepted invalid key")
	}
	if _, err := db.Bun().ExecContext(ctx, `INSERT INTO iam_menus(id, menu_key, label, path, permission_code, sort_order, protected, created_at, updated_at) VALUES (?, ?, ?, ?, NULL, 1, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, "menu-invalid-null", "valid-menu", "Invalid", "/iam/invalid-null"); err == nil {
		t.Fatal("SQLite menu permission accepted NULL")
	}
	var permissionNotNull int
	if err := db.Bun().QueryRowContext(ctx, `SELECT "notnull" FROM pragma_table_info('iam_menus') WHERE name = 'permission_code'`).Scan(&permissionNotNull); err != nil || permissionNotNull != 0 {
		t.Fatalf("SQLite menu permission nullability = %d, %v", permissionNotNull, err)
	}
}

func TestSelfScopeCannotReadGlobalAdministrationMetadata(t *testing.T) {
	_, service := newAdministrationFixture(t)
	ctx := context.Background()
	user, err := service.CreateUser(ctx, adminID, administration.CreateUser{Username: "metadata-self", DisplayName: "Metadata Self", Email: "metadata-self@example.test", Password: "metadata self password"})
	if err != nil {
		t.Fatal(err)
	}
	role, err := service.CreateRole(ctx, adminID, "metadata-self", "Metadata self", authorization.ScopeSelf)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetRoleGrants(ctx, adminID, role.ID, []string{authorization.PermissionRolesRead, authorization.PermissionMenusRead, authorization.PermissionPermissionsRead}, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.SetUserRoles(ctx, adminID, user.ID, []string{role.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListRoles(ctx, user.ID); !errors.Is(err, administration.ErrDenied) {
		t.Fatalf("self role list = %v", err)
	}
	if _, err := service.ListMenus(ctx, user.ID); !errors.Is(err, administration.ErrDenied) {
		t.Fatalf("self menu list = %v", err)
	}
	if _, err := service.ListPermissions(ctx, user.ID); !errors.Is(err, administration.ErrDenied) {
		t.Fatalf("self permission list = %v", err)
	}
}

func TestPasswordResetHashesAndRevokesWithoutReturningSensitiveMaterial(t *testing.T) {
	db, service := newAdministrationFixture(t)
	ctx := context.Background()
	target, err := service.CreateUser(ctx, adminID, administration.CreateUser{Username: "target", DisplayName: "Target", Email: "target@example.test", Password: "original password value"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Bun().ExecContext(ctx, `INSERT INTO iam_sessions(id, account_id, token_hash, generation, csrf_hash, state, created_at, last_seen_at, idle_expires_at, absolute_expires_at, rotate_at) VALUES (?, ?, ?, 0, ?, 'active', ?, ?, ?, ?, ?)`, strings.Repeat("s", 43), target.ID, strings.Repeat("a", 64), strings.Repeat("b", 64), time.Now().UTC(), time.Now().UTC(), time.Now().Add(time.Hour).UTC(), time.Now().Add(2*time.Hour).UTC(), time.Now().Add(time.Hour).UTC()); err != nil {
		t.Fatal(err)
	}

	const replacement = "replacement password value"
	if err := service.ResetPassword(ctx, adminID, target.ID, replacement); err != nil {
		t.Fatal(err)
	}
	var hash, state string
	var generation int64
	if err := db.Bun().QueryRowContext(ctx, `SELECT password_hash, session_generation FROM iam_accounts WHERE id = ?`, target.ID).Scan(&hash, &generation); err != nil {
		t.Fatal(err)
	}
	if err := db.Bun().QueryRowContext(ctx, `SELECT state FROM iam_sessions WHERE account_id = ?`, target.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if hash == replacement || !strings.HasPrefix(hash, "$argon2id$") || !account.VerifyPassword(hash, replacement) || generation != 1 || state != "revoked" {
		t.Fatalf("reset state invalid: hashPolicy=%t generation=%d state=%q", strings.HasPrefix(hash, "$argon2id$"), generation, state)
	}
}

func TestDisableAdvancesGenerationAndRevokesWhileSelfScopeCannotChangeStatusOrReset(t *testing.T) {
	db, service := newAdministrationFixture(t)
	ctx := context.Background()
	target, err := service.CreateUser(ctx, adminID, administration.CreateUser{Username: "limited", DisplayName: "Limited", Email: "limited@example.test", Password: "limited password value"})
	if err != nil {
		t.Fatal(err)
	}
	role, err := service.CreateRole(ctx, adminID, "limited", "Limited", authorization.ScopeSelf)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetRoleGrants(ctx, adminID, role.ID, []string{authorization.PermissionUsersRead, authorization.PermissionUsersWrite, authorization.PermissionUsersResetPassword}, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.SetUserRoles(ctx, adminID, target.ID, []string{role.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Bun().ExecContext(ctx, `INSERT INTO iam_sessions(id, account_id, token_hash, generation, csrf_hash, state, created_at, last_seen_at, idle_expires_at, absolute_expires_at, rotate_at) VALUES (?, ?, ?, 0, ?, 'active', ?, ?, ?, ?, ?)`, strings.Repeat("t", 43), target.ID, strings.Repeat("c", 64), strings.Repeat("d", 64), time.Now().UTC(), time.Now().UTC(), time.Now().Add(time.Hour).UTC(), time.Now().Add(2*time.Hour).UTC(), time.Now().Add(time.Hour).UTC()); err != nil {
		t.Fatal(err)
	}

	if _, err := service.UpdateUser(ctx, target.ID, target.ID, "Limited", "limited@example.test", false); !errors.Is(err, administration.ErrDenied) {
		t.Fatalf("self scope disabled account: %v", err)
	}
	if err := service.ResetPassword(ctx, target.ID, target.ID, "self reset password"); !errors.Is(err, administration.ErrDenied) {
		t.Fatalf("self scope reset password: %v", err)
	}
	if _, err := service.UpdateUser(ctx, adminID, target.ID, "Limited", "limited@example.test", false); err != nil {
		t.Fatal(err)
	}
	var generation int64
	var state string
	if err := db.Bun().QueryRowContext(ctx, `SELECT session_generation FROM iam_accounts WHERE id = ?`, target.ID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if err := db.Bun().QueryRowContext(ctx, `SELECT state FROM iam_sessions WHERE account_id = ?`, target.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if generation != 1 || state != "revoked" {
		t.Fatalf("disable fence = generation %d, session %q", generation, state)
	}
}

func TestMigrationHasNoTenantCasbinOrJWTState(t *testing.T) {
	db, _ := newAdministrationFixture(t)
	for _, forbidden := range []string{"tenant", "casbin", "jwt"} {
		var count int
		if err := db.Bun().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sqlite_master WHERE lower(name) LIKE ?`, "%"+forbidden+"%").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("forbidden schema %q exists", forbidden)
		}
	}
}

const adminID = "account-admin-001"

func newAdministrationFixture(t *testing.T) (*database.Database, *administration.Service) {
	t.Helper()
	db, err := database.NewProcess().Open(context.Background(), database.Config{Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "iam.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner, err := migrations.NewRunner(sessionmigration.Provider{}, administrationmigration.Provider{}, sessionprotectionmigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	hash, err := account.HashPassword("administrator password")
	if err != nil {
		t.Fatal(err)
	}
	repository := account.NewRepository(db.Dialect())
	if err := db.WithinTx(context.Background(), func(ctx context.Context, tx database.Tx) error {
		return repository.Create(ctx, tx, account.Credential{Profile: account.Profile{ID: adminID, Username: "admin", DisplayName: "Administrator", Email: "admin@example.test"}, PasswordHash: hash}, time.Now().UTC())
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Bun().ExecContext(context.Background(), `INSERT INTO iam_account_roles(account_id, role_id) VALUES (?, ?)`, adminID, "role-system-admin"); err != nil {
		t.Fatal(err)
	}
	service, err := administration.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	return db, service
}

func TestLastAdministratorProtectionUsesRoleInsteadOfUsername(t *testing.T) {
	db, service := newAdministrationFixture(t)
	ctx := context.Background()
	if _, err := db.Bun().ExecContext(ctx, `UPDATE iam_accounts SET username = ? WHERE id = ?`, "first.admin", adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateUser(ctx, adminID, adminID, "Administrator", "admin@example.test", false); !errors.Is(err, administration.ErrConflict) {
		t.Fatalf("disable last administrator: %v", err)
	}
	if err := service.SetUserRoles(ctx, adminID, adminID, nil); !errors.Is(err, administration.ErrConflict) {
		t.Fatalf("remove last administrator: %v", err)
	}
}

func TestUnrelatedRoleCannotWidenPermissionScope(t *testing.T) {
	_, service := newAdministrationFixture(t)
	ctx := context.Background()
	user, err := service.CreateUser(ctx, adminID, administration.CreateUser{Username: "scope-check", DisplayName: "Scope", Email: "scope@example.test", Password: "scope regression password"})
	if err != nil {
		t.Fatal(err)
	}
	self, err := service.CreateRole(ctx, adminID, "scope-self", "Self", authorization.ScopeSelf)
	if err != nil {
		t.Fatal(err)
	}
	all, err := service.CreateRole(ctx, adminID, "scope-unrelated", "Unrelated", authorization.ScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetRoleGrants(ctx, adminID, self.ID, []string{authorization.PermissionUsersRead}, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.SetUserRoles(ctx, adminID, user.ID, []string{self.ID, all.ID}); err != nil {
		t.Fatal(err)
	}
	page, err := service.ListUsers(ctx, user.ID, "", 1, 20)
	if err != nil || page.Total != 1 {
		t.Fatalf("scope widened: total=%d err=%v", page.Total, err)
	}
}

func TestMenuTreeRegistrationCyclesAndRevisionConflicts(t *testing.T) {
	_, service := newAdministrationFixture(t)
	ctx := context.Background()
	directory, err := service.CreateMenu(ctx, adminID, administration.Menu{Key: "settings-root", Label: "系统设置", Kind: "directory", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	menus, err := service.ListMenus(ctx, adminID)
	if err != nil {
		t.Fatal(err)
	}
	var page administration.Menu
	for _, v := range menus {
		if v.Key == "iam-users" {
			page = v
		}
	}
	page.ParentID = directory.ID
	page.Label = "成员管理"
	if err := service.UpdateMenu(ctx, adminID, page); err != nil {
		t.Fatal(err)
	}
	if err := service.UpdateMenu(ctx, adminID, page); !errors.Is(err, administration.ErrConflict) {
		t.Fatalf("stale edit accepted: %v", err)
	}
	if _, err := service.CreateMenu(ctx, adminID, administration.Menu{Key: "unsafe-page", Label: "任意页面", Kind: "page", Path: "https://evil.test", Enabled: true}); !errors.Is(err, administration.ErrValidation) {
		t.Fatalf("unregistered page accepted: %v", err)
	}
	directory.ParentID = directory.ID
	if err := service.UpdateMenu(ctx, adminID, directory); !errors.Is(err, administration.ErrConflict) {
		t.Fatalf("menu cycle accepted: %v", err)
	}
	directory.ParentID = ""
	directory.Hidden = true
	if err := service.UpdateMenu(ctx, adminID, directory); err != nil {
		t.Fatal(err)
	}
	manifest, err := service.Manifest(ctx, adminID)
	if err != nil {
		t.Fatal(err)
	}
	for _, menu := range manifest.Menus {
		if menu.Key == "iam-users" || menu.ID == directory.ID {
			t.Fatal("hidden ancestor exposed navigation")
		}
	}
}

func TestMovingMenuSubtreeCannotExceedNavigationDepth(t *testing.T) {
	_, service := newAdministrationFixture(t)
	ctx := context.Background()
	var parent string
	for i := 0; i < 15; i++ {
		directory, err := service.CreateMenu(ctx, adminID, administration.Menu{Key: fmt.Sprintf("depth-%02d", i), Label: "目录", Kind: "directory", ParentID: parent, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		parent = directory.ID
	}
	root, err := service.CreateMenu(ctx, adminID, administration.Menu{Key: "moving-root", Label: "移动根目录", Kind: "directory", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateMenu(ctx, adminID, administration.Menu{Key: "moving-child", Label: "子目录", Kind: "directory", ParentID: root.ID, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	root.ParentID = parent
	if err := service.UpdateMenu(ctx, adminID, root); !errors.Is(err, administration.ErrConflict) {
		t.Fatalf("deep subtree move accepted: %v", err)
	}
}
