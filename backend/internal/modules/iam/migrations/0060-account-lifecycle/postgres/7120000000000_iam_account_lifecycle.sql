-- +goose Up
ALTER TABLE iam_accounts ADD COLUMN lifecycle_state TEXT NOT NULL DEFAULT 'active'
  CHECK (lifecycle_state IN ('active','disabled','deletion-pending','deleted'));
ALTER TABLE iam_accounts ADD COLUMN deleted_at TIMESTAMPTZ;
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
  audit_ref TEXT NOT NULL UNIQUE CHECK (audit_ref LIKE 'deleted-account:%'),
  event_id TEXT NOT NULL UNIQUE,
  business_key TEXT NOT NULL UNIQUE CHECK (business_key LIKE 'account-deletion:%'),
  failure_code TEXT,
  created_at TIMESTAMPTZ NOT NULL,
  claimed_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL,
  CHECK ((strategy = 'transfer' AND transfer_target_id IS NOT NULL AND transfer_target_id <> account_id)
    OR (strategy = 'purge' AND transfer_target_id IS NULL)),
  CHECK ((status = 'claimed' AND claimed_at IS NOT NULL) OR status <> 'claimed'),
  CHECK ((status = 'completed' AND completed_at IS NOT NULL) OR status <> 'completed')
);
CREATE UNIQUE INDEX iam_account_deletions_active_account_unique ON iam_account_deletions(account_id)
  WHERE status IN ('queued','claimed','failed');
CREATE INDEX iam_account_deletions_status_idx ON iam_account_deletions(status, updated_at, id);

-- +goose StatementBegin
CREATE FUNCTION iam_guard_account_lifecycle() RETURNS trigger AS $$
BEGIN
  IF OLD.lifecycle_state = 'deletion-pending' AND NEW.disabled_at IS NULL THEN
    RAISE EXCEPTION 'account deletion is pending' USING ERRCODE = '23514';
  END IF;
  IF OLD.lifecycle_state = 'deleted' AND (
    NEW.lifecycle_state <> 'deleted' OR NEW.disabled_at IS NULL OR NEW.deleted_at IS NULL OR
    NEW.username <> OLD.username OR NEW.display_name <> OLD.display_name OR NEW.email <> OLD.email OR
    NEW.password_hash <> OLD.password_hash
  ) THEN
    RAISE EXCEPTION 'deleted account is immutable' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER iam_accounts_lifecycle_guard BEFORE UPDATE ON iam_accounts
FOR EACH ROW EXECUTE FUNCTION iam_guard_account_lifecycle();

-- +goose StatementBegin
CREATE FUNCTION iam_guard_deleted_account_role() RETURNS trigger AS $$
BEGIN
  IF (SELECT lifecycle_state FROM iam_accounts WHERE id = NEW.account_id) IN ('deletion-pending','deleted') THEN
    RAISE EXCEPTION 'deleted account cannot gain a role' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER iam_deleted_account_role_guard BEFORE INSERT ON iam_account_roles
FOR EACH ROW EXECUTE FUNCTION iam_guard_deleted_account_role();

-- +goose StatementBegin
CREATE FUNCTION iam_sync_disabled_state() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.lifecycle_state IN ('active','disabled') THEN
 NEW.lifecycle_state := CASE WHEN NEW.disabled_at IS NULL THEN 'active' ELSE 'disabled' END;
 END IF;
 RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TRIGGER iam_accounts_disabled_state BEFORE UPDATE OF disabled_at ON iam_accounts FOR EACH ROW EXECUTE FUNCTION iam_sync_disabled_state();
