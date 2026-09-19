-- +goose Up
ALTER TABLE iam_accounts ADD COLUMN revision BIGINT NOT NULL DEFAULT 1;
CREATE TABLE iam_permissions (
  code TEXT PRIMARY KEY CHECK (length(code) BETWEEN 3 AND 100),
  name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  protected INTEGER NOT NULL DEFAULT 1 CHECK (protected IN (0, 1))
);
CREATE TABLE iam_roles (
  revision BIGINT NOT NULL DEFAULT 1,
  id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 16 AND 64),
  role_key TEXT NOT NULL COLLATE NOCASE UNIQUE CHECK (length(role_key) BETWEEN 3 AND 64 AND role_key NOT GLOB '*[^a-z0-9_-]*' AND substr(role_key, 1, 1) GLOB '[a-z0-9]'),
  name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  data_scope TEXT NOT NULL CHECK (data_scope IN ('all', 'self')),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  protected INTEGER NOT NULL DEFAULT 0 CHECK (protected IN (0, 1)),
  created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL
);
CREATE TABLE iam_account_roles (
  account_id TEXT NOT NULL REFERENCES iam_accounts(id) ON DELETE CASCADE,
  role_id TEXT NOT NULL REFERENCES iam_roles(id) ON DELETE RESTRICT,
  PRIMARY KEY (account_id, role_id)
);
CREATE TABLE iam_role_permissions (
  role_id TEXT NOT NULL REFERENCES iam_roles(id) ON DELETE CASCADE,
  permission_code TEXT NOT NULL REFERENCES iam_permissions(code) ON DELETE RESTRICT,
  PRIMARY KEY (role_id, permission_code)
);
CREATE TABLE iam_menus (
  id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 16 AND 64),
  menu_key TEXT NOT NULL COLLATE NOCASE UNIQUE CHECK (length(menu_key) BETWEEN 3 AND 64 AND menu_key NOT GLOB '*[^a-z0-9_-]*' AND substr(menu_key, 1, 1) GLOB '[a-z0-9]'),
  label TEXT NOT NULL CHECK (length(label) BETWEEN 1 AND 80),
  path TEXT NOT NULL DEFAULT '',
  permission_code TEXT REFERENCES iam_permissions(code) ON DELETE RESTRICT,
  parent_id TEXT REFERENCES iam_menus(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL DEFAULT 'page' CHECK (kind IN ('directory','page','button')) CHECK (kind = 'directory' OR permission_code IS NOT NULL),
  route_key TEXT NOT NULL DEFAULT '',
  icon TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  hidden INTEGER NOT NULL DEFAULT 0 CHECK (hidden IN (0,1)),
  revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
  sort_order INTEGER NOT NULL DEFAULT 0,
  protected INTEGER NOT NULL DEFAULT 0 CHECK (protected IN (0, 1)),
  created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL
);
CREATE UNIQUE INDEX iam_menus_page_path ON iam_menus(path) WHERE kind = 'page';
CREATE INDEX iam_menus_parent ON iam_menus(parent_id, sort_order);
CREATE TABLE iam_registered_pages (route_key TEXT PRIMARY KEY, path TEXT NOT NULL UNIQUE, permission_code TEXT NOT NULL REFERENCES iam_permissions(code));
CREATE TABLE iam_role_menus (
  role_id TEXT NOT NULL REFERENCES iam_roles(id) ON DELETE CASCADE,
  menu_id TEXT NOT NULL REFERENCES iam_menus(id) ON DELETE RESTRICT,
  PRIMARY KEY (role_id, menu_id)
);
CREATE INDEX iam_account_roles_role_idx ON iam_account_roles(role_id);
CREATE INDEX iam_role_permissions_permission_idx ON iam_role_permissions(permission_code);
CREATE INDEX iam_role_menus_menu_idx ON iam_role_menus(menu_id);

INSERT INTO iam_permissions(code, name) VALUES
 ('iam.users.read', 'Read users'), ('iam.users.write', 'Manage users'),
 ('iam.users.delete', 'Delete users'), ('iam.users.reset-password', 'Reset user passwords'),
 ('iam.roles.read', 'Read roles'), ('iam.roles.write', 'Manage roles'),
 ('iam.roles.delete', 'Delete roles'), ('iam.roles.assign', 'Assign authorization'),
 ('iam.menus.read', 'Read menus'), ('iam.menus.write', 'Manage menus'),
 ('iam.menus.delete', 'Delete menus'), ('iam.permissions.read', 'Read permission codes'),
 ('iam.manifest.read', 'Read capability manifest');
INSERT INTO iam_roles(id, role_key, name, data_scope, enabled, protected, created_at, updated_at)
 VALUES ('role-system-admin', 'system-admin', '系统管理员', 'all', 1, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
INSERT INTO iam_role_permissions(role_id, permission_code)
 SELECT 'role-system-admin', code FROM iam_permissions;
INSERT INTO iam_menus(id, menu_key, label, path, permission_code, sort_order, protected, created_at, updated_at) VALUES
 ('menu-iam-users-01', 'iam-users', '用户管理', '/iam/users', 'iam.users.read', 10, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
 ('menu-iam-roles-01', 'iam-roles', '角色管理', '/iam/roles', 'iam.roles.read', 20, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
 ('menu-iam-menus-01', 'iam-menus', '菜单管理', '/iam/menus', 'iam.menus.read', 30, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
INSERT INTO iam_role_menus(role_id, menu_id) SELECT 'role-system-admin', id FROM iam_menus;

INSERT INTO iam_registered_pages(route_key,path,permission_code) SELECT menu_key,path,permission_code FROM iam_menus;
UPDATE iam_menus SET route_key = menu_key;

-- 菜单与权限展示投影的版本由数据库维护，Redis 不参与权限判定。
CREATE TABLE iam_projection_revision (singleton INTEGER PRIMARY KEY CHECK (singleton = 1), revision BIGINT NOT NULL);
INSERT INTO iam_projection_revision(singleton, revision) VALUES (1, 1);

-- +goose StatementBegin
CREATE TRIGGER iam_roles_projection_insert AFTER INSERT ON iam_roles
BEGIN UPDATE iam_projection_revision SET revision = revision + 1 WHERE singleton = 1; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_roles_projection_update AFTER UPDATE ON iam_roles
BEGIN UPDATE iam_projection_revision SET revision = revision + 1 WHERE singleton = 1; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_roles_projection_delete AFTER DELETE ON iam_roles
BEGIN UPDATE iam_projection_revision SET revision = revision + 1 WHERE singleton = 1; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_permissions_projection_insert AFTER INSERT ON iam_permissions
BEGIN UPDATE iam_projection_revision SET revision = revision + 1 WHERE singleton = 1; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_permissions_projection_update AFTER UPDATE ON iam_permissions
BEGIN UPDATE iam_projection_revision SET revision = revision + 1 WHERE singleton = 1; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_permissions_projection_delete AFTER DELETE ON iam_permissions
BEGIN UPDATE iam_projection_revision SET revision = revision + 1 WHERE singleton = 1; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_role_permissions_projection_insert AFTER INSERT ON iam_role_permissions
BEGIN UPDATE iam_projection_revision SET revision = revision + 1 WHERE singleton = 1; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_role_permissions_projection_update AFTER UPDATE ON iam_role_permissions
BEGIN UPDATE iam_projection_revision SET revision = revision + 1 WHERE singleton = 1; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_role_permissions_projection_delete AFTER DELETE ON iam_role_permissions
BEGIN UPDATE iam_projection_revision SET revision = revision + 1 WHERE singleton = 1; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_menus_projection_insert AFTER INSERT ON iam_menus
BEGIN UPDATE iam_projection_revision SET revision = revision + 1 WHERE singleton = 1; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_menus_projection_update AFTER UPDATE ON iam_menus
BEGIN UPDATE iam_projection_revision SET revision = revision + 1 WHERE singleton = 1; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_menus_projection_delete AFTER DELETE ON iam_menus
BEGIN UPDATE iam_projection_revision SET revision = revision + 1 WHERE singleton = 1; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_role_menus_projection_insert AFTER INSERT ON iam_role_menus
BEGIN UPDATE iam_projection_revision SET revision = revision + 1 WHERE singleton = 1; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_role_menus_projection_update AFTER UPDATE ON iam_role_menus
BEGIN UPDATE iam_projection_revision SET revision = revision + 1 WHERE singleton = 1; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_role_menus_projection_delete AFTER DELETE ON iam_role_menus
BEGIN UPDATE iam_projection_revision SET revision = revision + 1 WHERE singleton = 1; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_account_roles_projection_insert AFTER INSERT ON iam_account_roles
BEGIN UPDATE iam_projection_revision SET revision = revision + 1 WHERE singleton = 1; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_account_roles_projection_update AFTER UPDATE ON iam_account_roles
BEGIN UPDATE iam_projection_revision SET revision = revision + 1 WHERE singleton = 1; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_account_roles_projection_delete AFTER DELETE ON iam_account_roles
BEGIN UPDATE iam_projection_revision SET revision = revision + 1 WHERE singleton = 1; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_accounts_revision AFTER UPDATE ON iam_accounts WHEN NEW.revision = OLD.revision
BEGIN UPDATE iam_accounts SET revision=revision+1 WHERE id=NEW.id; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_roles_revision AFTER UPDATE ON iam_roles WHEN NEW.revision = OLD.revision
BEGIN UPDATE iam_roles SET revision=revision+1 WHERE id=NEW.id; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_account_roles_revision_insert AFTER INSERT ON iam_account_roles
BEGIN UPDATE iam_accounts SET revision=revision+1 WHERE id=NEW.account_id; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_account_roles_revision_delete AFTER DELETE ON iam_account_roles
BEGIN UPDATE iam_accounts SET revision=revision+1 WHERE id=OLD.account_id; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_role_permissions_revision_insert AFTER INSERT ON iam_role_permissions
BEGIN UPDATE iam_roles SET revision=revision+1 WHERE id=NEW.role_id; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_role_permissions_revision_delete AFTER DELETE ON iam_role_permissions
BEGIN UPDATE iam_roles SET revision=revision+1 WHERE id=OLD.role_id; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_role_menus_revision_insert AFTER INSERT ON iam_role_menus
BEGIN UPDATE iam_roles SET revision=revision+1 WHERE id=NEW.role_id; END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_role_menus_revision_delete AFTER DELETE ON iam_role_menus
BEGIN UPDATE iam_roles SET revision=revision+1 WHERE id=OLD.role_id; END;
-- +goose StatementEnd
