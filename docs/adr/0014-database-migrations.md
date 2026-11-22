# ADR-0014: Database migration strategy

- Status: accepted
- Date: 2026-11-22
- Deciders: Principal Architect, under the approved plan (D-03, [08 §3.7](../plan/08-engineering-standards.md#37-data-access))

## Decision
- **Tool:** goose v3, migrations embedded in the binary with `embed`.
- **Ownership:** each module owns one PostgreSQL schema and its migrations,
  stored beside its PostgreSQL adapter; each schema keeps its own goose version
  table, so modules migrate independently in a fixed order.
- **Roles:** migrations run as the owner role; they create tables, row-level
  security policies and grants for `portun_app` and `portun_platform`.
- **Compatibility:** only expand → migrate → contract changes; a release never
  requires downtime, and the previous release keeps working against the new
  schema.
- **Tests:** every migration set is applied to an empty database in CI and to
  the previous release's schema before release.

## Consequences
- Migrations are versioned with the code that needs them.
