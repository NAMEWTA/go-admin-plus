-- +goose Up
CREATE TABLE iam_bootstrap_state (
  marker INTEGER PRIMARY KEY CHECK (marker = 1),
  account_id TEXT NOT NULL CHECK (length(account_id) BETWEEN 16 AND 64),
  initialized_at TIMESTAMP NOT NULL
);
CREATE TABLE iam_account_recovery_blocks (
  account_id TEXT PRIMARY KEY REFERENCES iam_accounts(id) ON DELETE RESTRICT,
  blocked_at TIMESTAMP NOT NULL
);
