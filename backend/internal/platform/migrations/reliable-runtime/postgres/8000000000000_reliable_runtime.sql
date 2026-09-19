-- +goose Up
CREATE TABLE reliable_outbox (
    id text PRIMARY KEY,
    topic text NOT NULL,
    business_key text NOT NULL,
    payload bytea NOT NULL,
    state text NOT NULL CHECK (state IN ('pending', 'claimed', 'delivered', 'retry')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at timestamptz NOT NULL,
    occurred_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    claimed_by text,
    claim_token text,
    claim_until timestamptz,
    delivered_at timestamptz,
    last_error_code text,
    CHECK ((state = 'claimed' AND claimed_by IS NOT NULL AND claim_token ~ '^[0-9a-f]{32}$' AND claim_until IS NOT NULL)
        OR (state <> 'claimed' AND claimed_by IS NULL AND claim_token IS NULL AND claim_until IS NULL)),
    UNIQUE (topic, business_key)
);
CREATE INDEX reliable_outbox_ready_idx
    ON reliable_outbox (state, available_at, created_at, id);
CREATE INDEX reliable_outbox_claim_idx
    ON reliable_outbox (state, claim_until);
CREATE TABLE reliable_consumer_receipt (
    consumer_name text NOT NULL,
    business_key text NOT NULL,
    event_id text NOT NULL REFERENCES reliable_outbox(id),
    processed_at timestamptz NOT NULL,
    PRIMARY KEY (consumer_name, business_key)
);
