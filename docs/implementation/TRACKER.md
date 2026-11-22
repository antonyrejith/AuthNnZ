# Implementation Tracker

Live status of implementation tasks. Status values: Not started, In progress, In review, Done (with evidence), Blocked (with reason).
Tasks for later increments are added at the start of each increment ([plan §4.3](00-implementation-plan.md#43-later-increments)).

**Overall:** plan approved by the owner; increment B0 in progress.

| Increment | Task | Work | Owner role | Status | Evidence |
|-----------|------|------|-----------|--------|----------|
| B0 | T0.1 | Repository bootstrap: `go.mod` (Go 1.27, toolchain 1.27.4), tool directives, Makefile, golangci-lint config, `tools/pincheck`, Renovate … | DevOps | In review | Commit d6ec801. `make pincheck lint build test` passed (pincheck tests: 25 cases); awaiting independent review |
| B0 | T0.2 | `CLAUDE.md` working rules; ADRs 0001, 0002, 0003, 0007, 0008, 0009, 0014, 0016, 0017 | Principal Architect | In review | `CLAUDE.md` and ADRs 0001, 0002, 0003, 0007, 0008, 0009, 0014, 0016, 0017 written; awaiting independent review |
| B0 | T0.3 | `platform/ids`, `errs`, `log` (redaction types), `config`, `version` | Backend | In review | Packages `ids`, `errs`, `secret`, `scope`, `log`, `config`, `version` with tests. `make pincheck lint build test test-integration tidy-check` passed; `FuzzParse` 20 s, ~694k execs, no failures; TypeID vectors checked against an independent encoder; awaiting independent review |
| B0 | T0.4 | `platform/db`: pgx pool, tenant-bound transactions with RLS, migrations runner, keyset pagination; `testkit` PostgreSQL template databases | Database | In review | Commit 4b8aa01 (merged with T0.6; go.mod conflict resolved, one test adapted to non-comparable secret.String). Orchestrator re-ran `make pincheck lint build test test-integration tidy-check` against PostgreSQL 19: pass. Awaiting independent and security review |
| B0 | T0.5 | `platform/crypto` and `kms`: approved algorithms, envelope encryption, local and OpenBao KMS, per-tenant KEK, crypto-shredding | Security | In review | Commit 4fd5cdb (merged; go.mod conflict resolved). Contract suite (10 cases) passes on local and real OpenBao transit; FuzzOpen 30 s, 1.28M execs. Orchestrator re-ran `make pincheck lint build test test-integration tidy-check`: pass. Open: local KMS coverage 87.2% < 90% target. Awaiting independent and security review |
| B0 | T0.6 | `platform/bus`: NATS JetStream with embedded server | Backend | In review | Commit 7b74d83 (merged). Orchestrator re-ran `make pincheck lint build test test-integration tidy-check`: pass; agent ran bus integration tests `-count=10 -race`: pass. Awaiting independent review |
| B0 | T0.7 | `platform/httpx` (timeouts, middleware, problem details, health) and `telemetry` (OpenTelemetry) | Backend | In review | Commit 94c6bef (merged; go.mod conflict resolved). Includes SEC-09 CORS, CSRF, frame options, hardened cookies. Orchestrator re-ran `make pincheck lint build test test-integration tidy-check`: pass. Coverage httpx 90.5%, telemetry 92.5% (agent run). Awaiting independent and security review |
| B0 | T0.8 | Audit module: registry, `audit.Tx` and `audit.Change`, outbox and relay, durable spool, per-tenant writer with gapless sequence and JCS hash … | Backend + Security + Database | Not started | — |
| B0 | T0.9 | Tenancy module: partners, tenants, environments; provisioning, suspend, export, crypto-shred delete; tenant resolution by host | Backend | Not started | — |
| B0 | T0.10 | Configuration module: versioned objects, JSON Schema validation, inheritance resolution with modes and merge rules, CEL environment, … | Backend + Architect | Not started | — |
| B0 | T0.11 | OpenAPI 3.2 pipeline (lint, bundle, breaking-change check, generator spike D-16); Platform and Management API endpoints for B0 | Backend | Not started | — |
| B0 | T0.12 | `cmd/portun` all-in-one, graceful shutdown, Dockerfile, Compose stack | DevOps | Not started | — |
| B0 | T0.13 | Test harnesses: isolation and audit-coverage matrices generated from OpenAPI, reconciliation after integration suites, fuzz and benchmark harnesses | Test | Not started | — |
| B0 | T0.14 | Integration: the "hello tenant" end-to-end scenario (create tenant → publish configuration → resolve by host → audit chain verified → zero … | QA + Reviewers | Not started | — |
| B1 | T1.1 | Identity module: dynamic user schema, identifiers with verification, static and dynamic groups, lifecycle with hooks, search, device registry | Backend + Database | Not started | — |
| B1 | T1.2 | Credentials: Argon2id passwords with policies and breached-password check (offline corpus; online range API class X), TOTP/HOTP (RFC test … | Security + Backend | Not started | — |
| B1 | T1.3 | Signing keys per tenant, rotation, JWKS overlap | Security | Not started | — |
| B1 | T1.4 | Sessions: SSO sessions (Valkey and PostgreSQL), timeouts, listing and revocation, sign-out everywhere, BFF token handler | Backend | Not started | — |
| B1 | T1.5 | Flow engine: graph schema, node registry, transaction state, bundle pinning, server-driven UI, default login, sign-up, recovery and step-up flows, … | Backend + Architect | Not started | — |
| B1 | T1.6 | OAuth 2.1 / OIDC provider: authorization code + PKCE, client credentials (secret, `private_key_jwt`), refresh rotation with reuse detection, … | Backend + Security | Not started | — |
| B1 | T1.7 | Hosted login renderer (Lit web components) and the headless authentication API | Frontend | Not started | — |
| B1 | T1.8 | Management API for B1 resources; admin sign-in through the platform's own OIDC; machine clients with scopes. Admin authorization is scope-based … | Backend | Not started | — |
| B1 | T1.9 | Brute-force and credential-stuffing protection, rate limiting, new-device notifications | Security | Not started | — |
| B1 | T1.10 | Tests: unit and integration per module, OpenID conformance suite (or our own conformance tests if the suite image is unreachable), Playwright … | Test + QA | Not started | — |
| B1 | T1.11 | Integration, reviews and corrections | All | Not started | — |
