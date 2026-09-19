-- +goose Up
CREATE TABLE reliable_outbox (
    id TEXT PRIMARY KEY,
    topic TEXT NOT NULL,
    business_key TEXT NOT NULL,
    payload BLOB NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending', 'claimed', 'delivered', 'retry')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at TIMESTAMP NOT NULL,
    occurred_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP NOT NULL,
    claimed_by TEXT,
    claim_token TEXT,
    claim_until TIMESTAMP,
    delivered_at TIMESTAMP,
    last_error_code TEXT,
    CHECK ((state = 'claimed' AND claimed_by IS NOT NULL AND length(claim_token) = 32
            AND claim_token NOT GLOB '*[^0-9a-f]*' AND claim_until IS NOT NULL)
        OR (state <> 'claimed' AND claimed_by IS NULL AND claim_token IS NULL AND claim_until IS NULL)),
    UNIQUE (topic, business_key)
);
CREATE INDEX reliable_outbox_ready_idx
    ON reliable_outbox (state, available_at, created_at, id);
CREATE INDEX reliable_outbox_claim_idx
    ON reliable_outbox (state, claim_until);
CREATE TABLE reliable_consumer_receipt (
    consumer_name TEXT NOT NULL,
    business_key TEXT NOT NULL,
    event_id TEXT NOT NULL REFERENCES reliable_outbox(id),
    processed_at TIMESTAMP NOT NULL,
    PRIMARY KEY (consumer_name, business_key)
);
