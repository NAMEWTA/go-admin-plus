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
			AND substr(business_key, 1, 6) = 'login:'
			AND length(substr(business_key, 7)) = 32
			AND substr(business_key, 7) NOT GLOB '*[^a-f0-9]*')
		OR
		(topic IN ('operation.created', 'operation.updated', 'operation.deleted') AND (
			substr(business_key, 1, 9) = 'resource:'
			AND business_key NOT GLOB '*[^a-z0-9_:-]*'
			AND business_key NOT LIKE '%::%'
			AND substr(business_key, -1) <> ':'
			AND (length(business_key) - length(replace(business_key, ':', ''))) IN (2, 3)
			AND instr(substr(business_key, 10), ':') BETWEEN 2 AND 65
			AND substr(substr(business_key, 10), 1, 1) GLOB '[a-z0-9]'
			AND substr(
				substr(business_key, 10),
				instr(substr(business_key, 10), ':') + 1,
				CASE
					WHEN instr(substr(substr(business_key, 10), instr(substr(business_key, 10), ':') + 1), ':') = 0
					THEN length(substr(substr(business_key, 10), instr(substr(business_key, 10), ':') + 1))
					ELSE instr(substr(substr(business_key, 10), instr(substr(business_key, 10), ':') + 1), ':') - 1
				END
			) <> ''
			AND length(substr(substr(business_key, 10), instr(substr(business_key, 10), ':') + 1)) <= 129
			AND (
				(length(business_key) - length(replace(business_key, ':', '')) = 2
					AND length(substr(substr(business_key, 10), instr(substr(business_key, 10), ':') + 1)) BETWEEN 1 AND 64
					AND substr(substr(substr(business_key, 10), instr(substr(business_key, 10), ':') + 1), 1, 1) GLOB '[a-z0-9]')
				OR
				(length(business_key) - length(replace(business_key, ':', '')) = 3
					AND instr(substr(substr(business_key, 10), instr(substr(business_key, 10), ':') + 1), ':') BETWEEN 2 AND 65
					AND length(substr(substr(substr(business_key, 10), instr(substr(business_key, 10), ':') + 1), 1, instr(substr(substr(business_key, 10), instr(substr(business_key, 10), ':') + 1), ':') - 1)) BETWEEN 1 AND 64
					AND substr(substr(substr(business_key, 10), instr(substr(business_key, 10), ':') + 1), 1, 1) GLOB '[a-z0-9]'
					AND length(substr(substr(substr(business_key, 10), instr(substr(business_key, 10), ':') + 1), instr(substr(substr(business_key, 10), instr(substr(business_key, 10), ':') + 1), ':') + 1)) BETWEEN 1 AND 64
					AND substr(substr(substr(substr(business_key, 10), instr(substr(business_key, 10), ':') + 1), instr(substr(substr(business_key, 10), instr(substr(business_key, 10), ':') + 1), ':') + 1), 1, 1) GLOB '[a-z0-9]')
			)
		))
	),
    actor_ref TEXT CHECK (
        length(actor_ref) BETWEEN 16 AND 72
        AND substr(actor_ref, 1, 8) = 'account:'
        AND substr(actor_ref, 9, 1) GLOB '[a-z0-9]'
        AND substr(actor_ref, 9) NOT GLOB '*[^a-z0-9_-]*'
    ),
    payload BLOB NOT NULL CHECK (length(payload) BETWEEN 2 AND 1024),
	occurred_at TIMESTAMP NOT NULL,
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
