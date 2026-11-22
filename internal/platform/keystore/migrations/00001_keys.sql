-- Per-tenant key-encryption keys and per-subject data keys (crypto-shredding).
-- The nil UUID tenant holds the platform's keys.

-- +goose Up
CREATE TABLE keys.tenant_keks (
    tenant_id    uuid        NOT NULL,
    kek_ref      text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    destroyed_at timestamptz NULL,
    PRIMARY KEY (tenant_id)
);
ALTER TABLE keys.tenant_keks ENABLE ROW LEVEL SECURITY;
ALTER TABLE keys.tenant_keks FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON keys.tenant_keks
    USING (tenant_id = current_setting('app.tenant_id')::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);

CREATE TABLE keys.data_keys (
    tenant_id    uuid        NOT NULL,
    id           uuid        NOT NULL,
    subject      text        NOT NULL CHECK (subject <> ''),
    kek_ref      text        NOT NULL,
    wrapped_dek  bytea       NULL,     -- NULL once destroyed
    created_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    destroyed_at timestamptz NULL,
    PRIMARY KEY (tenant_id, id),
    CHECK ((destroyed_at IS NULL) = (wrapped_dek IS NOT NULL))
);
-- At most one active key per subject.
CREATE UNIQUE INDEX data_keys_active_subject ON keys.data_keys (tenant_id, subject) WHERE destroyed_at IS NULL;
ALTER TABLE keys.data_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE keys.data_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON keys.data_keys
    USING (tenant_id = current_setting('app.tenant_id')::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);

-- Tenant KEKs are created and destroyed by tenancy as the platform role; the
-- application reads its tenant's KEK reference. Rows are never deleted:
-- destroyed keys stay as tombstones.
GRANT USAGE ON SCHEMA keys TO portun_app, portun_platform;
GRANT SELECT ON keys.tenant_keks TO portun_app;
GRANT SELECT, INSERT, UPDATE ON keys.tenant_keks TO portun_platform;
GRANT SELECT, INSERT, UPDATE ON keys.data_keys TO portun_app, portun_platform;

-- +goose Down
DROP TABLE keys.data_keys;
DROP TABLE keys.tenant_keks;
