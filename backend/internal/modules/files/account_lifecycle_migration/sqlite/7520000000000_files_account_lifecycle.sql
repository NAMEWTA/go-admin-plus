-- +goose Up
CREATE TABLE files_account_lifecycle_events (
  event_id TEXT PRIMARY KEY,
  business_key TEXT NOT NULL UNIQUE CHECK (substr(business_key, 1, 17) = 'account-deletion:'),
  payload BLOB NOT NULL,
  occurred_at TIMESTAMP NOT NULL,
  state TEXT NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','claimed','completed','failed','canceled')),
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  claim_token TEXT,
  claim_expires_at TIMESTAMP,
  next_attempt_at TIMESTAMP,
  claimed_at TIMESTAMP,
  completed_at TIMESTAMP,
  last_error_code TEXT,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX files_account_lifecycle_events_work_idx ON files_account_lifecycle_events(state, occurred_at, event_id);
