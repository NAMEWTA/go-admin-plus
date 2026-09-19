-- +goose Up
ALTER TABLE iam_accounts ADD COLUMN lifecycle_state TEXT NOT NULL DEFAULT 'active'
  CHECK (lifecycle_state IN ('active','disabled','deletion-pending','deleted'));
ALTER TABLE iam_accounts ADD COLUMN deleted_at TIMESTAMP;
ALTER TABLE iam_accounts ADD COLUMN audit_ref TEXT;
UPDATE iam_accounts SET lifecycle_state = 'disabled' WHERE disabled_at IS NOT NULL;
CREATE UNIQUE INDEX iam_accounts_audit_ref_unique ON iam_accounts(audit_ref) WHERE audit_ref IS NOT NULL;
CREATE INDEX iam_accounts_lifecycle_idx ON iam_accounts(lifecycle_state, disabled_at, id);

CREATE TABLE iam_account_deletions (
  id TEXT PRIMARY KEY CHECK (length(id) = 36),
  account_id TEXT NOT NULL REFERENCES iam_accounts(id) ON DELETE RESTRICT,
  strategy TEXT NOT NULL CHECK (strategy IN ('transfer','purge')),
  transfer_target_id TEXT REFERENCES iam_accounts(id) ON DELETE RESTRICT,
  status TEXT NOT NULL CHECK (status IN ('queued','claimed','completed','failed','canceled')),
  audit_ref TEXT NOT NULL UNIQUE CHECK (substr(audit_ref, 1, 16) = 'deleted-account:'),
  event_id TEXT NOT NULL UNIQUE,
  business_key TEXT NOT NULL UNIQUE CHECK (substr(business_key, 1, 17) = 'account-deletion:'),
  failure_code TEXT,
  created_at TIMESTAMP NOT NULL,
  claimed_at TIMESTAMP,
  completed_at TIMESTAMP,
  updated_at TIMESTAMP NOT NULL,
  CHECK ((strategy = 'transfer' AND transfer_target_id IS NOT NULL AND transfer_target_id <> account_id)
    OR (strategy = 'purge' AND transfer_target_id IS NULL)),
  CHECK ((status = 'claimed' AND claimed_at IS NOT NULL) OR status <> 'claimed'),
  CHECK ((status = 'completed' AND completed_at IS NOT NULL) OR status <> 'completed')
);
CREATE UNIQUE INDEX iam_account_deletions_active_account_unique ON iam_account_deletions(account_id)
  WHERE status IN ('queued','claimed','failed');
CREATE INDEX iam_account_deletions_status_idx ON iam_account_deletions(status, updated_at, id);

-- +goose StatementBegin
CREATE TRIGGER iam_accounts_pending_cannot_reenable
BEFORE UPDATE ON iam_accounts
WHEN OLD.lifecycle_state = 'deletion-pending' AND NEW.disabled_at IS NULL
BEGIN
  SELECT RAISE(ABORT, 'account deletion is pending');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_accounts_deleted_is_terminal
BEFORE UPDATE ON iam_accounts
WHEN OLD.lifecycle_state = 'deleted' AND (
  NEW.lifecycle_state <> 'deleted' OR NEW.disabled_at IS NULL OR NEW.deleted_at IS NULL OR
  NEW.username <> OLD.username OR NEW.display_name <> OLD.display_name OR NEW.email <> OLD.email OR
  NEW.password_hash <> OLD.password_hash
)
BEGIN
  SELECT RAISE(ABORT, 'deleted account is immutable');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_deleted_account_cannot_gain_role
BEFORE INSERT ON iam_account_roles
WHEN (SELECT lifecycle_state FROM iam_accounts WHERE id = NEW.account_id) IN ('deletion-pending','deleted')
BEGIN
  SELECT RAISE(ABORT, 'deleted account cannot gain a role');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER iam_accounts_disabled_state AFTER UPDATE OF disabled_at ON iam_accounts
WHEN NEW.lifecycle_state IN ('active','disabled')
BEGIN UPDATE iam_accounts SET lifecycle_state = CASE WHEN NEW.disabled_at IS NULL THEN 'active' ELSE 'disabled' END WHERE id=NEW.id; END;
-- +goose StatementEnd
