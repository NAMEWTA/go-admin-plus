-- +goose Up
ALTER TABLE iam_sessions ADD COLUMN family_id TEXT CHECK (family_id IS NULL OR length(family_id) = 43);
ALTER TABLE iam_sessions ADD COLUMN renewed_at TIMESTAMP;
ALTER TABLE iam_sessions ADD COLUMN renew_after_at TIMESTAMP;
UPDATE iam_sessions SET state = 'revoked', revoked_at = CURRENT_TIMESTAMP WHERE state = 'active';

CREATE TABLE iam_login_buckets (
  kind TEXT NOT NULL CHECK (kind IN ('account','source')),
  key_hash TEXT NOT NULL CHECK (length(key_hash) = 64),
  window_started_at TIMESTAMP NOT NULL,
  attempt_count INTEGER NOT NULL CHECK (attempt_count >= 0),
  blocked_until TIMESTAMP,
  updated_at TIMESTAMP NOT NULL,
  PRIMARY KEY (kind, key_hash)
);
CREATE INDEX iam_login_buckets_blocked_idx ON iam_login_buckets(blocked_until);
