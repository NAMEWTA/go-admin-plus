-- +goose Up
ALTER TABLE iam_sessions ADD COLUMN csrf_token TEXT CHECK (csrf_token IS NULL OR length(csrf_token) = 43);
UPDATE iam_sessions SET state = 'revoked', revoked_at = CURRENT_TIMESTAMP WHERE state = 'active';
