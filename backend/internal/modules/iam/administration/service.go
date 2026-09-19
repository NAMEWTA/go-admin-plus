// Package administration owns IAM management commands and their final authorization boundary.
package administration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	audit "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/auditing"
	"github.com/google/uuid"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/account"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

var (
	ErrDenied     = authorization.ErrDenied
	ErrNotFound   = errors.New("iam administration resource not found")
	ErrValidation = errors.New("iam administration request invalid")
	ErrConflict   = errors.New("iam administration resource conflict")
	ErrInternal   = errors.New("iam administration operation failed")
)

const systemAdministratorKey = "system-admin"

var stableKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

const maximumUserPage = 1_000_000

type Database interface {
	WithinTx(context.Context, func(context.Context, database.Tx) error) error
	Dialect() database.Dialect
}

type User struct {
	Revision                         int64
	ID, Username, DisplayName, Email string
	Disabled                         bool
	RoleIDs                          []string
}

type Role struct {
	Revision           int64
	ID, Key, Name      string
	Scope              authorization.Scope
	Enabled, Protected bool
	PermissionCodes    []string
	MenuIDs            []string
}

type Menu struct {
	ID, Key, Label, Path, PermissionCode string
	ParentID, Kind, RouteKey, Icon       string
	Enabled, Hidden                      bool
	Revision                             int64
	SortOrder                            int
	Protected                            bool
}

type Permission struct{ Code, Name string }

type Page[T any] struct {
	Rows  []T
	Total int
}

type Service struct {
	recorder           audit.OperationPort
	db                 Database
	authorizer         *authorization.Service
	accounts           *account.Repository
	passwordWork       *account.PasswordWorkBudget
	now                func() time.Time
	authorizationProbe func(string)
}

type Option func(*Service)

func WithAuditRecorder(recorder audit.OperationPort) Option {
	return func(s *Service) { s.recorder = recorder }
}

func WithClock(clock func() time.Time) Option { return func(s *Service) { s.now = clock } }
func WithPasswordWorkBudget(value *account.PasswordWorkBudget) Option {
	return func(s *Service) { s.passwordWork = value }
}
func WithAuthorizationProbe(probe func(string)) Option {
	return func(s *Service) { s.authorizationProbe = probe }
}
func NewService(db Database, options ...Option) (*Service, error) {
	if db == nil {
		return nil, errors.New("iam administration database is required")
	}
	service := &Service{db: db, authorizer: authorization.NewService(db), accounts: account.NewRepository(db.Dialect()), passwordWork: account.ProcessPasswordWorkBudget(), now: time.Now}
	for _, option := range options {
		option(service)
	}
	if service.passwordWork == nil || service.now == nil {
		return nil, errors.New("iam administration runtime dependency is required")
	}
	return service, nil
}

func (s *Service) ListUsers(ctx context.Context, actorID, search string, page, pageSize int) (Page[User], error) {
	if page < 1 || page > maximumUserPage || pageSize < 1 || pageSize > 100 {
		return Page[User]{}, ErrValidation
	}
	search = strings.ToLower(strings.TrimSpace(search))
	var result Page[User]
	err := s.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		decision, err := s.authorizer.RequireInTx(ctx, tx, actorID, authorization.PermissionUsersRead)
		if err != nil {
			return err
		}
		where, args := userScopeWhere(decision.Scope, actorID, search)
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM iam_accounts a `+where, args...).Scan(&result.Total); err != nil {
			return err
		}
		args = append(args, pageSize, (page-1)*pageSize)
		rows, err := tx.QueryContext(ctx, `SELECT a.id, a.username, a.display_name, a.email, a.disabled_at, a.revision FROM iam_accounts a `+where+` ORDER BY a.username LIMIT ? OFFSET ?`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var value User
			var disabled sql.NullTime
			if err := rows.Scan(&value.ID, &value.Username, &value.DisplayName, &value.Email, &disabled, &value.Revision); err != nil {
				return err
			}
			value.Disabled = disabled.Valid
			result.Rows = append(result.Rows, value)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		return loadUserRoles(ctx, tx, result.Rows)
	})
	return result, s.normalize(ctx, err)
}

func (s *Service) GetUser(ctx context.Context, actorID, userID string) (User, error) {
	result := User{RoleIDs: []string{}}
	err := s.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		decision, err := s.authorizer.RequireInTx(ctx, tx, actorID, authorization.PermissionUsersRead)
		if err != nil {
			return err
		}
		if decision.Scope == authorization.ScopeSelf && userID != actorID {
			return ErrDenied
		}
		var disabled sql.NullTime
		if err := tx.QueryRowContext(ctx, `SELECT id, username, display_name, email, disabled_at, revision FROM iam_accounts WHERE id = ?`, userID).Scan(&result.ID, &result.Username, &result.DisplayName, &result.Email, &disabled, &result.Revision); err != nil {
			return err
		}
		result.Disabled = disabled.Valid
		rows, err := tx.QueryContext(ctx, `SELECT role_id FROM iam_account_roles WHERE account_id = ? ORDER BY role_id`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			result.RoleIDs = append(result.RoleIDs, id)
		}
		return rows.Err()
	})
	return result, s.normalize(ctx, err)
}

type CreateUser struct{ Username, DisplayName, Email, Password string }

func (s *Service) CreateUser(ctx context.Context, actorID string, input CreateUser) (User, error) {
	input.Username = strings.ToLower(strings.TrimSpace(input.Username))
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	if !validUser(input.Username, input.DisplayName, input.Email) || len(input.Password) < account.MinimumPasswordLength || len(input.Password) > 128 {
		return User{}, ErrValidation
	}
	if _, err := s.authorizer.Require(ctx, actorID, authorization.PermissionUsersWrite); err != nil {
		return User{}, err
	}
	release, acquired := s.passwordWork.TryAcquire()
	if !acquired {
		return User{}, ErrConflict
	}
	defer release()
	hash, err := account.HashPassword(input.Password)
	if err != nil {
		return User{}, ErrInternal
	}
	release()
	now := s.now().UTC()
	value := User{Revision: 1, ID: uuid.NewString(), Username: input.Username, DisplayName: input.DisplayName, Email: input.Email, RoleIDs: []string{}}
	err = s.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		decision, err := s.authorizer.RequireInTx(ctx, tx, actorID, authorization.PermissionUsersWrite)
		if err != nil {
			return err
		}
		if decision.Scope != authorization.ScopeAll {
			return ErrDenied
		}
		if err := s.accounts.Create(ctx, tx, account.Credential{Profile: account.Profile{ID: value.ID, Username: value.Username, DisplayName: value.DisplayName, Email: value.Email}, PasswordHash: hash}, now); err != nil {
			return err
		}
		return audit.Write(s.recorder, ctx, tx, audit.Operation{Action: "create", ResourceType: "iam_user", ResourceID: value.ID, ActorID: actorID, Code: "CreateUser"})
	})
	return value, s.normalize(ctx, err)
}

func (s *Service) UpdateUser(ctx context.Context, actorID, userID, displayName, email string, enabled bool) (User, error) {
	displayName = strings.TrimSpace(displayName)
	email = strings.ToLower(strings.TrimSpace(email))
	if !validUser("valid", displayName, email) {
		return User{}, ErrValidation
	}
	var result User
	err := s.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		if err := lockAdministrator(ctx, tx, s.db.Dialect()); err != nil {
			return err
		}

		decision, err := s.authorizer.RequireInTx(ctx, tx, actorID, authorization.PermissionUsersWrite)
		if err != nil {
			return err
		}
		if decision.Scope == authorization.ScopeSelf && userID != actorID {
			return ErrDenied
		}
		if err := checkExpectedRevision(ctx, tx); err != nil {
			return err
		}
		var username string
		var disabled sql.NullTime
		if err := s.queryRowForUpdate(tx, ctx, `SELECT username, disabled_at FROM iam_accounts WHERE id = ?`, userID).Scan(&username, &disabled); err != nil {
			return err
		}
		if decision.Scope == authorization.ScopeSelf && enabled == disabled.Valid {
			return ErrDenied
		}
		if !enabled {
			if err := protectLastAdministrator(ctx, tx, userID); err != nil {
				return err
			}
		}
		var disabledAt any
		if !enabled {
			disabledAt = s.now().UTC()
		}
		generationAdvance := 0
		if !enabled && !disabled.Valid {
			generationAdvance = 1
		}
		update, err := tx.ExecContext(ctx, `UPDATE iam_accounts SET display_name = ?, email = ?, disabled_at = ?, session_generation = session_generation + ?, updated_at = ? WHERE id = ?`, displayName, email, disabledAt, generationAdvance, s.now().UTC(), userID)
		if err != nil {
			return err
		}
		count, err := update.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrNotFound
		}
		if generationAdvance == 1 {
			if _, err := tx.ExecContext(ctx, `UPDATE iam_sessions SET state = 'revoked', revoked_at = ? WHERE account_id = ? AND state = 'active'`, s.now().UTC(), userID); err != nil {
				return err
			}
		}
		result = User{ID: userID, Username: username, DisplayName: displayName, Email: email, Disabled: !enabled, RoleIDs: []string{}}
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM iam_accounts WHERE id=?`, userID).Scan(&result.Revision); err != nil {
			return err
		}
		projected := []User{result}
		if err := loadUserRoles(ctx, tx, projected); err != nil {
			return err
		}
		result = projected[0]
		return audit.Write(s.recorder, ctx, tx, audit.Operation{Action: "update", ResourceType: "iam_user", ResourceID: userID, ActorID: actorID, Code: "UpdateUser"})
	})
	return result, s.normalize(ctx, err)
}

func (s *Service) SetUserRoles(ctx context.Context, actorID, userID string, roleIDs []string) error {
	if duplicate(roleIDs) {
		return ErrConflict
	}
	return s.write(ctx, actorID, authorization.PermissionRolesAssign, func(ctx context.Context, tx database.Tx) error {
		var administratorRoleID string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM iam_roles WHERE role_key = ?`, systemAdministratorKey).Scan(&administratorRoleID); err != nil {
			return err
		}
		assignAdministrator := false
		for _, id := range roleIDs {
			if id == administratorRoleID {
				assignAdministrator = true
			}
		}
		if assignAdministrator {
			allowed, err := isAdministrator(ctx, tx, actorID)
			if err != nil {
				return err
			}
			if !allowed {
				return ErrDenied
			}
		} else if err := protectLastAdministrator(ctx, tx, userID); err != nil {
			return err
		}
		if err := validateIDs(ctx, tx, `SELECT COUNT(*) FROM iam_roles WHERE enabled = ? AND id IN (`+placeholders(len(roleIDs))+`)`, true, roleIDs); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM iam_account_roles WHERE account_id = ?`, userID); err != nil {
			return err
		}
		for _, roleID := range roleIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO iam_account_roles(account_id, role_id) VALUES (?, ?)`, userID, roleID); err != nil {
				return err
			}
		}
		return nil
	}, audit.Operation{Action: "update", ResourceType: "iam_user", ResourceID: userID, ActorID: actorID, Code: "SetUserRoles"})
}

func (s *Service) ResetPassword(ctx context.Context, actorID, userID, replacement string) error {
	if len(replacement) < account.MinimumPasswordLength || len(replacement) > 128 {
		return ErrValidation
	}
	if _, err := s.authorizer.Require(ctx, actorID, authorization.PermissionUsersResetPassword); err != nil {
		return err
	}
	release, acquired := s.passwordWork.TryAcquire()
	if !acquired {
		return ErrConflict
	}
	defer release()
	hash, err := account.HashPassword(replacement)
	if err != nil {
		return ErrInternal
	}
	release()
	return s.write(ctx, actorID, authorization.PermissionUsersResetPassword, func(ctx context.Context, tx database.Tx) error {
		if err := s.requireAccountForUpdate(tx, ctx, userID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE iam_accounts SET password_hash = ?, password_changed_at = ?, session_generation = session_generation + 1, updated_at = ? WHERE id = ?`, hash, s.now().UTC(), s.now().UTC(), userID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `UPDATE iam_sessions SET state = 'revoked', revoked_at = ? WHERE account_id = ? AND state = 'active'`, s.now().UTC(), userID)
		return err
	}, audit.Operation{Action: "update", ResourceType: "iam_user", ResourceID: userID, ActorID: actorID, Code: "ResetPassword"})
}

func (s *Service) ListRoles(ctx context.Context, actorID string) ([]Role, error) {
	var result []Role
	err := s.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		decision, err := s.authorizer.RequireInTx(ctx, tx, actorID, authorization.PermissionRolesRead)
		if err != nil {
			return err
		}
		if decision.Scope != authorization.ScopeAll {
			return ErrDenied
		}
		rows, err := tx.QueryContext(ctx, `SELECT id, role_key, name, data_scope, enabled, protected, revision FROM iam_roles ORDER BY role_key`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var value Role
			if err := rows.Scan(&value.ID, &value.Key, &value.Name, &value.Scope, &value.Enabled, &value.Protected, &value.Revision); err != nil {
				return err
			}
			result = append(result, value)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		return loadRoleGrants(ctx, tx, result)
	})
	return result, s.normalize(ctx, err)
}

func (s *Service) CreateRole(ctx context.Context, actorID, key, name string, scope authorization.Scope) (Role, error) {
	name = strings.TrimSpace(name)
	if !validStableKey(key) || name == "" || len(name) > 100 || scope != authorization.ScopeAll && scope != authorization.ScopeSelf {
		return Role{}, ErrValidation
	}
	value := Role{Revision: 1, ID: uuid.NewString(), Key: key, Name: name, Scope: scope, Enabled: true, PermissionCodes: []string{}, MenuIDs: []string{}}
	err := s.write(ctx, actorID, authorization.PermissionRolesWrite, func(ctx context.Context, tx database.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO iam_roles(id, role_key, name, data_scope, enabled, protected, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.Key, value.Name, value.Scope, true, false, s.now().UTC(), s.now().UTC())
		return err
	}, audit.Operation{Action: "create", ResourceType: "iam_role", ResourceID: value.ID, ActorID: actorID, Code: "CreateRole"})
	return value, err
}

func (s *Service) UpdateRole(ctx context.Context, actorID string, value Role) error {
	value.Name = strings.TrimSpace(value.Name)
	if value.ID == "" || !validStableKey(value.Key) || value.Name == "" || len(value.Name) > 100 || value.Scope != authorization.ScopeAll && value.Scope != authorization.ScopeSelf {
		return ErrValidation
	}
	return s.write(ctx, actorID, authorization.PermissionRolesWrite, func(ctx context.Context, tx database.Tx) error {
		var protected bool
		if err := s.queryRowForUpdate(tx, ctx, `SELECT protected FROM iam_roles WHERE id = ?`, value.ID).Scan(&protected); err != nil {
			return err
		}
		if protected {
			return ErrConflict
		}
		result, err := tx.ExecContext(ctx, `UPDATE iam_roles SET role_key = ?, name = ?, data_scope = ?, enabled = ?, updated_at = ? WHERE id = ?`, value.Key, value.Name, value.Scope, value.Enabled, s.now().UTC(), value.ID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrNotFound
		}
		return nil
	}, audit.Operation{Action: "update", ResourceType: "iam_role", ResourceID: value.ID, ActorID: actorID, Code: "UpdateRole"})
}

func (s *Service) SetRoleGrants(ctx context.Context, actorID, roleID string, permissionCodes, menuIDs []string) error {
	if duplicate(permissionCodes) || duplicate(menuIDs) {
		return ErrConflict
	}
	return s.write(ctx, actorID, authorization.PermissionRolesAssign, func(ctx context.Context, tx database.Tx) error {
		var protected bool
		if err := s.queryRowForUpdate(tx, ctx, `SELECT protected FROM iam_roles WHERE id = ?`, roleID).Scan(&protected); err != nil {
			return err
		}
		if protected {
			return ErrConflict
		}
		if err := validateIDs(ctx, tx, `SELECT COUNT(*) FROM iam_permissions WHERE code IN (`+placeholders(len(permissionCodes))+`)`, nil, permissionCodes); err != nil {
			return err
		}
		if err := validateIDs(ctx, tx, `SELECT COUNT(*) FROM iam_menus WHERE id IN (`+placeholders(len(menuIDs))+`)`, nil, menuIDs); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM iam_role_permissions WHERE role_id = ?`, roleID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM iam_role_menus WHERE role_id = ?`, roleID); err != nil {
			return err
		}
		for _, code := range permissionCodes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO iam_role_permissions(role_id, permission_code) VALUES (?, ?)`, roleID, code); err != nil {
				return err
			}
		}
		for _, menuID := range menuIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO iam_role_menus(role_id, menu_id) VALUES (?, ?)`, roleID, menuID); err != nil {
				return err
			}
		}
		return nil
	}, audit.Operation{Action: "update", ResourceType: "iam_role", ResourceID: roleID, ActorID: actorID, Code: "SetRoleGrants"})
}

func (s *Service) DeleteRole(ctx context.Context, actorID, roleID string) error {
	return s.write(ctx, actorID, authorization.PermissionRolesDelete, func(ctx context.Context, tx database.Tx) error {
		var protected bool
		if err := s.queryRowForUpdate(tx, ctx, `SELECT protected FROM iam_roles WHERE id = ?`, roleID).Scan(&protected); err != nil {
			return err
		}
		if protected {
			return ErrConflict
		}
		var references int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM iam_account_roles WHERE role_id = ?`, roleID).Scan(&references); err != nil {
			return err
		}
		if references > 0 {
			return ErrConflict
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM iam_roles WHERE id = ?`, roleID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrNotFound
		}
		return nil
	}, audit.Operation{Action: "delete", ResourceType: "iam_role", ResourceID: roleID, ActorID: actorID, Code: "DeleteRole"})
}

func (s *Service) ListPermissions(ctx context.Context, actorID string) ([]Permission, error) {
	var result []Permission
	err := s.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		decision, err := s.authorizer.RequireInTx(ctx, tx, actorID, authorization.PermissionPermissionsRead)
		if err != nil {
			return err
		}
		if decision.Scope != authorization.ScopeAll {
			return ErrDenied
		}
		rows, err := tx.QueryContext(ctx, `SELECT code, name FROM iam_permissions ORDER BY code`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var value Permission
			if err := rows.Scan(&value.Code, &value.Name); err != nil {
				return err
			}
			result = append(result, value)
		}
		return rows.Err()
	})
	return result, s.normalize(ctx, err)
}

func (s *Service) Manifest(ctx context.Context, actorID string) (authorization.Manifest, error) {
	return s.authorizer.Manifest(ctx, actorID)
}

func (s *Service) write(ctx context.Context, actorID, permission string, operation func(context.Context, database.Tx) error, facts ...audit.Operation) error {
	fact := audit.Operation{Action: "update", ResourceType: "iam", ResourceID: "command", ActorID: actorID, Code: permission}
	if len(facts) > 0 {
		fact = facts[0]
	}
	err := s.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		if err := lockAdministrator(ctx, tx, s.db.Dialect()); err != nil {
			return err
		}

		decision, err := s.authorizer.RequireInTx(ctx, tx, actorID, permission)
		if err != nil {
			return err
		}
		if decision.Scope != authorization.ScopeAll {
			return ErrDenied
		}
		if s.authorizationProbe != nil {
			s.authorizationProbe(permission)
		}
		if err := checkExpectedRevision(ctx, tx); err != nil {
			return err
		}
		if err := operation(ctx, tx); err != nil {
			return err
		}
		return audit.Write(s.recorder, ctx, tx, fact)
	})
	audit.RecordFailure(s.recorder, ctx, s.db, fact, err)
	return s.normalize(ctx, err)
}

func (s *Service) normalize(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	for _, stable := range []error{context.Canceled, context.DeadlineExceeded, ErrDenied, ErrNotFound, ErrValidation, ErrConflict} {
		if errors.Is(err, stable) {
			return stable
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if errors.Is(err, account.ErrConflict) || constraintConflict(s.db.Dialect(), err) {
		return ErrConflict
	}
	return ErrInternal
}

func constraintConflict(dialect database.Dialect, err error) bool {
	if dialect == database.DialectPostgres {
		var state interface{ SQLState() string }
		return errors.As(err, &state) && (state.SQLState() == "23505" || state.SQLState() == "23503")
	}
	if dialect == database.DialectSQLite {
		var coded interface{ Code() int }
		return errors.As(err, &coded) && (coded.Code() == 1555 || coded.Code() == 2067 || coded.Code() == 787)
	}
	return false
}

func userScopeWhere(scope authorization.Scope, actorID, search string) (string, []any) {
	clauses, args := []string{"1 = 1"}, []any{}
	if scope == authorization.ScopeSelf {
		clauses = append(clauses, "a.id = ?")
		args = append(args, actorID)
	}
	if search != "" {
		clauses = append(clauses, "(lower(a.username) LIKE ? OR lower(a.display_name) LIKE ? OR lower(a.email) LIKE ?)")
		value := "%" + search + "%"
		args = append(args, value, value, value)
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func validUser(username, displayName, email string) bool {
	parsed, err := mail.ParseAddress(email)
	return len(username) >= 3 && len(username) <= 64 && displayName != "" && len(displayName) <= 80 && err == nil && parsed.Address == email && len(email) <= 254
}

func validStableKey(value string) bool {
	return len(value) >= 3 && len(value) <= 64 && stableKeyPattern.MatchString(value)
}

func duplicate(values []string) bool {
	seen := map[string]struct{}{}
	for _, value := range values {
		if value == "" {
			return true
		}
		if _, ok := seen[value]; ok {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}
func placeholders(count int) string {
	if count == 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}
func validateIDs(ctx context.Context, tx database.Tx, query string, prefix any, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, 0, len(ids)+1)
	if prefix != nil {
		args = append(args, prefix)
	}
	for _, id := range ids {
		args = append(args, id)
	}
	var count int
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return err
	}
	if count != len(ids) {
		return ErrConflict
	}
	return nil
}
func (s *Service) requireAccountForUpdate(tx database.Tx, ctx context.Context, id string) error {
	var found string
	if err := s.queryRowForUpdate(tx, ctx, `SELECT id FROM iam_accounts WHERE id = ?`, id).Scan(&found); err != nil {
		return err
	}
	return nil
}

func (s *Service) queryRowForUpdate(tx database.Tx, ctx context.Context, query string, args ...any) *sql.Row {
	if s.db.Dialect() == database.DialectPostgres {
		query += ` FOR UPDATE`
	}
	return tx.QueryRowContext(ctx, query, args...)
}

func loadUserRoles(ctx context.Context, tx database.Tx, users []User) error {
	if len(users) == 0 {
		return nil
	}
	index, arguments := make(map[string]int, len(users)), make([]any, 0, len(users))
	for position := range users {
		users[position].RoleIDs = []string{}
		index[users[position].ID] = position
		arguments = append(arguments, users[position].ID)
	}
	rows, err := tx.QueryContext(ctx, `SELECT account_id, role_id FROM iam_account_roles WHERE account_id IN (`+placeholders(len(users))+`) ORDER BY account_id, role_id`, arguments...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, roleID string
		if err := rows.Scan(&accountID, &roleID); err != nil {
			return err
		}
		if position, ok := index[accountID]; ok {
			users[position].RoleIDs = append(users[position].RoleIDs, roleID)
		}
	}
	return rows.Err()
}

func loadRoleGrants(ctx context.Context, tx database.Tx, roles []Role) error {
	if len(roles) == 0 {
		return nil
	}
	index := make(map[string]int, len(roles))
	arguments := make([]any, 0, len(roles))
	for position := range roles {
		roles[position].PermissionCodes = []string{}
		roles[position].MenuIDs = []string{}
		index[roles[position].ID] = position
		arguments = append(arguments, roles[position].ID)
	}
	permissionRows, err := tx.QueryContext(ctx, `SELECT role_id, permission_code FROM iam_role_permissions WHERE role_id IN (`+placeholders(len(roles))+`) ORDER BY role_id, permission_code`, arguments...)
	if err != nil {
		return err
	}
	for permissionRows.Next() {
		var roleID, code string
		if err := permissionRows.Scan(&roleID, &code); err != nil {
			_ = permissionRows.Close()
			return err
		}
		if position, ok := index[roleID]; ok {
			roles[position].PermissionCodes = append(roles[position].PermissionCodes, code)
		}
	}
	if err := permissionRows.Err(); err != nil {
		_ = permissionRows.Close()
		return err
	}
	if err := permissionRows.Close(); err != nil {
		return err
	}
	menuRows, err := tx.QueryContext(ctx, `SELECT role_id, menu_id FROM iam_role_menus WHERE role_id IN (`+placeholders(len(roles))+`) ORDER BY role_id, menu_id`, arguments...)
	if err != nil {
		return err
	}
	defer menuRows.Close()
	for menuRows.Next() {
		var roleID, menuID string
		if err := menuRows.Scan(&roleID, &menuID); err != nil {
			return err
		}
		if position, ok := index[roleID]; ok {
			roles[position].MenuIDs = append(roles[position].MenuIDs, menuID)
		}
	}
	return menuRows.Err()
}

func (r Role) String() string { return fmt.Sprintf("Role{%s,%s}", r.ID, r.Key) }
