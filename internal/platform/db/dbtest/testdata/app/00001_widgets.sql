-- Test-only tenant-owned table following the conventions of package migrate.

-- +goose Up
CREATE TABLE dbtest_app.widgets (
    tenant_id uuid NOT NULL,
    id        uuid NOT NULL,
    name      text NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name)
);
ALTER TABLE dbtest_app.widgets ENABLE ROW LEVEL SECURITY;
ALTER TABLE dbtest_app.widgets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON dbtest_app.widgets
    USING (tenant_id = current_setting('app.tenant_id')::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);
GRANT USAGE ON SCHEMA dbtest_app TO portun_app, portun_platform;
GRANT SELECT, INSERT, UPDATE, DELETE ON dbtest_app.widgets TO portun_app, portun_platform;

-- +goose Down
DROP TABLE dbtest_app.widgets;
