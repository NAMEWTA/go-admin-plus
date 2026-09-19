-- +goose Up
CREATE TABLE files_capacity_counters (
  scope_kind TEXT NOT NULL CHECK (scope_kind IN ('global', 'account')),
  scope_id TEXT NOT NULL CHECK (length(scope_id) BETWEEN 1 AND 64),
  reserved_bytes INTEGER NOT NULL CHECK (reserved_bytes >= 0),
  reserved_objects INTEGER NOT NULL CHECK (reserved_objects >= 0),
  PRIMARY KEY (scope_kind, scope_id),
  CHECK ((scope_kind = 'global' AND scope_id = 'global') OR scope_kind = 'account')
);

INSERT INTO files_capacity_counters(scope_kind, scope_id, reserved_bytes, reserved_objects)
SELECT 'global', 'global', COALESCE(SUM(size_bytes), 0), COUNT(*) FROM files_objects;

INSERT INTO files_capacity_counters(scope_kind, scope_id, reserved_bytes, reserved_objects)
SELECT 'account', owner_account_id, COALESCE(SUM(size_bytes), 0), COUNT(*)
FROM files_objects GROUP BY owner_account_id;

CREATE TABLE files_capacity_reservations (
  id TEXT PRIMARY KEY CHECK (length(id) = 36),
  owner_account_id TEXT NOT NULL CHECK (length(owner_account_id) BETWEEN 1 AND 64),
  reserved_bytes INTEGER NOT NULL CHECK (reserved_bytes > 0),
  created_at TIMESTAMP NOT NULL,
  expires_at TIMESTAMP NOT NULL CHECK (expires_at > created_at)
);
CREATE INDEX files_capacity_reservations_expiry_idx ON files_capacity_reservations(expires_at, id);
