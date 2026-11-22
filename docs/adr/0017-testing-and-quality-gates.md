# ADR-0017: Testing strategy, test infrastructure and quality gates

- Status: accepted
- Date: 2026-11-22
- Deciders: Principal Architect, under the approved plan ([09](../plan/09-testing-strategy.md))

## Context
Claude implements and reviews the platform (D-28, D-29); quality must be proven
by executed checks, never asserted ([09](../plan/09-testing-strategy.md)).

## Options considered
1. Mocks for infrastructure: fast, but prove little about SQL, RLS and the bus.
2. Real PostgreSQL, Valkey and NATS in tests via containers and embedding.
   Chosen.

## Decision
- **Unit tests** beside the code; table-driven, `t.Parallel()`, `-race`,
  `-shuffle=on`; time controlled with `testing/synctest`.
- **Integration tests** carry the `integration` build tag and use real
  PostgreSQL 19, Valkey and NATS: PostgreSQL through testcontainers with one
  container per test binary and a migrated template database cloned per test;
  NATS embedded in the test process.
- **Traceability:** each test states the features it covers in its doc comment,
  `// Covers: GOV-01, GOV-11`. A tool combines these with `go test -json` results
  into `docs/implementation/TRACEABILITY.md`; only executed results are reported.
- **Matrices** generated from the OpenAPI documents: tenant isolation (another
  tenant always gets `404`), admin permissions (`x-required-permission`) and
  audit coverage (`x-audit-event`).
- **Reconciliation:** each integration suite ends by checking that every change
  in the test databases has a matching audit record.
- **Gates:** `make gates` = `pincheck lint build test test-integration
  tidy-check vuln secrets cover-check` on every commit, plus
  `generate-check` once code generation exists. Coverage ≥ 80% per package and
  ≥ 90% for security-critical packages; in CI also ≥ 85% on changed lines of
  each pull request (plan 09).
- **CI:** GitHub Actions. The hosted runner's base image cannot be pinned by
  digest; every toolchain the jobs use is installed at an exact,
  checksum-verified version instead (Go via `GOTOOLCHAIN` and the module
  proxy's checksum database; tools via the tools module). This is a listed
  exception to D-15, pending the owner's agreement; self-hosted runners with
  pinned images remove it.

- **Toolchain deviation (pending the owner's agreement):** development and
  this environment use `GOTOOLCHAIN=go1.27.4` (exact, checksum-verified
  download); CI installs Go 1.27.4 with a SHA-pinned `setup-go` and runs with
  `GOTOOLCHAIN=local`; developer tools live in a separate `tools/` module so
  they do not enter the product's dependency graph (plan 08 §1.2 puts them in
  the main `go.mod`).

## Consequences
- Tests prove behavior against real infrastructure, not mocks of it.
