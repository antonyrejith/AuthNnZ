# CLAUDE.md — Working Rules for Portun

Portun is an enterprise authentication and authorization platform. This file
is read at the start of every implementation session.

## Source of truth

1. The approved plan: [`docs/plan`](docs/plan) (product and architecture) and
   [`docs/plan/06-decisions.md`](docs/plan/06-decisions.md) (decisions).
2. The approved implementation plan: [`docs/implementation/00-implementation-plan.md`](docs/implementation/00-implementation-plan.md),
   with the [requirement map](docs/implementation/01-requirement-map.md) and the
   live [tracker](docs/implementation/TRACKER.md).
3. Architecture decision records in [`docs/adr`](docs/adr). An ADR refines the
   plan; a deviation from the plan needs the owner's approval.

## Session start

```bash
docker info >/dev/null 2>&1 || (nohup dockerd >/tmp/dockerd.log 2>&1 &)  # integration tests need Docker
make tools                                                                  # pinned tools into ./bin
```

The Makefile pins Go to 1.27.4 (`GOTOOLCHAIN`); always use `make` targets or
export `GOTOOLCHAIN=go1.27.4`.

## Gates before any commit

```bash
make pincheck lint build test test-integration tidy-check secrets cover-check
```

All must pass. `make gates` adds `vuln` (needs network access to vuln.go.dev;
it always runs in CI). Never commit with failing gates; never claim a result that was
not produced by an actual run.

## Rules

- **Pinned versions only** (D-15): exact versions, image digests, action SHAs.
  New dependencies need a reason and a permissive license (MIT, BSD, Apache-2.0,
  ISC, MPL-2.0). Prefer the standard library.
- **Structure** ([ADR-0016](docs/adr/0016-code-structure-and-module-boundaries.md)):
  business modules live in `internal/modules/<name>` with a facade and an
  `internal/` directory; technical packages in `internal/platform/<name>`;
  `cmd/*` only wires.
- **Tenant isolation** ([ADR-0002](docs/adr/0002-tenancy-and-isolation.md)):
  every tenant-owned table has `tenant_id` and row-level security; all
  tenant data access goes through `db.InTenantTx`.
- **100% audit** ([ADR-0008](docs/adr/0008-audit-architecture.md)): every write
  goes through `audit.Change` in the same transaction; events outside a
  transaction go through the spool; no opt-outs.
- **Errors** use `internal/platform/errs` codes; APIs return RFC 9457 problem
  details ([ADR-0009](docs/adr/0009-api-conventions.md)).
- **Logs** use `log/slog` with `snake_case` attribute keys; secrets use
  `secret.String`; never log tokens, passwords, OTPs or full personal data.
- **No placeholders:** no fake success responses, no unimplemented TODOs for
  required behavior, no silently skipped requirements.
- **Tests:** table-driven, parallel, `-race`; integration tests behind the
  `integration` build tag with real PostgreSQL, Valkey and NATS; each test
  names the features it covers in its doc comment:
  `// Covers: GOV-01, GOV-11` ([ADR-0017](docs/adr/0017-testing-and-quality-gates.md)).
- **Reviews:** every change gets an independent review pass and, for
  security-critical code, a security review (D-29). Implementers never approve
  their own work.
- **Agent workflow (owner's instruction):** the main session (higher model)
  plans each task, writes an exact brief (files owned, signatures, schema,
  behaviors, required tests, gates) and reviews the result. Implementation
  agents run on a lower model (`sonnet`) and follow the brief without making
  design decisions. The main session reviews the diff, re-runs the gates and
  probes security-critical paths; defects go back to the same agent as a
  correction brief, repeated until the work passes (escalate to the owner
  after 3 failed cycles).
- **Tracker:** update [`TRACKER.md`](docs/implementation/TRACKER.md) when a task
  changes status, with evidence (commands and results).

## Git

- Develop on `claude/enterprise-auth-service-gai45l`; push only there.
- Commit messages follow Conventional Commits (plan 08 §5), e.g.
  `feat(audit): add hash-chain writer (T0.8)`, and state what and why, the task
  ID, and what was verified.
