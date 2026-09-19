package audit

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/contracts/capabilities"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	administrationmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0020-administration-schema"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
)

type capabilityCapture struct {
	values []capabilities.ModuleCapabilities
}

func (capture *capabilityCapture) Register(_ context.Context, value capabilities.ModuleCapabilities) error {
	capture.values = append(capture.values, value)
	return nil
}

func TestRegisterCapabilitiesUsesStableDefinitionsAndOwnedSlices(t *testing.T) {
	capture := &capabilityCapture{}
	if err := RegisterCapabilities(context.Background(), capture); err != nil {
		t.Fatal(err)
	}
	want := capabilities.ModuleCapabilities{
		Permissions: []capabilities.PermissionDefinition{
			{Code: "audit.records.read", Name: "Read audit records"},
			{Code: "audit.records.cleanup", Name: "Clean up audit records"},
		},
		Menus: []capabilities.MenuDefinition{{
			ID: "menu-audit-records", Key: "audit-records", Label: "审计日志", Path: "/audit/records",
			PermissionCode: "audit.records.read", SortOrder: 500,
		}},
	}
	if len(capture.values) != 1 || !reflect.DeepEqual(capture.values[0], want) {
		t.Fatalf("capabilities = %#v", capture.values)
	}

	capture.values[0].Permissions[0].Name = "mutated"
	capture.values[0].Menus[0].PermissionCode = "audit.records.cleanup"
	if err := RegisterCapabilities(context.Background(), capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.values) != 2 || !reflect.DeepEqual(capture.values[1], want) {
		t.Fatalf("second capabilities = %#v", capture.values)
	}
}

func TestRegisterCapabilitiesRejectsNilRegistrar(t *testing.T) {
	if err := RegisterCapabilities(context.Background(), nil); !errors.Is(err, ErrInternal) {
		t.Fatalf("nil registrar = %v", err)
	}
}

func TestRegisterCapabilitiesGrantsProtectedSystemAdministratorCapabilities(t *testing.T) {
	db := openCapabilityDatabase(t)
	registry, err := authorization.NewCapabilityRegistry(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterCapabilities(context.Background(), registry); err != nil {
		t.Fatal(err)
	}
	if err := RegisterCapabilities(context.Background(), registry); err != nil {
		t.Fatalf("idempotent registration = %v", err)
	}

	var permissions, permissionGrants int
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM iam_permissions WHERE code IN (?, ?) AND protected = ?`, string(PermissionRead), string(PermissionCleanup), true).Scan(&permissions); err != nil {
		t.Fatal(err)
	}
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM iam_role_permissions WHERE role_id = ? AND permission_code IN (?, ?)`, "role-system-admin", string(PermissionRead), string(PermissionCleanup)).Scan(&permissionGrants); err != nil {
		t.Fatal(err)
	}
	var menuID, menuKey, label, path, permissionCode string
	var sortOrder int
	var protected bool
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT id, menu_key, label, path, permission_code, sort_order, protected FROM iam_menus WHERE id = ?`, "menu-audit-records").Scan(&menuID, &menuKey, &label, &path, &permissionCode, &sortOrder, &protected); err != nil {
		t.Fatal(err)
	}
	var menuGrants int
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM iam_role_menus WHERE role_id = ? AND menu_id = ?`, "role-system-admin", "menu-audit-records").Scan(&menuGrants); err != nil {
		t.Fatal(err)
	}
	if permissions != 2 || permissionGrants != 2 || menuGrants != 1 ||
		menuID != "menu-audit-records" || menuKey != "audit-records" || label != "审计日志" ||
		path != "/audit/records" || permissionCode != string(PermissionRead) || sortOrder != 500 || !protected {
		t.Fatalf("registry projection permissions=%d grants=%d menuGrants=%d menu=%q/%q/%q/%q/%q/%d protected=%t", permissions, permissionGrants, menuGrants, menuID, menuKey, label, path, permissionCode, sortOrder, protected)
	}

	if _, err := db.Bun().ExecContext(context.Background(), `UPDATE iam_menus SET label = ? WHERE id = ?`, "Tampered audit records", "menu-audit-records"); err != nil {
		t.Fatal(err)
	}
	if err := RegisterCapabilities(context.Background(), registry); err != nil {
		t.Fatalf("display metadata should remain editable = %v", err)
	}
}

func openCapabilityDatabase(t *testing.T) *database.Database {
	t.Helper()
	db, err := database.NewProcess().Open(context.Background(), database.Config{
		Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "audit-capabilities.sqlite3"),
	})
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
