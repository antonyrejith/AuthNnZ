# ADR-0002: Tenancy model, identifiers and tenant isolation

- Status: accepted
- Date: 2026-11-22
- Deciders: Principal Architect, under the approved plan ([04 §6](../plan/04-architecture.md#6-multi-tenancy-and-isolation), [15](../plan/15-hierarchy-and-shared-settings.md))

## Context
Cross-tenant data access is a critical risk (R5). Isolation must hold even if
application code has a bug.

## Decision

**Hierarchy:** platform → partner → tenant → environment → organization →
sub-organizations. Partners and tenants are platform-level records; everything
below a tenant is tenant-owned.

**Identifiers** (`internal/platform/ids`): UUIDv7 (time-ordered) stored as
PostgreSQL `uuid`; shown externally as type-prefixed strings in the TypeID
format: `<prefix>_<26 lowercase Crockford base32 characters>`, for example
`ten_01jd8q…`. Each kind has a Go type (`ids.TenantID`, `ids.UserID`, …), so a
user ID cannot be passed where a tenant ID is expected.

**Tenant context** (`internal/platform/scope`): request handlers resolve the
tenant and environment (from the host, the credential or the path) and attach
a `scope.Tenant` to the context. Code that touches tenant data obtains it with
`scope.TenantFrom(ctx)`; a missing scope is an error, never a default.

**Database isolation:**
- Every tenant-owned table has `tenant_id uuid NOT NULL` as the first column of
  its primary key.
- Row-level security is enabled and forced on every tenant-owned table, with
  the policy `tenant_id = current_setting('app.tenant_id')::uuid`.
- The application connects as role `portun_app`, which does not own the tables
  and has no `BYPASSRLS`. Migrations run as the owner role.
- All tenant data access goes through `db.InTenantTx(ctx, fn)`, which starts a
  transaction and runs `SET LOCAL app.tenant_id` from the context scope. Pool
  connections therefore never carry another tenant's setting.
- Platform-level work (managing tenants, the audit writer, the change-capture
  reconciler) uses a separate pool as role `portun_platform` with `BYPASSRLS`,
  through `db.InPlatformTx`, which is used only by those components and is
  itself audited.

## Consequences
- A missing `WHERE tenant_id = …` cannot leak data: the database filters it.
- The tenant-isolation matrix (ADR-0017) still tests every endpoint.
- Two database roles and two pools are needed in every deployment.
