-- Versioned configuration, settings with inheritance, runtime bundles
-- (ADR-0003, plan 15).

-- +goose Up

-- A configuration object (a flow, a theme, a policy ...) of one environment.
-- version is the object's etag: it grows with every change to the object or
-- to one of its versions.
CREATE TABLE config.objects (
    tenant_id       uuid        NOT NULL,
    environment_id  uuid        NOT NULL,
    id              uuid        NOT NULL,
    kind            text        NOT NULL CHECK (kind <> ''),
    name            text        NOT NULL CHECK (name <> ''),
    status          text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    current_version int         NULL,
    version         bigint      NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, environment_id, kind, name)
);
ALTER TABLE config.objects ENABLE ROW LEVEL SECURITY;
ALTER TABLE config.objects FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON config.objects
    USING (tenant_id = current_setting('app.tenant_id')::uuid
           AND environment_id = current_setting('app.environment_id')::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid
                AND environment_id = current_setting('app.environment_id')::uuid);

-- The versions of an object. A draft is editable; a published version is
-- immutable (trigger below) and versions are never deleted.
CREATE TABLE config.versions (
    tenant_id    uuid        NOT NULL,
    object_id    uuid        NOT NULL,
    version      int         NOT NULL CHECK (version > 0),
    status       text        NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'archived')),
    spec         jsonb       NOT NULL,
    checksum     text        NOT NULL,
    change_note  text        NOT NULL DEFAULT '',
    created_by   text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    published_at timestamptz NULL,
    PRIMARY KEY (tenant_id, object_id, version),
    FOREIGN KEY (tenant_id, object_id) REFERENCES config.objects (tenant_id, id)
);
ALTER TABLE config.versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE config.versions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON config.versions
    USING (tenant_id = current_setting('app.tenant_id')::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);

-- +goose StatementBegin
CREATE FUNCTION config.versions_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'config.versions rows are never deleted'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF OLD.status <> 'draft' AND (
           NEW.spec IS DISTINCT FROM OLD.spec
        OR NEW.checksum IS DISTINCT FROM OLD.checksum
        OR NEW.change_note IS DISTINCT FROM OLD.change_note
        OR NEW.version IS DISTINCT FROM OLD.version
        OR NEW.object_id IS DISTINCT FROM OLD.object_id
        OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
        OR NEW.status = 'draft') THEN
        RAISE EXCEPTION 'a published configuration version is immutable'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER versions_guard BEFORE UPDATE OR DELETE ON config.versions
    FOR EACH ROW EXECUTE FUNCTION config.versions_guard();

-- The values of settings at the tenant level (level_environment_id is the
-- nil UUID) and at the environment level. The nil UUID stands for "no
-- environment" instead of NULL so that the level is part of the primary key,
-- which logical replication (reconciliation) and the audit row references
-- use, and so that dbtest.AssertRLS applies the plain tenant policy: this
-- table has no environment_id column because a row of the tenant level
-- belongs to every environment of the tenant.
CREATE TABLE config.settings (
    tenant_id            uuid        NOT NULL,
    level_environment_id uuid        NOT NULL,
    key                  text        NOT NULL CHECK (key <> ''),
    mode                 text        NOT NULL DEFAULT 'default' CHECK (mode IN ('default', 'parent', 'own')),
    value                jsonb       NULL,
    version              bigint      NOT NULL DEFAULT 1,
    updated_by           text        NOT NULL DEFAULT '',
    updated_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, key, level_environment_id),
    CHECK ((mode = 'own') = (value IS NOT NULL))
);
ALTER TABLE config.settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE config.settings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON config.settings
    USING (tenant_id = current_setting('app.tenant_id')::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);

-- Compiled, immutable bundles of an environment.
CREATE TABLE config.bundles (
    tenant_id      uuid        NOT NULL,
    environment_id uuid        NOT NULL,
    version        bigint      NOT NULL CHECK (version > 0),
    checksum       text        NOT NULL,
    payload        jsonb       NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, environment_id, version)
);
ALTER TABLE config.bundles ENABLE ROW LEVEL SECURITY;
ALTER TABLE config.bundles FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON config.bundles
    USING (tenant_id = current_setting('app.tenant_id')::uuid
           AND environment_id = current_setting('app.environment_id')::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid
                AND environment_id = current_setting('app.environment_id')::uuid);

CREATE TABLE config.publications (
    tenant_id      uuid        NOT NULL,
    environment_id uuid        NOT NULL,
    bundle_version bigint      NOT NULL,
    published_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    published_by   text        NOT NULL,
    change_note    text        NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, environment_id, bundle_version)
);
ALTER TABLE config.publications ENABLE ROW LEVEL SECURITY;
ALTER TABLE config.publications FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON config.publications
    USING (tenant_id = current_setting('app.tenant_id')::uuid
           AND environment_id = current_setting('app.environment_id')::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid
                AND environment_id = current_setting('app.environment_id')::uuid);

-- Platform-level settings: the baseline every tenant inherits. Not tenant
-- data: no row-level security; tenants may only read it.
CREATE TABLE config.platform_settings (
    key        text        NOT NULL PRIMARY KEY CHECK (key <> ''),
    value      jsonb       NOT NULL,
    policy     text        NOT NULL CHECK (policy IN ('overridable', 'bounded', 'additive', 'locked')),
    version    bigint      NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- The application role never deletes, truncates or references (the
-- conventions of package migrate); the platform role reads.
GRANT USAGE ON SCHEMA config TO portun_app, portun_platform;
GRANT SELECT, INSERT, UPDATE ON config.objects, config.versions, config.settings,
    config.bundles, config.publications TO portun_app;
GRANT SELECT ON config.objects, config.versions, config.settings,
    config.bundles, config.publications TO portun_platform;
GRANT SELECT ON config.platform_settings TO portun_app;
GRANT SELECT, INSERT, UPDATE ON config.platform_settings TO portun_platform;

-- +goose Down
DROP TABLE config.platform_settings;
DROP TABLE config.publications;
DROP TABLE config.bundles;
DROP TABLE config.settings;
DROP TABLE config.versions;
DROP FUNCTION config.versions_guard();
DROP TABLE config.objects;
