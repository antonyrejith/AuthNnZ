-- Test-only environment-scoped table following the conventions of package
-- migrate.

-- +goose Up
CREATE TABLE dbtest_app.gadgets (
    tenant_id      uuid NOT NULL,
    environment_id uuid NOT NULL,
    id             uuid NOT NULL,
    name           text NOT NULL,
    PRIMARY KEY (tenant_id, id)
);
ALTER TABLE dbtest_app.gadgets ENABLE ROW LEVEL SECURITY;
ALTER TABLE dbtest_app.gadgets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON dbtest_app.gadgets
    USING (tenant_id = current_setting('app.tenant_id')::uuid
           AND environment_id = current_setting('app.environment_id')::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid
                AND environment_id = current_setting('app.environment_id')::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON dbtest_app.gadgets TO portun_app, portun_platform;

-- +goose Down
DROP TABLE dbtest_app.gadgets;
