-- Test-only tenant tables for reconciliation tests.

-- +goose Up
CREATE TABLE audittest_app.widgets (
    tenant_id uuid NOT NULL,
    id        uuid NOT NULL,
    name      text NOT NULL,
    PRIMARY KEY (tenant_id, id)
);
ALTER TABLE audittest_app.widgets ENABLE ROW LEVEL SECURITY;
ALTER TABLE audittest_app.widgets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audittest_app.widgets
    USING (tenant_id = current_setting('app.tenant_id')::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);

CREATE TABLE audittest_app.rate_counters (
    tenant_id uuid NOT NULL,
    k         text NOT NULL,
    n         bigint NOT NULL,
    PRIMARY KEY (tenant_id, k)
);
ALTER TABLE audittest_app.rate_counters ENABLE ROW LEVEL SECURITY;
ALTER TABLE audittest_app.rate_counters FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audittest_app.rate_counters
    USING (tenant_id = current_setting('app.tenant_id')::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);

GRANT USAGE ON SCHEMA audittest_app TO portun_app, portun_platform;
GRANT SELECT, INSERT, UPDATE, DELETE ON audittest_app.widgets, audittest_app.rate_counters TO portun_app, portun_platform;

-- +goose Down
DROP TABLE audittest_app.rate_counters, audittest_app.widgets;
