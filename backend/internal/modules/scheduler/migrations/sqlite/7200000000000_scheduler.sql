-- +goose Up
CREATE TABLE scheduler_definitions (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    name_key TEXT NOT NULL,
    task_type TEXT NOT NULL,
    schedule_json BLOB NOT NULL,
    parameters_json BLOB NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
    next_run_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    deleted_at TIMESTAMP,
    CHECK ((enabled = 1 AND next_run_at IS NOT NULL AND deleted_at IS NULL) OR (enabled = 0 AND next_run_at IS NULL))
);
CREATE UNIQUE INDEX scheduler_definitions_active_name_key_uq ON scheduler_definitions(name_key COLLATE BINARY) WHERE deleted_at IS NULL;
CREATE INDEX scheduler_definitions_due_idx ON scheduler_definitions(enabled, next_run_at, id) WHERE deleted_at IS NULL;
CREATE TABLE scheduler_executions (
    id TEXT PRIMARY KEY,
    definition_id TEXT NOT NULL REFERENCES scheduler_definitions(id),
    definition_revision INTEGER NOT NULL CHECK (definition_revision >= 1),
    task_type TEXT NOT NULL,
    scheduled_for TIMESTAMP NOT NULL,
    started_at TIMESTAMP NOT NULL,
    finished_at TIMESTAMP NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('succeeded', 'failed')),
    error_code TEXT,
    executor_owner TEXT NOT NULL,
    CHECK (scheduled_for <= started_at),
    CHECK (started_at <= finished_at),
    CHECK ((status = 'succeeded' AND error_code IS NULL) OR (status = 'failed' AND error_code IS NOT NULL)),
    UNIQUE(definition_id, scheduled_for)
);
CREATE INDEX scheduler_executions_history_idx ON scheduler_executions(started_at DESC, id DESC);
CREATE INDEX scheduler_executions_definition_idx ON scheduler_executions(definition_id, started_at DESC, id DESC);

-- 持久认领与执行历史分离；每次重试保留同一个执行 ID，供 handler 做幂等处理。
CREATE TABLE scheduler_runs (
 id TEXT PRIMARY KEY,
 definition_id TEXT NOT NULL REFERENCES scheduler_definitions(id),
 definition_revision BIGINT NOT NULL,
 task_type TEXT NOT NULL,
 parameters_json BLOB NOT NULL,
 scheduled_for TIMESTAMP NOT NULL,
 state TEXT NOT NULL CHECK (state IN ('queued','running','succeeded','failed')),
 claim_token TEXT,
 lease_until TIMESTAMP,
 attempts INTEGER NOT NULL DEFAULT 0,
 created_at TIMESTAMP NOT NULL,
 UNIQUE(definition_id, scheduled_for)
);
CREATE UNIQUE INDEX scheduler_runs_active_definition ON scheduler_runs(definition_id) WHERE state IN ('queued','running');
CREATE INDEX scheduler_runs_claim ON scheduler_runs(state, lease_until, scheduled_for);
