// Package authorization owns the database-backed Permission Code decision boundary.
package authorization

import (
	"context"
	"errors"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

const (
	PermissionUsersRead          = "iam.users.read"
	PermissionUsersWrite         = "iam.users.write"
	PermissionUsersDelete        = "iam.users.delete"
	PermissionUsersResetPassword = "iam.users.reset-password"
	PermissionRolesRead          = "iam.roles.read"
	PermissionRolesWrite         = "iam.roles.write"
	PermissionRolesDelete        = "iam.roles.delete"
	PermissionRolesAssign        = "iam.roles.assign"
	PermissionMenusRead          = "iam.menus.read"
	PermissionMenusWrite         = "iam.menus.write"
	PermissionMenusDelete        = "iam.menus.delete"
	PermissionPermissionsRead    = "iam.permissions.read"
	PermissionManifestRead       = "iam.manifest.read"
)

var (
	ErrDenied   = errors.New("authorization denied")
	ErrInternal = errors.New("authorization decision failed")
)

type Scope string

const (
	ScopeSelf Scope = "self"
	ScopeAll  Scope = "all"
)

type Decision struct{ Scope Scope }

type Menu struct {
	ID, ParentID, Kind, RouteKey, Icon string
	Key, Label, Path, PermissionCode   string
	SortOrder                          int
}

type Manifest struct {
	Permissions []string
	Menus       []Menu
	Scope       Scope
}

type Database interface {
	WithinTx(context.Context, func(context.Context, database.Tx) error) error
	Dialect() database.Dialect
}

type Service struct {
	db      Database
	dialect database.Dialect
}

func NewService(db Database) *Service {
	service := &Service{db: db}
	if db != nil {
		service.dialect = db.Dialect()
	}
	return service
}

// Require deliberately reaches the database for every decision. Correctness never depends on a cache.
func (s *Service) Require(ctx context.Context, accountID, permission string) (Decision, error) {
	if s == nil || s.db == nil || accountID == "" || permission == "" {
		return Decision{}, ErrDenied
	}
	var decision Decision
	err := s.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		var err error
		decision, err = s.RequireInTx(ctx, tx, accountID, permission)
		return err
	})
	if errors.Is(err, ErrDenied) {
		return Decision{}, ErrDenied
	}
	if err != nil {
		return Decision{}, sanitize(ctx, err)
	}
	return decision, nil
}

// RequireInTx is the final application-use-case boundary. PostgreSQL locks every contributing
// row so a concurrent revoke cannot commit between this decision and its protected mutation.
func (s *Service) RequireInTx(ctx context.Context, tx database.Tx, accountID, permission string) (Decision, error) {
	if s == nil || tx == nil || accountID == "" || permission == "" {
		return Decision{}, ErrDenied
	}
	query := `SELECT permission_role.data_scope
		FROM iam_accounts a
		JOIN iam_account_roles permission_ar ON permission_ar.account_id = a.id
		JOIN iam_roles permission_role ON permission_role.id = permission_ar.role_id AND permission_role.enabled = ?
		JOIN iam_role_permissions rp ON rp.role_id = permission_role.id AND rp.permission_code = ?
		WHERE a.id = ? AND a.disabled_at IS NULL`
	if s.dialect == database.DialectPostgres {
		query += ` FOR SHARE OF a, permission_ar, permission_role, rp`
	}
	rows, err := tx.QueryContext(ctx, query, true, permission, accountID)
	if err != nil {
		return Decision{}, err
	}
	defer rows.Close()
	decision := Decision{Scope: ScopeSelf}
	found := false
	for rows.Next() {
		var scope string
		if err := rows.Scan(&scope); err != nil {
			return Decision{}, err
		}
		found = true
		if Scope(scope) == ScopeAll {
			decision.Scope = ScopeAll
		}
	}
	if err := rows.Err(); err != nil {
		return Decision{}, err
	}
	if !found {
		return Decision{}, ErrDenied
	}
	return decision, nil
}

// Manifest 返回已授权页面及其目录祖先；展示缓存不参与后端权限决策。
func (s *Service) Manifest(ctx context.Context, accountID string) (Manifest, error) {
	if s == nil || s.db == nil || accountID == "" {
		return Manifest{}, ErrDenied
	}
	manifest := Manifest{Scope: ScopeSelf, Permissions: []string{}, Menus: []Menu{}}
	err := s.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		decision, err := s.RequireInTx(ctx, tx, accountID, PermissionManifestRead)
		if err != nil {
			return err
		}
		manifest.Scope = decision.Scope
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT rp.permission_code FROM iam_account_roles ar JOIN iam_roles r ON r.id=ar.role_id AND r.enabled=? JOIN iam_role_permissions rp ON rp.role_id=r.id WHERE ar.account_id=? ORDER BY rp.permission_code`, true, accountID)
		if err != nil {
			return err
		}
		grants := map[string]bool{}
		for rows.Next() {
			var code string
			if err := rows.Scan(&code); err != nil {
				rows.Close()
				return err
			}
			grants[code] = true
			manifest.Permissions = append(manifest.Permissions, code)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, `SELECT m.id,COALESCE(m.parent_id,''),m.kind,m.route_key,m.icon,m.menu_key,m.label,m.path,COALESCE(m.permission_code,''),m.sort_order,m.enabled,m.hidden,EXISTS(SELECT 1 FROM iam_role_menus rm JOIN iam_roles r ON r.id=rm.role_id AND r.enabled=? JOIN iam_account_roles ar ON ar.role_id=r.id WHERE rm.menu_id=m.id AND ar.account_id=?) FROM iam_menus m ORDER BY m.sort_order,m.menu_key`, true, accountID)
		if err != nil {
			return err
		}
		defer rows.Close()
		type item struct {
			Menu
			visible, granted bool
		}
		all := []item{}
		byID := map[string]item{}
		for rows.Next() {
			var v item
			var enabled, hidden bool
			if err := rows.Scan(&v.ID, &v.ParentID, &v.Kind, &v.RouteKey, &v.Icon, &v.Key, &v.Label, &v.Path, &v.PermissionCode, &v.SortOrder, &enabled, &hidden, &v.granted); err != nil {
				return err
			}
			v.visible = enabled && !hidden
			all = append(all, v)
			byID[v.ID] = v
		}
		if err := rows.Err(); err != nil {
			return err
		}
		include := map[string]bool{}
		for _, v := range all {
			if v.Kind != "page" || !v.visible || !v.granted || !grants[v.PermissionCode] {
				continue
			}
			ancestors := []string{v.ID}
			parent := v.ParentID
			valid := true
			for depth := 0; parent != ""; depth++ {
				ancestor, exists := byID[parent]
				if depth >= 16 || !exists || !ancestor.visible || ancestor.Kind != "directory" {
					valid = false
					break
				}
				ancestors = append(ancestors, parent)
				parent = ancestor.ParentID
			}
			if valid {
				for _, id := range ancestors {
					include[id] = true
				}
			}
		}
		for _, v := range all {
			if include[v.ID] {
				manifest.Menus = append(manifest.Menus, v.Menu)
			}
		}
		return nil
	})
	if errors.Is(err, ErrDenied) {
		return Manifest{}, ErrDenied
	}
	if err != nil {
		return Manifest{}, sanitize(ctx, err)
	}
	return manifest, nil
}

func sanitize(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrInternal
}

// ProjectionRevision 只用于展示缓存版本。最终权限判定始终由 RequireInTx 查询数据库。
func (s *Service) ProjectionRevision(ctx context.Context) (int64, error) {
	var revision int64
	err := s.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT revision FROM iam_projection_revision WHERE singleton = 1`).Scan(&revision)
	})
	return revision, err
}
