# ADR-0017: Testing strategy, test infrastructure and quality gates

- Status: accepted
- Date: 2026-11-22
- Deciders: Principal Architect, under the approved plan ([09](../plan/09-testing-strategy.md))

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
- **Gates:** `make pincheck lint build test test-integration` on every commit;
  coverage ≥ 80% per module and ≥ 90% for security-critical modules, measured
  per increment.
- **CI:** GitHub Actions. The hosted runner's base image cannot be pinned by
  digest; every toolchain the jobs use is installed at an exact,
  checksum-verified version instead (Go via `GOTOOLCHAIN` and the module
  proxy's checksum database; tools via the tools module). This is a listed
  exception to D-15, pending the owner's agreement; self-hosted runners with
  pinned images remove it.

## Consequences
- Tests prove behavior against real infrastructure, not mocks of it.
