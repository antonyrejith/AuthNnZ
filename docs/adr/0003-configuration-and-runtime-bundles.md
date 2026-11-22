# ADR-0003: Versioned configuration, inheritance and runtime bundles

- Status: accepted
- Date: 2026-11-22
- Deciders: Principal Architect, under the approved plan ([04 §2](../plan/04-architecture.md#2-how-100-dynamic-works), [15](../plan/15-hierarchy-and-shared-settings.md))

## Context
Every behavior is tenant configuration ("100% dynamic"), changed at runtime
with draft → validate → simulate → publish → rollback, and the data plane must
read it without slowing requests down.

## Decision

**Storage** (schema `config`):
- `config.objects(tenant_id, environment_id, id, kind, scope_level, scope_id, current_version, status)`
- `config.versions(tenant_id, object_id, version, status, spec jsonb, checksum, change_note, created_by, created_at)`;
  versions are immutable once published; drafts are editable until published.
- `config.publications(tenant_id, environment_id, bundle_version, published_at, published_by)`.

**Validation:** each kind registers a JSON Schema 2020-12 document; specs are
validated on save and on publish. CEL expressions inside specs are
type-checked at publish time.

**Settings and inheritance:** each setting declares a merge rule (most
specific, strictest, intersection, union, ceiling) and the parent's override
policy (`overridable`, `bounded`, `additive`, `locked`). Each value at each
level has a mode (Default, Parent, Template, Own). The resolver walks
platform → partner → tenant → environment → organization path → application.

**Runtime bundles:** publishing compiles a tenant environment into an
immutable bundle (resolved settings, compiled CEL programs, indexes) with a
monotonically increasing version and a checksum, stored in
`config.bundles(tenant_id, environment_id, version, checksum, payload)`. A
`config.<tenant>.<env>` message is published on the bus. Each process keeps the
current bundle per tenant environment behind an `atomic.Pointer`, loads lazily
on first use, and swaps atomically on notification. Long-running transactions
(for example a login flow) record the bundle version they started with.

**Static stability:** if the bus is unavailable, processes poll the latest
bundle version periodically and keep serving the last good bundle.

## Consequences
- Requests never parse raw configuration; they read a compiled, immutable bundle.
- Publish → effective target: under 5 seconds at p99.
- Every configuration change is a normal audited write.
