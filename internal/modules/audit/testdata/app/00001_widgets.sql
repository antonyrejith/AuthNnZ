-- Test-only tenant-owned table for audited changes.

-- +goose Up
CREATE TABLE audittest.widgets (
    tenant_id uuid NOT NULL,
    id        uuid NOT NULL,
    name      text NOT NULL,
    PRIMARY KEY (tenant_id, id)
);
ALTER TABLE audittest.widgets ENABLE ROW LEVEL SECURITY;
ALTER TABLE audittest.widgets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audittest.widgets
    USING (tenant_id = current_setting('app.tenant_id')::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);
GRANT USAGE ON SCHEMA audittest TO portun_app, portun_platform;
GRANT SELECT, INSERT, UPDATE, DELETE ON audittest.widgets TO portun_app, portun_platform;

-- +goose Down
DROP TABLE audittest.widgets;
