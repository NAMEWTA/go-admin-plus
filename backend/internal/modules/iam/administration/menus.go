package administration

import (
	"context"
	"strings"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	audit "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/auditing"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/google/uuid"
)

// 菜单保存展示属性；页面必须来自代码注册，按钮权限仍由后端逐次校验。
func (s *Service) ListMenus(ctx context.Context, actorID string) ([]Menu, error) {
	result := []Menu{}
	err := s.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		decision, err := s.authorizer.RequireInTx(ctx, tx, actorID, authorization.PermissionMenusRead)
		if err != nil {
			return err
		}
		if decision.Scope != authorization.ScopeAll {
			return ErrDenied
		}
		rows, err := tx.QueryContext(ctx, `SELECT id,menu_key,label,path,COALESCE(permission_code,''),sort_order,protected,COALESCE(parent_id,''),kind,route_key,icon,enabled,hidden,revision FROM iam_menus ORDER BY sort_order,menu_key`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v Menu
			if err := rows.Scan(&v.ID, &v.Key, &v.Label, &v.Path, &v.PermissionCode, &v.SortOrder, &v.Protected, &v.ParentID, &v.Kind, &v.RouteKey, &v.Icon, &v.Enabled, &v.Hidden, &v.Revision); err != nil {
				return err
			}
			result = append(result, v)
		}
		return rows.Err()
	})
	return result, s.normalize(ctx, err)
}

func (s *Service) CreateMenu(ctx context.Context, actorID string, value Menu) (Menu, error) {
	value.ID = uuid.NewString()
	value.Revision = 1
	if value.Kind == "" {
		value.Kind = "page"
		value.Enabled = true
	}
	err := s.write(ctx, actorID, authorization.PermissionMenusWrite, func(ctx context.Context, tx database.Tx) error {
		if err := validateMenuTree(ctx, tx, &value); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO iam_menus(id,menu_key,label,path,permission_code,sort_order,protected,parent_id,kind,route_key,icon,enabled,hidden,revision,created_at,updated_at) VALUES (?,?,?,?,?,?,?, ?,?,?,?,?,?,?,?,?)`, value.ID, value.Key, value.Label, value.Path, nullableString(value.PermissionCode), value.SortOrder, false, nullableString(value.ParentID), value.Kind, value.RouteKey, value.Icon, value.Enabled, value.Hidden, 1, s.now().UTC(), s.now().UTC())
		return err
	}, audit.Operation{Action: "create", ResourceType: "iam_menu", ResourceID: value.ID, ActorID: actorID, Code: "CreateMenu"})
	return value, err
}

func (s *Service) UpdateMenu(ctx context.Context, actorID string, value Menu) error {
	if value.ID == "" || value.Revision < 1 {
		return ErrValidation
	}
	return s.write(ctx, actorID, authorization.PermissionMenusWrite, func(ctx context.Context, tx database.Tx) error {
		var protected bool
		var key, path, permission, kind string
		var revision int64
		if err := s.queryRowForUpdate(tx, ctx, `SELECT protected,menu_key,path,COALESCE(permission_code,''),kind,revision FROM iam_menus WHERE id = ?`, value.ID).Scan(&protected, &key, &path, &permission, &kind, &revision); err != nil {
			return err
		}
		if revision != value.Revision {
			return ErrConflict
		}
		if protected && (value.Key != key || value.Path != path || value.PermissionCode != permission || value.Kind != kind) {
			return ErrConflict
		}
		if err := validateMenuTree(ctx, tx, &value); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE iam_menus SET menu_key=?,label=?,path=?,permission_code=?,sort_order=?,parent_id=?,kind=?,route_key=?,icon=?,enabled=?,hidden=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, value.Key, value.Label, value.Path, nullableString(value.PermissionCode), value.SortOrder, nullableString(value.ParentID), value.Kind, value.RouteKey, value.Icon, value.Enabled, value.Hidden, s.now().UTC(), value.ID, value.Revision)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrConflict
		}
		return nil
	}, audit.Operation{Action: "update", ResourceType: "iam_menu", ResourceID: value.ID, ActorID: actorID, Code: "UpdateMenu"})
}

func (s *Service) DeleteMenu(ctx context.Context, actorID, menuID string) error {
	return s.write(ctx, actorID, authorization.PermissionMenusDelete, func(ctx context.Context, tx database.Tx) error {
		var protected bool
		if err := s.queryRowForUpdate(tx, ctx, `SELECT protected FROM iam_menus WHERE id=?`, menuID).Scan(&protected); err != nil {
			return err
		}
		if protected {
			return ErrConflict
		}
		var refs int
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM iam_role_menus WHERE menu_id=?) + (SELECT COUNT(*) FROM iam_menus WHERE parent_id=?)`, menuID, menuID).Scan(&refs); err != nil {
			return err
		}
		if refs > 0 {
			return ErrConflict
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM iam_menus WHERE id=?`, menuID)
		return err
	}, audit.Operation{Action: "delete", ResourceType: "iam_menu", ResourceID: menuID, ActorID: actorID, Code: "DeleteMenu"})
}

func validateMenuTree(ctx context.Context, tx database.Tx, value *Menu) error {
	value.Label = strings.TrimSpace(value.Label)
	value.Path = strings.TrimSpace(value.Path)
	value.PermissionCode = strings.TrimSpace(value.PermissionCode)
	if !validStableKey(value.Key) || value.Label == "" || len(value.Label) > 80 || value.SortOrder < 0 || value.SortOrder > 100000 || len(value.Icon) > 64 {
		return ErrValidation
	}
	switch value.Kind {
	case "directory":
		value.Path = ""
		value.PermissionCode = ""
		value.RouteKey = ""
	case "page":
		// 客户端不能指定组件文件名或任意 URL，路由注册表只接受编译时能力声明。
		if err := tx.QueryRowContext(ctx, `SELECT route_key,permission_code FROM iam_registered_pages WHERE path=?`, value.Path).Scan(&value.RouteKey, &value.PermissionCode); err != nil {
			return ErrValidation
		}
	case "button":
		value.Path = ""
		value.RouteKey = ""
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM iam_permissions WHERE code=?`, value.PermissionCode).Scan(&count); err != nil {
			return err
		}
		if count != 1 || value.ParentID == "" {
			return ErrValidation
		}
	default:
		return ErrValidation
	}
	// 每次父链最多 16 层，禁止自引用、环和按钮下挂子项。
	parent := value.ParentID
	ancestors := 0
	for ; parent != ""; ancestors++ {
		if ancestors >= 15 || parent == value.ID {
			return ErrConflict
		}
		var kind, next string
		if err := tx.QueryRowContext(ctx, `SELECT kind,COALESCE(parent_id,'') FROM iam_menus WHERE id=?`, parent).Scan(&kind, &next); err != nil {
			return ErrValidation
		}
		if kind == "button" || (ancestors == 0 && value.Kind != "button" && kind != "directory") {
			return ErrValidation
		}
		parent = next
	}
	var children int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM iam_menus WHERE parent_id=?`, value.ID).Scan(&children); err != nil {
		return err
	}
	if children > 0 && value.Kind == "button" {
		return ErrConflict
	}
	// 移动整个子树时同时校验后代深度；不能通过多次移动突破导航层数上限。
	var subtreeDepth int
	if err := tx.QueryRowContext(ctx, `WITH RECURSIVE descendants(id,depth) AS (
		SELECT id,1 FROM iam_menus WHERE id=?
		UNION ALL SELECT m.id,d.depth+1 FROM iam_menus m JOIN descendants d ON m.parent_id=d.id WHERE d.depth<=16
	) SELECT COALESCE(MAX(depth),1) FROM descendants`, value.ID).Scan(&subtreeDepth); err != nil {
		return err
	}
	if ancestors+subtreeDepth > 16 {
		return ErrConflict
	}
	return nil
}
func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
