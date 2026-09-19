-- +goose Up
CREATE TABLE audit_facts (
    topic TEXT NOT NULL CHECK (topic IN (
        'iam.login.succeeded',
        'iam.login.failed',
		'operation.created',
		'operation.updated',
		'operation.deleted'
    )),
    business_key TEXT NOT NULL CHECK (length(business_key) BETWEEN 3 AND 255) CHECK (
		(topic IN ('iam.login.succeeded', 'iam.login.failed')
			AND business_key ~ '^login:[a-f0-9]{32}$')
		OR
		(topic IN ('operation.created', 'operation.updated', 'operation.deleted') AND (
			business_key ~ '^resource:[a-z0-9][a-z0-9_-]{0,63}:[a-z0-9][a-z0-9_-]{0,63}$'
			OR business_key ~ '^resource:[a-z0-9][a-z0-9_-]{0,63}:[a-z0-9][a-z0-9_-]{0,63}:[a-z0-9][a-z0-9_-]{0,63}$'
		))
	),
    actor_ref TEXT CHECK (actor_ref ~ '^account:[a-z0-9][a-z0-9_-]{7,63}$'),
    payload BYTEA NOT NULL CHECK (octet_length(payload) BETWEEN 2 AND 1024),
	occurred_at TIMESTAMPTZ NOT NULL,
	event_id TEXT UNIQUE,
    target_type TEXT,
    target_id TEXT,
    operation_code TEXT,
    outcome TEXT CHECK (outcome IN ('succeeded','failed')),
    trace_id TEXT,
    PRIMARY KEY (topic, business_key)
);

CREATE INDEX audit_facts_occurred_idx
	ON audit_facts (occurred_at DESC, topic, business_key);
CREATE INDEX audit_facts_topic_occurred_idx
	ON audit_facts (topic, occurred_at DESC, business_key);
