-- Test-only platform-level table (no tenant_id), in a second schema.

-- +goose Up
CREATE TABLE dbtest_aux.settings (
    key   text PRIMARY KEY,
    value text NOT NULL
);
GRANT USAGE ON SCHEMA dbtest_aux TO portun_platform;
GRANT SELECT, INSERT, UPDATE, DELETE ON dbtest_aux.settings TO portun_platform;

-- +goose Down
DROP TABLE dbtest_aux.settings;
