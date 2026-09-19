-- +goose Up
CREATE TABLE files_objects (
  id TEXT PRIMARY KEY CHECK (length(id) = 36),
  owner_account_id TEXT NOT NULL CHECK (length(owner_account_id) BETWEEN 1 AND 64),
  original_name TEXT NOT NULL CHECK (length(btrim(original_name)) BETWEEN 1 AND 255),
  name_key TEXT NOT NULL CHECK (length(name_key) BETWEEN 1 AND 255),
  media_type TEXT NOT NULL CHECK (length(media_type) BETWEEN 1 AND 100),
  size_bytes BIGINT NOT NULL CHECK (size_bytes BETWEEN 0 AND 1073741824),
  sha256 TEXT NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
  provider_id TEXT NOT NULL DEFAULT 'local' CHECK (provider_id IN ('local','s3')),
  storage_key TEXT NOT NULL UNIQUE CHECK (storage_key ~ '^object-[a-f0-9]{32}$'),
  temporary_key TEXT CHECK (temporary_key IS NULL OR temporary_key ~ '^stage-[a-f0-9]{32}$'),
  state TEXT NOT NULL CHECK (state IN ('pending', 'ready', 'deleting')),
  revision BIGINT NOT NULL DEFAULT 1 CHECK (revision >= 1),
  claim_token TEXT CHECK (claim_token IS NULL OR length(claim_token) = 36),
  claim_expires_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  CHECK ((state = 'pending' AND temporary_key IS NOT NULL) OR (state IN ('ready', 'deleting') AND temporary_key IS NULL)),
  CHECK ((claim_token IS NULL AND claim_expires_at IS NULL) OR (claim_token IS NOT NULL AND claim_expires_at IS NOT NULL))
);
CREATE INDEX files_objects_owner_ready_idx ON files_objects(owner_account_id, state, updated_at DESC, id);
CREATE INDEX files_objects_name_idx ON files_objects(name_key COLLATE "C", id);
CREATE INDEX files_objects_recovery_idx ON files_objects(state, claim_expires_at, id);
