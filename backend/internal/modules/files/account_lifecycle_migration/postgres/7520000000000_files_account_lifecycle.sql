-- +goose Up
CREATE TABLE files_account_lifecycle_events (
  event_id TEXT PRIMARY KEY,
  business_key TEXT NOT NULL UNIQUE CHECK (business_key LIKE 'account-deletion:%'),
  payload BYTEA NOT NULL,
  occurred_at TIMESTAMPTZ NOT NULL,
  state TEXT NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','claimed','completed','failed','canceled')),
  attempts BIGINT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  claim_token TEXT,
  claim_expires_at TIMESTAMPTZ,
  next_attempt_at TIMESTAMPTZ,
  claimed_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ,
  last_error_code TEXT,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX files_account_lifecycle_events_work_idx ON files_account_lifecycle_events(state, occurred_at, event_id);
