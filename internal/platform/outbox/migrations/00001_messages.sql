-- The transactional outbox (ADR-0007). Rows are written in the same
-- transaction as the change they describe and published by the relay.

-- +goose Up
CREATE TABLE outbox.messages (
    tenant_id    uuid        NOT NULL, -- the nil UUID for platform-level messages
    id           uuid        NOT NULL, -- the bus message ID (deduplication)
    stream       text        NOT NULL CHECK (stream IN ('AUDIT', 'EVENTS', 'CONFIG')),
    subject      text        NOT NULL CHECK (subject <> ''),
    payload      jsonb       NOT NULL,
    published_at timestamptz NULL,     -- AUDIT rows: last publish; the audit writer deletes them
    xid          xid8        NOT NULL DEFAULT pg_current_xact_id(),
    created_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id)
);
CREATE INDEX messages_relay_order ON outbox.messages (created_at, id);

ALTER TABLE outbox.messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox.messages FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON outbox.messages
    USING (tenant_id = current_setting('app.tenant_id')::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);

-- The application role may only insert, for its own tenant (the policy).
-- The platform role (BYPASSRLS) writes platform-level messages and runs the
-- relay and the audit writer: it may read, mark published and delete.
GRANT USAGE ON SCHEMA outbox TO portun_app, portun_platform;
GRANT INSERT ON outbox.messages TO portun_app;
GRANT SELECT, INSERT, DELETE ON outbox.messages TO portun_platform;
GRANT UPDATE (published_at) ON outbox.messages TO portun_platform;

-- +goose Down
DROP TABLE outbox.messages;
