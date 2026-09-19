-- +goose Up
CREATE TABLE iam_accounts (
  id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 16 AND 64),
  username TEXT NOT NULL UNIQUE CHECK (length(username) BETWEEN 3 AND 64),
  display_name TEXT NOT NULL CHECK (length(display_name) BETWEEN 1 AND 80),
  email TEXT NOT NULL CHECK (length(email) <= 254), avatar_ref TEXT,
  password_hash TEXT NOT NULL CHECK (password_hash LIKE '$argon2id$%'), password_changed_at TIMESTAMPTZ NOT NULL,
  session_generation BIGINT NOT NULL DEFAULT 0 CHECK (session_generation >= 0),
  created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL, disabled_at TIMESTAMPTZ,
  CONSTRAINT iam_accounts_username_normalized CHECK (username = lower(btrim(username)))
);
CREATE TABLE iam_sessions (
  id TEXT PRIMARY KEY CHECK (length(id) = 43), account_id TEXT NOT NULL REFERENCES iam_accounts(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64), generation BIGINT NOT NULL CHECK (generation >= 0),
  csrf_hash TEXT NOT NULL CHECK (length(csrf_hash) = 64),
  state TEXT NOT NULL CHECK (state IN ('active','rotated','revoked','expired')),
  created_at TIMESTAMPTZ NOT NULL, last_seen_at TIMESTAMPTZ NOT NULL,
  idle_expires_at TIMESTAMPTZ NOT NULL, absolute_expires_at TIMESTAMPTZ NOT NULL,
  rotate_at TIMESTAMPTZ NOT NULL, revoked_at TIMESTAMPTZ, replaced_by TEXT,
  CHECK (idle_expires_at <= absolute_expires_at AND rotate_at <= absolute_expires_at)
);
CREATE INDEX iam_sessions_account_state_idx ON iam_sessions(account_id, state);
CREATE INDEX iam_sessions_expiry_idx ON iam_sessions(state, idle_expires_at, absolute_expires_at);
