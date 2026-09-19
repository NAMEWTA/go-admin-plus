package authorization

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/contracts/capabilities"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

const systemAdministratorRoleKey = "system-admin"

var (
	ErrCapabilityRegistryInvalid  = errors.New("capability registry definition invalid")
	ErrCapabilityRegistryConflict = errors.New("capability registry conflict")
	permissionCodePattern         = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)+$`)
	stableKeyPattern              = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	stablePathPattern             = regexp.MustCompile(`^/[a-z0-9][a-z0-9/_-]*$`)
)

type CapabilityRegistry struct{ db Database }

func NewCapabilityRegistry(db Database) (*CapabilityRegistry, error) {
	if db == nil || (db.Dialect() != database.DialectSQLite && db.Dialect() != database.DialectPostgres) {
		return nil, ErrCapabilityRegistryInvalid
	}
	return &CapabilityRegistry{db: db}, nil
}

// Register atomically owns module permissions, protected navigation and system-admin grants.
func (r *CapabilityRegistry) Register(ctx context.Context, definitions capabilities.ModuleCapabilities) error {
	definitions, err := validateModuleCapabilities(definitions)
	if err != nil {
		return err
	}
	err = r.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		var roleID string
		var protected, enabled bool
		err := tx.QueryRowContext(ctx, `SELECT id, protected, enabled FROM iam_roles WHERE role_key = ?`, systemAdministratorRoleKey).Scan(&roleID, &protected, &enabled)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCapabilityRegistryConflict
		}
		if err != nil {
			return err
		}
		if !protected || !enabled || roleID == "" {
			return ErrCapabilityRegistryConflict
		}
		for _, definition := range definitions.Permissions {
			if _, err := tx.ExecContext(ctx, `INSERT INTO iam_permissions(code, name, protected) VALUES (?, ?, ?) ON CONFLICT(code) DO NOTHING`, definition.Code, definition.Name, true); err != nil {
				return err
			}
			var name string
			var permissionProtected bool
			if err := tx.QueryRowContext(ctx, `SELECT name, protected FROM iam_permissions WHERE code = ?`, definition.Code).Scan(&name, &permissionProtected); err != nil {
				return err
			}
			if name != definition.Name || !permissionProtected {
				return ErrCapabilityRegistryConflict
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO iam_role_permissions(role_id, permission_code) VALUES (?, ?) ON CONFLICT(role_id, permission_code) DO NOTHING`, roleID, definition.Code); err != nil {
				return err
			}
		}
		for _, definition := range definitions.Menus {
			if _, err := tx.ExecContext(ctx, `INSERT INTO iam_registered_pages(route_key,path,permission_code) VALUES (?,?,?) ON CONFLICT DO NOTHING`, definition.Key, definition.Path, definition.PermissionCode); err != nil {
				return err
			}
			var path, permission string
			if err := tx.QueryRowContext(ctx, `SELECT path,permission_code FROM iam_registered_pages WHERE route_key = ?`, definition.Key).Scan(&path, &permission); err != nil {
				return err
			}
			if path != definition.Path || permission != definition.PermissionCode {
				return ErrCapabilityRegistryConflict
			}

			if _, err := tx.ExecContext(ctx, `INSERT INTO iam_menus(id, menu_key, label, path, permission_code, sort_order, protected, route_key, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) ON CONFLICT DO NOTHING`, definition.ID, definition.Key, definition.Label, definition.Path, definition.PermissionCode, definition.SortOrder, true, definition.Key); err != nil {
				return err
			}
			rows, err := tx.QueryContext(ctx, `SELECT id, menu_key, label, path, permission_code, sort_order, protected FROM iam_menus WHERE id = ? OR menu_key = ? OR path = ?`, definition.ID, definition.Key, definition.Path)
			if err != nil {
				return err
			}
			matches := 0
			for rows.Next() {
				var existing capabilities.MenuDefinition
				var menuProtected bool
				if err := rows.Scan(&existing.ID, &existing.Key, &existing.Label, &existing.Path, &existing.PermissionCode, &existing.SortOrder, &menuProtected); err != nil {
					_ = rows.Close()
					return err
				}
				matches++
				if existing.ID != definition.ID || existing.Key != definition.Key || existing.Path != definition.Path || existing.PermissionCode != definition.PermissionCode || !menuProtected {
					_ = rows.Close()
					return ErrCapabilityRegistryConflict
				}
			}
			if err := rows.Close(); err != nil {
				return err
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if matches != 1 {
				return ErrCapabilityRegistryConflict
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO iam_role_menus(role_id, menu_id) VALUES (?, ?) ON CONFLICT(role_id, menu_id) DO NOTHING`, roleID, definition.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil || errors.Is(err, ErrCapabilityRegistryInvalid) || errors.Is(err, ErrCapabilityRegistryConflict) {
		return err
	}
	return sanitize(ctx, err)
}

func validateModuleCapabilities(definitions capabilities.ModuleCapabilities) (capabilities.ModuleCapabilities, error) {
	if len(definitions.Permissions) == 0 || len(definitions.Permissions) > 100 || len(definitions.Menus) > 100 {
		return capabilities.ModuleCapabilities{}, ErrCapabilityRegistryInvalid
	}
	permissions, permissionCodes := make([]capabilities.PermissionDefinition, 0, len(definitions.Permissions)), map[string]struct{}{}
	for _, definition := range definitions.Permissions {
		if len(definition.Code) < 3 || len(definition.Code) > 100 || !permissionCodePattern.MatchString(definition.Code) || !validDisplayText(definition.Name, 100) {
			return capabilities.ModuleCapabilities{}, ErrCapabilityRegistryInvalid
		}
		if _, duplicate := permissionCodes[definition.Code]; duplicate {
			return capabilities.ModuleCapabilities{}, ErrCapabilityRegistryInvalid
		}
		permissionCodes[definition.Code] = struct{}{}
		permissions = append(permissions, definition)
	}
	menus := make([]capabilities.MenuDefinition, 0, len(definitions.Menus))
	menuIDs, menuKeys, menuPaths := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	for _, menu := range definitions.Menus {
		_, permissionExists := permissionCodes[menu.PermissionCode]
		_, duplicateID := menuIDs[menu.ID]
		_, duplicateKey := menuKeys[menu.Key]
		_, duplicatePath := menuPaths[menu.Path]
		if len(menu.ID) < 16 || len(menu.ID) > 64 || !stableKeyPattern.MatchString(menu.ID) || len(menu.Key) < 3 || len(menu.Key) > 64 || !stableKeyPattern.MatchString(menu.Key) || !validDisplayText(menu.Label, 80) || len(menu.Path) > 160 || !stablePathPattern.MatchString(menu.Path) || !permissionExists || menu.SortOrder < 0 || menu.SortOrder > 100000 || duplicateID || duplicateKey || duplicatePath {
			return capabilities.ModuleCapabilities{}, ErrCapabilityRegistryInvalid
		}
		menuIDs[menu.ID] = struct{}{}
		menuKeys[menu.Key] = struct{}{}
		menuPaths[menu.Path] = struct{}{}
		menus = append(menus, menu)
	}
	return capabilities.ModuleCapabilities{Permissions: permissions, Menus: menus}, nil
}

func validDisplayText(value string, maximum int) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	count := utf8.RuneCountInString(value)
	if count < 1 || count > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
