# 00 — Implementation Plan

**Status: proposed, approved by the owner.** No implementation starts before
approval.

This plan turns the approved product plan ([`docs/plan`](../plan)) and the
decision register ([06](../plan/06-decisions.md)) into executable work: the
architecture to build, the increments and tasks, how specialist agents work,
how quality is proven, and what can and cannot be verified in this
environment.

## 1. Requirements in scope

| Source | Content |
|--------|---------|
| [Feature catalog](../plan/02-feature-catalog.md) | 298 features, each mapped to an increment and a verification class in [01 — Requirement map](01-requirement-map.md) |
| [Architecture](../plan/04-architecture.md) | Control/data plane, runtime bundles, flow engine, authorization engine, cells, key hierarchy, SLOs |
| [Engineering standards](../plan/08-engineering-standards.md) | Pinned versions, module structure, Go and TypeScript rules, audit rules, reviews |
| [Testing strategy](../plan/09-testing-strategy.md) | Test levels, matrices, conformance, fuzzing, gates |
| [API standards](../plan/10-api-standards.md) | OpenAPI 3.2, RFC 9457 errors, pagination, idempotency, versioning |
| [Audit](../plan/13-audit-and-change-tracking.md) | 100% audit, seven completeness guarantees, no opt-outs |
| [Hierarchy](../plan/15-hierarchy-and-shared-settings.md) | Setting modes, merge rules, templates, reset |
| Other plan documents | Integration, look and feel, domains, operations, localization, commercialization, SDKs, product forms |

**Release model.** All features ship together at GA (D-23). The build
increments below are internal checkpoints that follow technical dependencies;
they are not releases and nothing is dropped from GA.

## 2. Environment assessment

Inspected on 22 November 2026.

| Capability | Status here | Consequence |
|------------|-------------|-------------|
| Go | 1.24.7 installed; 1.27.4 downloads through the module proxy | Builds use the pinned 1.27.4 toolchain |
| Docker | Client and daemon installed; the daemon must be started each session; images pull from Docker Hub | Integration tests use real PostgreSQL 19, Valkey 9.2, NATS 2.15.3 and partner systems in containers |
| Browser | Chromium with Playwright | End-to-end tests, passkeys through virtual authenticators |
| Node.js 22, Java, Python, Rust, PHP, Ruby | Installed | Front ends and SDKs for these languages can be built and tested |
| Swift/Xcode, Android SDK, .NET, Unity, Unreal | Not available | SDKs and apps for these platforms are class **T** |
| Cloud accounts (AWS, Azure, Google Cloud), domains, provider credentials | None | Cloud and provider integrations are class **X** until the owner provides access |
| External certification, penetration tests, audits | Outside this environment | Prepared for, not performed |

## 3. Architecture to build

The architecture is the one approved in [04](../plan/04-architecture.md). The
concrete choices for implementation:

### 3.1 Code organization

- **One Go module**, `github.com/antonyrejith/portun` (named after the product;
  the repository itself can be renamed to `portun` at any time).
- **Modular monolith** ([08 §2](../plan/08-engineering-standards.md#2-repository-and-code-structure)):
  `internal/platform/*` for technical packages, `internal/modules/<name>` for
  business modules with a public facade and compiler-enforced `internal/`
  boundaries, `cmd/*` for deployables, `api/` for OpenAPI and protobuf, `web/`
  for front ends, `deploy/` for containers and charts, `test/` for end-to-end,
  conformance and load tests.
- **The all-in-one binary `cmd/portun` comes first:** every module in one
  process, with embedded NATS for local and test use. The separate deployables
  (`idp-runtime`, `pdp`, `mgmt-api`, `worker`, `router`, `ext-runtime`,
  `edge-pdp`, `connector-agent`) are compositions of the same modules, added as
  the modules they need are completed.

### 3.2 Data design

| Rule | Implementation |
|------|----------------|
| One PostgreSQL schema per module | `tenancy`, `config`, `audit`, `keys`, `identity`, `oauth`, `session`, `authz`, … |
| Tenant isolation | Every tenant-owned table has `tenant_id` in its primary key; row-level security policies on `current_setting('app.tenant_id')`, set by `SET LOCAL` in each transaction; the application role is not the table owner and RLS is forced |
| IDs | UUIDv7 stored as `uuid`, shown as type-prefixed strings (`usr_01jd…`, `app_01jd…`) |
| Migrations | goose, embedded per module, expand → migrate → contract only |
| Writes | Only through `audit.Tx` (same-transaction outbox; [13](../plan/13-audit-and-change-tracking.md)) |
| Personal data | Encrypted fields with per-subject data keys wrapped by the tenant key; crypto-shredding on erasure and tenant deletion |
| Hot data | Valkey for flow transactions, rate counters, OTPs and session cache; never the system of record |
| History | Audit diffs are authoritative; as-of tables for identity and relationships added with GOV-13 |

**First schemas (increment B0):**

| Schema.table | Purpose |
|--------------|---------|
| `tenancy.partners`, `tenancy.tenants`, `tenancy.environments` | Hierarchy, status, region, key reference |
| `config.objects`, `config.versions`, `config.publications` | Versioned configuration with draft, published and archived states, change notes, checksums |
| `outbox.messages` | Same-transaction audit records and events with the database transaction ID ([ADR-0007](../adr/0007-event-bus-and-outbox.md)) |
| `audit.events` (partitioned by month) | Append-only records with per-tenant gapless sequence, previous hash and hash |
| `audit.checkpoints` | Signed Merkle roots per tenant |
| `keys.tenant_keks`, `keys.data_keys` | Envelope-encryption key references; destroyed for crypto-shredding |

Increment B1 adds `identity.*` (users, identifiers, credentials, groups,
memberships, devices), `oauth.*` (clients, secrets, redirect URIs, API
resources, scopes, consents, refresh-token families), `session.*` and
`keys.signing_keys`. Each increment's task brief defines its schemas before
coding starts.

### 3.3 APIs

| Surface | First endpoints |
|---------|-----------------|
| Platform API (B0) | `POST/GET/PATCH /platform/v1/tenants`, suspend, resume, export (long-running), delete (crypto-shred, reason required) |
| Management API (B0) | Configuration objects: drafts, versions, diff, publish, rollback; effective settings; audit search and verification |
| Protocol endpoints (B1) | Discovery, AS metadata, JWKS, authorize, token, userinfo, introspection, revocation, logout |
| Authentication API (B1) | Headless flow transactions returning server-driven screens |
| Management API (B1) | Users, groups, applications, APIs and scopes, flows, sessions, signing keys |
| Authorization API (B2) | AuthZEN evaluation, evaluations, search; relationships write, read and watch |

All of them follow [10](../plan/10-api-standards.md): OpenAPI 3.2 written first,
generated server interfaces (generator chosen by the D-16 spike in B0), RFC 9457
errors, cursor pagination, `Idempotency-Key`, `ETag`/`If-Match`,
`x-required-permission` and `x-audit-event` on every operation.

### 3.4 Security

- Key hierarchy: a KMS interface with three implementations: a local key file
  for development, OpenBao transit for integration tests, and AWS KMS, Azure Key
  Vault and Google Cloud KMS (class X until accounts exist). Tenant KEK →
  data keys and signing keys.
- Passwords: Argon2id with NIST SP 800-63B policies; constant-time
  comparisons; `crypto/rand` only.
- Tokens: ES256 by default (RS256, PS256, EdDSA available), per-tenant signing
  keys with rotation and JWKS overlap; PKCE S256 required; exact redirect
  matching; `iss` response parameter.
- Web: strict CSP, CSRF protection, hardened cookies, CORS allowlists.
- Abuse: rate limits and brute-force protection in Valkey from B1.
- Outbound calls only through the SSRF-safe egress client.
- A threat model per module, written before its code and checked by the
  security reviewer.

### 3.5 Concurrency

- Every goroutine has an owner and a stop path (`errgroup`, context cancellation).
- Runtime bundles swap atomically (`atomic.Pointer`).
- Audit writing is ordered per tenant through NATS JetStream subjects partitioned
  by tenant, with idempotent consumers.
- Refresh-token rotation and reuse detection use row locks.
- All tests run with `-race`; time-dependent code is tested with `testing/synctest`.

### 3.6 Errors and observability

- **Errors:** `platform/errs` carries stable codes; one table maps codes to HTTP
  status and problem types; internal details never leave the service.
- **Logs:** `log/slog` JSON with `tenant_id` and `trace_id`; secrets redacted by type.
- **Tracing and metrics:** OpenTelemetry on every inbound and outbound call; RED
  metrics; health endpoints `/healthz/startup`, `/healthz/live`, `/healthz/ready`.

### 3.7 Deployment

- **Local:** Docker Compose with PostgreSQL, Valkey, NATS, Mailpit and Grafana
  OTel LGTM, plus the all-in-one binary.
- **Images:** distroless images pinned by digest, with SBOMs.
- **CI:** GitHub Actions with pinned action SHAs.
- **Kubernetes:** Helm chart tested on kind in B10, where Docker allows.
- **Cloud:** OpenTofu modules for the three clouds, written in B10 (class X until
  accounts exist).

## 4. Build increments

Each increment ends with integration, the quality gates in §7, independent
review cycles (§6) and an updated traceability report. An increment is
complete only when every feature assigned to it is implemented and verified to
its class.

| Increment | Delivers | Features |
|-----------|----------|----------|
| **B0** Foundations and platform core | Repository, standards tooling, CI, platform packages, tenancy, configuration engine and runtime bundles, audit core (all seven guarantees), all-in-one binary | 13 |
| **B1** Identity, core authentication and OpenID provider | Users, credentials (password, email OTP and links, TOTP, recovery, passkeys), sessions, signing keys, flow engine with server-driven UI, OAuth 2.1/OIDC provider, hosted login renderer, rate limits and brute-force protection | 37 |
| **B2** Authorization engine | Model language, relationships, CEL conditions, Cedar guardrails, AuthZEN API, explain, consistency tokens, permission catalog, effective permissions, data filtering, sidecar and embedded engine, decision logging | 29 |
| **B3** Organizations, multi-level settings and federation | Organizations and hierarchy, setting modes, merge rules, templates and push, reset, SAML IdP/SP, OIDC inbound, LDAP, SCIM server, JIT, brokering, social login | 33 |
| **B4** Console, portals and experience | React console, account and organization portals, theme studio, effects, screen builder, translations, tours, localization, analytics, as-of views | 29 |
| **B5** Extensibility and developer tools | Actions (WASM), webhooks, event streaming, connectors, configuration as code, CLI, emulator, test mode, Terraform provider, SDK core behavior, test kit, typed permissions | 23 |
| **B6** Security and risk | Risk engine, bot protection, adaptive MFA, push and QR login, device binding, Shared Signals, device trust, fraud, posture center, waiting room | 16 |
| **B7** Governance and privacy | Log streaming, access reviews and requests, SoD, privacy and DSAR, retention, audit alerts, access transparency, governance automation, audit log service | 17 |
| **B8** Advanced and regulated protocols | Token exchange, PAR, JAR/JARM, DPoP, mTLS, RAR, transaction tokens, certificates, Kerberos, device flow, CIBA, verifiable credentials, transaction signing, Secure Payment Confirmation, WS-Fed, OpenID Federation, RADIUS, KYC | 25 |
| **B9** Machine, agent and AI identity | API keys, workload federation, SPIFFE, IoT, MCP server and guard, agent identities, human approvals, AI authorization, AI admin assistant | 13 |
| **B10** Scale, deployment, domains and commercial | Cells and router, multi-region, three clouds, dedicated and customer-cloud cells, self-hosted and air-gapped, Helm, operator, domains and certificates, tenant moves, FIPS, metering, billing, licensing, partner platform, product lines | 35 |
| **B11** SDK breadth, integrations, migration, industry packs | SDKs for every listed language and framework, edge and serverless, games and devices, OEM kit, gateway plugins, ecosystem connectors, importers, HR provisioning, industry packs | 28 |
| **B12** System hardening | Full-scale load, soak, chaos and disaster-recovery tests; conformance suites; accessibility audit; full independent review; final traceability report | — |

### 4.1 B0 tasks

| Task | Work | Owner role | Depends on |
|------|------|-----------|-----------|
| T0.1 | Repository bootstrap: `go.mod` (Go 1.27, toolchain 1.27.4), tool directives, Makefile, golangci-lint config, `tools/pincheck`, Renovate (propose-only), dev container, CI workflow, PR template | DevOps | — |
| T0.2 | `CLAUDE.md` working rules; ADRs 0001, 0002, 0003, 0007, 0008, 0009, 0014, 0016, 0017 | Principal Architect | T0.1 |
| T0.3 | `platform/ids`, `errs`, `log` (redaction types), `config`, `version` | Backend | T0.1 |
| T0.4 | `platform/db`: pgx pool, tenant-bound transactions with RLS, migrations runner, keyset pagination; `testkit` PostgreSQL template databases | Database | T0.3 |
| T0.5 | `platform/crypto` and `kms`: approved algorithms, envelope encryption, local and OpenBao KMS, per-tenant KEK, crypto-shredding | Security | T0.3 |
| T0.6 | `platform/bus`: NATS JetStream with embedded server | Backend | T0.3 |
| T0.7 | `platform/httpx` (timeouts, middleware, problem details, health) and `telemetry` (OpenTelemetry) | Backend | T0.3 |
| T0.8 | Audit module: registry, `audit.Tx` and `audit.Change`, outbox and relay, durable spool, per-tenant writer with gapless sequence and JCS hash chain, signed checkpoints, PostgreSQL store, search, verification CLI, change-capture reconciler, audit of audit access | Backend + Security + Database | T0.4–T0.7 |
| T0.9 | Tenancy module: partners, tenants, environments; provisioning, suspend, export, crypto-shred delete; tenant resolution by host | Backend | T0.4, T0.5, T0.8 |
| T0.10 | Configuration module: versioned objects, JSON Schema validation, inheritance resolution with modes and merge rules, CEL environment, runtime-bundle compiler with atomic swap and change propagation | Backend + Architect | T0.4, T0.8 |
| T0.11 | OpenAPI 3.2 pipeline (lint, bundle, breaking-change check, generator spike D-16); Platform and Management API endpoints for B0 | Backend | T0.7, T0.9, T0.10 |
| T0.12 | `cmd/portun` all-in-one, graceful shutdown, Dockerfile, Compose stack | DevOps | T0.11 |
| T0.13 | Test harnesses: isolation and audit-coverage matrices generated from OpenAPI, reconciliation after integration suites, fuzz and benchmark harnesses | Test | T0.8, T0.11 |
| T0.14 | Integration: the "hello tenant" end-to-end scenario (create tenant → publish configuration → resolve by host → audit chain verified → zero unaudited changes → traces emitted); reviews | QA + Reviewers | all |

Parallel groups: {T0.3} → {T0.4, T0.5, T0.6, T0.7} → {T0.8} → {T0.9, T0.10,
T0.13 harness parts} → {T0.11} → {T0.12} → {T0.14}.

### 4.2 B1 tasks

| Task | Work | Owner role |
|------|------|-----------|
| T1.1 | Identity module: dynamic user schema, identifiers with verification, static and dynamic groups, lifecycle with hooks, search, device registry | Backend + Database |
| T1.2 | Credentials: Argon2id passwords with policies and breached-password check (offline corpus; online range API class X), TOTP/HOTP (RFC test vectors), recovery codes, email OTP and magic links (SMTP, Mailpit), passkeys (go-webauthn, attestation policy, metadata snapshots) | Security + Backend |
| T1.3 | Signing keys per tenant, rotation, JWKS overlap | Security |
| T1.4 | Sessions: SSO sessions (Valkey and PostgreSQL), timeouts, listing and revocation, sign-out everywhere, BFF token handler | Backend |
| T1.5 | Flow engine: graph schema, node registry, transaction state, bundle pinning, server-driven UI, default login, sign-up, recovery and step-up flows, MFA policies, remember device, guest sessions | Backend + Architect |
| T1.6 | OAuth 2.1 / OIDC provider: authorization code + PKCE, client credentials (secret, `private_key_jwt`), refresh rotation with reuse detection, discovery, JWKS, userinfo, introspection, revocation, consent, RP-initiated logout, JWT and opaque tokens, native-app rules, API registry | Backend + Security |
| T1.7 | Hosted login renderer (Lit web components) and the headless authentication API | Frontend |
| T1.8 | Management API for B1 resources; admin sign-in through the platform's own OIDC; machine clients with scopes. Admin authorization is scope-based until B2 replaces it with the authorization engine | Backend |
| T1.9 | Brute-force and credential-stuffing protection, rate limiting, new-device notifications | Security |
| T1.10 | Tests: unit and integration per module, OpenID conformance suite (or our own conformance tests if the suite image is unreachable), Playwright journeys with virtual authenticators and Mailpit, fuzzing of JOSE, WebAuthn and OAuth inputs, matrices for new endpoints, token-endpoint benchmarks | Test + QA |
| T1.11 | Integration, reviews and corrections | All |

### 4.3 Later increments

At the start of each increment from B2 on, the Principal Architect writes its
detailed task list in the same form (work, owner, dependencies, acceptance
criteria), from the features in the [requirement map](01-requirement-map.md)
and the relevant plan documents. The list is recorded in the tracker before
coding begins. A change to the approved architecture is escalated to the owner.

## 5. Agents and responsibilities

| Role | Responsibilities | How it runs |
|------|------------------|-------------|
| **Orchestrator / Principal Architect** (main agent) | Plan, contracts (interfaces, OpenAPI, schemas, ADRs), task briefs, merges and integration, re-running tests, final acceptance | Main session |
| **Backend developers** (one per active workstream) | Module implementation with tests | Sub-agents in isolated git worktrees, run in parallel |
| **Database engineer** | Schemas, migrations, RLS, queries, transaction design | Sub-agent |
| **Security engineer** | Cryptography, keys, protocol security, threat models; security review of security-critical code | Sub-agent, plus the security-review skill |
| **Frontend engineer** | Hosted login renderer, console, design system | Sub-agent |
| **SDK engineer** | SDKs and the test kit | Sub-agent |
| **DevOps / SRE engineer** | CI, containers, Compose, Helm, kind, OpenTofu | Sub-agent |
| **Test engineer** | Unit, integration, concurrency, property and fuzz tests; harnesses | Sub-agent |
| **QA engineer** | End-to-end acceptance, conformance suites | Sub-agent |
| **Code reviewers** | Independent review against requirements and architecture | Fresh sub-agents that never implemented the code under review, plus the code-review skill |
| **Performance engineer** (B12) | Load, soak, chaos | Sub-agent |
| **Technical writer** | Docs, quick-starts, API reference | Sub-agent |

**Rules:**
- Contracts (interfaces, OpenAPI, schemas) are written by the architect before
  parallel work starts.
- Each brief states the files a sub-agent owns, so parallel work does not
  collide.
- The orchestrator never merges a sub-agent's work without rebuilding and
  re-running the tests itself.
- An implementer never reviews its own work.

## 6. Review and correction cycle

1. After integration of each task group, and for the whole increment, an
   independent reviewer inspects the actual code. It covers correctness,
   architecture, quality, security, concurrency, performance, resilience,
   database use, tests and production readiness.
2. Findings are recorded with severity (critical, high, medium, low), files,
   explanation, impact and recommended correction.
3. Each finding goes to the right specialist, who implements the fix; the
   relevant tests and the full regression suite run again.
4. A fresh reviewer re-reviews the corrections.
5. Up to three cycles; anything still open after three is escalated to the owner.
6. Findings are never downgraded or dropped to pass a gate.

## 7. Quality gates

Every merge to the development branch, and every increment:

| Gate | Rule |
|------|------|
| Build | `go build ./...`, front-end and SDK builds |
| Static analysis | `go vet`, golangci-lint (zero warnings), ESLint, pin check, govulncheck, secret scan |
| Unit tests | `-race -shuffle=on -count=1` |
| Integration tests | Real PostgreSQL, Valkey and NATS in containers; reconciliation finds zero unaudited changes |
| Contract and matrices | OpenAPI validation of every API test; tenant-isolation, admin-permission and audit-coverage matrices |
| End-to-end | Playwright journeys for the increment's features |
| Fuzzing and benchmarks | Seed-corpus fuzzing; benchmark regressions above 10% fail |
| Coverage | ≥ 80% per module; ≥ 90% for security-critical modules |
| Reviews | No open critical or high findings |

## 8. Tracking and traceability

- **Live tracker:** [`TRACKER.md`](TRACKER.md) lists every task with status,
  owner role and evidence, updated as work progresses. In-session task tools
  mirror it.
- **Traceability:** every test names the features it covers (`// Covers:
  AUTH-05`). A tool builds `TRACEABILITY.md` (created with the first tests) from actual
  `go test -json` and Playwright results: requirement, implementation files,
  tests and their results, status. Only executed results are reported.
- **Increment reports:** at the end of each increment, a summary of completed
  features, test results, review findings and corrections, and remaining
  risks. Work then continues into the next increment without waiting.

## 9. Risks and limitations

| Risk | Handling |
|------|----------|
| The scope is very large | Work continues across many sessions; the repository (`CLAUDE.md`, ADRs, tracker, traceability) carries state between them, and every increment leaves a working, verified system |
| Class X features cannot be verified live | Implemented against the providers' documented APIs with contract tests; marked "live verification pending" until the owner provides credentials |
| Class T features cannot be built here | Built and verified when an environment with Xcode, the Android SDK, .NET, Unity or Unreal is attached (for example the owner's own computer); until then reported as not verified |
| No human code review (D-29) | Independent reviewer agents, security reviews, strict automated gates; external penetration tests remain a GA criterion |
| The Docker daemon is not started automatically | Each session starts it; an environment setup script can be proposed later |
| LLM-based features (design assistant, AI admin assistant) need an API key | Requested through the environment's credential form when B4 and B9 reach them |

## 10. Approval requested

Approving this plan approves:

1. The increment structure B0–B12 and the [requirement map](01-requirement-map.md),
   with detailed task lists for B0 and B1 now and for each later increment at
   its start.
2. The verification classes, including reporting class X and T features as
   "live verification pending" or "not verified" rather than complete until the
   needed access exists.
3. The Go module path `github.com/antonyrejith/portun`.
4. Scope-based admin authorization in B1, replaced by the authorization engine
   in B2.
5. Continuous execution: after approval, increments run one after another
   without asking for confirmation, with a report at the end of each.
   Escalation happens only for architecture deviations, findings still open
   after three review cycles, or genuine blockers.
