-- +goose Up
CREATE TABLE scheduler_definitions (
    id uuid PRIMARY KEY,
    name text NOT NULL,
    name_key text COLLATE "C" NOT NULL,
    task_type text COLLATE "C" NOT NULL,
    schedule_json bytea NOT NULL,
    parameters_json bytea NOT NULL,
    enabled boolean NOT NULL DEFAULT false,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision >= 1),
    next_run_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    deleted_at timestamptz,
    CHECK ((enabled AND next_run_at IS NOT NULL AND deleted_at IS NULL) OR (NOT enabled AND next_run_at IS NULL))
);
CREATE UNIQUE INDEX scheduler_definitions_active_name_key_uq ON scheduler_definitions(name_key) WHERE deleted_at IS NULL;
CREATE INDEX scheduler_definitions_due_idx ON scheduler_definitions(enabled, next_run_at, id) WHERE deleted_at IS NULL;
CREATE TABLE scheduler_executions (
    id uuid PRIMARY KEY,
    definition_id uuid NOT NULL REFERENCES scheduler_definitions(id),
    definition_revision bigint NOT NULL CHECK (definition_revision >= 1),
    task_type text COLLATE "C" NOT NULL,
    scheduled_for timestamptz NOT NULL,
    started_at timestamptz NOT NULL,
    finished_at timestamptz NOT NULL,
    status text COLLATE "C" NOT NULL CHECK (status IN ('succeeded', 'failed')),
    error_code text COLLATE "C",
    executor_owner text COLLATE "C" NOT NULL,
    CHECK (scheduled_for <= started_at),
    CHECK (started_at <= finished_at),
    CHECK ((status = 'succeeded' AND error_code IS NULL) OR (status = 'failed' AND error_code IS NOT NULL)),
    UNIQUE(definition_id, scheduled_for)
);
CREATE INDEX scheduler_executions_history_idx ON scheduler_executions(started_at DESC, id DESC);
CREATE INDEX scheduler_executions_definition_idx ON scheduler_executions(definition_id, started_at DESC, id DESC);

-- 持久认领与执行历史分离；每次重试保留同一个执行 ID，供 handler 做幂等处理。
CREATE TABLE scheduler_runs (
 id UUID PRIMARY KEY,
 definition_id UUID NOT NULL REFERENCES scheduler_definitions(id),
 definition_revision BIGINT NOT NULL,
 task_type TEXT NOT NULL,
 parameters_json BYTEA NOT NULL,
 scheduled_for TIMESTAMPTZ NOT NULL,
 state TEXT NOT NULL CHECK (state IN ('queued','running','succeeded','failed')),
 claim_token TEXT,
 lease_until TIMESTAMPTZ,
 attempts INTEGER NOT NULL DEFAULT 0,
 created_at TIMESTAMPTZ NOT NULL,
 UNIQUE(definition_id, scheduled_for)
);
CREATE UNIQUE INDEX scheduler_runs_active_definition ON scheduler_runs(definition_id) WHERE state IN ('queued','running');
CREATE INDEX scheduler_runs_claim ON scheduler_runs(state, lease_until, scheduled_for);
