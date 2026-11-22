-- Partners, tenants, environments, hosts and operations (ADR-0002, plan 15).
--
-- Platform-level tables (partners, tenants, hosts, operations) have no
-- row-level security: they are read and written by the platform role, and
-- tenant resolution by host happens before a tenant is known. They name the
-- tenant in owner_tenant_id (not tenant_id), so that the tenant_id
-- convention of tenant-owned tables (primary key, row-level security) applies
-- only to tenancy.environments. The application role can read nothing but a
-- tenant's basic columns and its own environments.

-- +goose Up
CREATE TABLE tenancy.partners (
    id         uuid        PRIMARY KEY,
    parent_id  uuid        NULL REFERENCES tenancy.partners (id),
    name       text        NOT NULL CHECK (name <> ''),
    status     text        NOT NULL CHECK (status IN ('active', 'suspended')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    version    bigint      NOT NULL DEFAULT 1
);
CREATE INDEX partners_parent ON tenancy.partners (parent_id);

CREATE TABLE tenancy.tenants (
    id         uuid        PRIMARY KEY,
    partner_id uuid        NULL REFERENCES tenancy.partners (id),
    slug       text        NOT NULL UNIQUE CHECK (slug ~ '^[a-z][a-z0-9-]{1,38}[a-z0-9]$'),
    name       text        NOT NULL CHECK (name <> ''),
    status     text        NOT NULL CHECK (status IN ('active', 'suspended', 'deleting', 'deleted')),
    region     text        NOT NULL CHECK (region <> ''),
    plan       text        NOT NULL CHECK (plan <> ''),
    kek_ref    text        NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    deleted_at timestamptz NULL,
    version    bigint      NOT NULL DEFAULT 1
);
CREATE INDEX tenants_partner ON tenancy.tenants (partner_id) WHERE partner_id IS NOT NULL;
CREATE INDEX tenants_created ON tenancy.tenants (created_at, id);

CREATE TABLE tenancy.environments (
    tenant_id  uuid        NOT NULL REFERENCES tenancy.tenants (id),
    id         uuid        NOT NULL,
    name       text        NOT NULL CHECK (name <> ''),
    kind       text        NOT NULL CHECK (kind IN ('development', 'staging', 'production')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    version    bigint      NOT NULL DEFAULT 1,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name)
);
ALTER TABLE tenancy.environments ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenancy.environments FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenancy.environments
    USING (tenant_id = current_setting('app.tenant_id')::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);

CREATE TABLE tenancy.hosts (
    host            text        PRIMARY KEY CHECK (host = lower(host)),
    owner_tenant_id uuid        NOT NULL,
    environment_id  uuid        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (owner_tenant_id, environment_id) REFERENCES tenancy.environments (tenant_id, id)
);
CREATE INDEX hosts_tenant ON tenancy.hosts (owner_tenant_id);

CREATE TABLE tenancy.operations (
    id              uuid        PRIMARY KEY,
    owner_tenant_id uuid        NOT NULL REFERENCES tenancy.tenants (id),
    kind            text        NOT NULL CHECK (kind IN ('export')),
    status          text        NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    result_path     text        NULL,
    error_code      text        NULL,
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at      timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX operations_tenant ON tenancy.operations (owner_tenant_id);

GRANT USAGE ON SCHEMA tenancy TO portun_app, portun_platform;
GRANT SELECT, INSERT, UPDATE ON tenancy.partners TO portun_platform;
GRANT SELECT, INSERT, UPDATE ON tenancy.tenants TO portun_platform;
GRANT SELECT (id, slug, name, status, region, plan) ON tenancy.tenants TO portun_app;
GRANT SELECT ON tenancy.environments TO portun_app;
GRANT SELECT, INSERT, UPDATE ON tenancy.environments TO portun_platform;
GRANT SELECT, INSERT, DELETE ON tenancy.hosts TO portun_platform;
GRANT SELECT, INSERT, UPDATE ON tenancy.operations TO portun_platform;

-- +goose Down
DROP TABLE tenancy.operations;
DROP TABLE tenancy.hosts;
DROP TABLE tenancy.environments;
DROP TABLE tenancy.tenants;
DROP TABLE tenancy.partners;
