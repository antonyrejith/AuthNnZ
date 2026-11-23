-- The API idempotency cache (ADR-0009): the stored result of a POST request,
-- kept for 24 hours, so that a client can repeat the request safely.
--
-- A platform-level, ephemeral table: no row-level security (the nil UUID in
-- scope_id stands for the platform; a tenant's ID scopes tenant APIs) and no
-- access for the application role. It never holds tenant business data, only
-- what the API already returned to the same caller. It is on the reviewed
-- exclusion list of the audit reconciler (ADR-0008).

-- +goose Up
CREATE TABLE api.idempotency_keys (
    scope_id         uuid        NOT NULL, -- the tenant, or the nil UUID for platform APIs
    key              text        NOT NULL CHECK (key ~ '^[ -~]{1,255}$'),
    request_hash     text        NOT NULL,
    status           text        NOT NULL CHECK (status IN ('in_progress', 'completed')),
    response_status  integer     NULL,
    response_headers jsonb       NULL,
    response_body    bytea       NULL,
    created_at       timestamptz NOT NULL,
    expires_at       timestamptz NOT NULL, -- in_progress rows: the end of the lease
    PRIMARY KEY (scope_id, key),
    CHECK ((status = 'completed') = (response_status IS NOT NULL))
);
CREATE INDEX idempotency_keys_expiry ON api.idempotency_keys (expires_at);

GRANT USAGE ON SCHEMA api TO portun_platform;
GRANT SELECT, INSERT, UPDATE, DELETE ON api.idempotency_keys TO portun_platform;

-- +goose Down
DROP TABLE api.idempotency_keys;
