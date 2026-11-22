# 06 — Decisions & ADR Backlog

## 1. Decision register

Decisions recorded on 21 November 2026 with the product owner. Each decision is
**accepted**, **accepted with a foundations spike** (the direction is set and a
short experiment picks the exact option, recorded in an ADR), or **deferred**
until the stated point.

### 1.1 Accepted

| ID | Decision | Choice | Consequences |
|----|----------|--------|--------------|
| D-01 | Product name | **Portun**, for both the code and the market (from Portunus, the Roman god of keys and doors) | Packages, CLI (`portunctl`), images and URLs use `portun`. Domains shown in the plan (`portun.io`) are placeholders until a domain is secured; domain and trademark checks are owner tasks ([25 §1](25-business-setup-guide.md#1-name-and-brand-protection)) |
| D-02 | Sales and marketing focus at launch | **All segments equally** | Industry packs, demos and content are needed for every segment at GA |
| D-03 | Backend language | **Go, pinned to an exact release (1.27.4 at start)** | — |
| D-06 | Deployment models at GA | **All:** shared SaaS, dedicated cells, customer cloud, self-hosted and air-gapped | Follows from D-23 |
| D-07 | Cloud providers | **AWS, Azure and Google Cloud from day one** | Each cloud needs its own managed database, cache, key management, marketplace listing and test runs before GA ([12 §6](12-containers-and-deployment.md#6-cloud-infrastructure-saas)) |
| D-08 | Licensing | **Fully proprietary, SDKs included**; free self-hosted developer edition under a proprietary license | SDKs, connectors and the Terraform provider are published under a proprietary license. Public directories that require open-source code (for example the WordPress plugin directory) are replaced by our own download site and registry ([23 §7](23-sdks.md#7-distribution-versions-and-trust)) |
| D-09 | Repository strategy | **Monorepo** for services and web apps; separate repositories per published SDK | — |
| D-10 | Extension runtime | **WebAssembly (wazero)** | — |
| D-12 | Event bus | **NATS JetStream** internally; Kafka and cloud streams as customer sinks | — |
| D-13 | Compliance automation tool | **Chosen during foundations** by price and auditor network | Owner task ([25 §5](25-business-setup-guide.md#5-compliance-program)) |
| D-15 | Version policy | **Exact pinned versions everywhere, including the dependencies our SDKs declare**; nothing upgrades automatically; every upgrade is an intentional, reviewed pull request | SDKs keep runtime dependencies to a minimum ([08 §1](08-engineering-standards.md#1-version-policy-pinned-everywhere-upgrades-on-purpose)) |
| D-17 | SDK generation | **Generated core + hand-written idiomatic layer** | — |
| D-18 | Front-end stack | **React + TypeScript + TanStack + Radix UI + Tailwind CSS; Lit for the hosted login** | — |
| D-20 | Container base image | **Distroless static (Debian 13, nonroot)** | — |
| D-21 | Audit | **100% audit with no opt-outs**: every change, login, decision and staff action is recorded; strict mode is always on | No tenant can turn off any part of the audit trail ([13](13-audit-and-change-tracking.md)) |
| D-22 | Database change capture | **Our own Go reader on PostgreSQL logical replication** | — |
| D-23 | Release strategy | **One release: all 298 features at GA** | Built in dependency order with integration checkpoints ([05](05-roadmap-and-delivery.md)) |
| D-24 | Scope boundaries | **Add** the virtual waiting room (SEC-17) and Secure Payment Confirmation (AUTH-32); **leave out** the identity-aware proxy, password vaulting and desktop login | See the [feature catalog](02-feature-catalog.md#scope-boundaries) |
| D-27 | Partners and services | **Partner-led implementation**, with a small in-house team for migrations and key accounts | — |
| D-28 | Implementation model | **Claude implements the platform** once the plan is complete, workstream by workstream in dependency order | Team sizes and the calendar timeline no longer apply; the timeline is re-estimated after foundations from measured progress ([05 §3](05-roadmap-and-delivery.md#3-implementation-model)) |
| D-29 | Code review | **Claude reviews only:** every pull request gets an independent review pass, with security-focused reviews on security-critical code; no human approval is required | Mitigated by automated security gates and external penetration tests before GA; recorded as risk R9 ([08 §5](08-engineering-standards.md#5-git-workflow-reviews-and-releases)) |
| D-31 | Company-side work | **The owner leads it, with guidance** from the business setup guide | [25 — Business setup guide](25-business-setup-guide.md) |
| D-32 | Starting mode for new organizations | **Chosen when each organization is created**, with the parent's configurable default pre-selected (Parent out of the box), and changeable at any time | [15 §2.2](15-hierarchy-and-shared-settings.md#22-before-any-change-the-starting-state) |

### 1.2 Accepted with a foundations spike

| ID | Decision | Direction | Spike decides |
|----|----------|-----------|---------------|
| D-04 | Protocol layer | **Own implementation on vetted primitives**, using existing frameworks as references | Which primitive libraries pass the hardening review (JOSE, SAML/XML signature) |
| D-05 | Authorization engine | **Embeddable Zanzibar engine behind our own model language and interface, plus `cedar-go` for guardrails** | OpenFGA, SpiceDB or our own implementation |
| D-11 | Global data strategy | **Region-homed PostgreSQL per cell; distributed SQL for tenants needing global active-active** | CockroachDB or YugabyteDB |
| D-16 | OpenAPI → Go server generator | **Generated, strictly typed interfaces with request validation** | ogen or oapi-codegen, by OpenAPI 3.1/3.2 support |
| D-19 | Service-to-service mTLS | **mTLS between all services inside a cell** | Service mesh, SPIFFE/SPIRE certificates or application-level mTLS |

### 1.3 Deferred

| ID | Decision | Decide by | Notes |
|----|----------|-----------|-------|
| D-14 | Budget | Before cloud accounts are opened | With Claude implementing, the main costs are cloud infrastructure, third-party services (auditors, penetration tests, translation, certifications) and any people hired for operations and sales ([25](25-business-setup-guide.md)) |
| D-25 | Pricing and packaging | Before the release candidate | The edition table in [22](22-commercialization.md) is the starting proposal; packaging is configuration, so it can change without engineering |
| D-26 | Go-to-market team | Before hardening | Options: start lean, a full team, or partners and marketplaces only ([22 §8](22-commercialization.md#8-go-to-market-team-and-timeline)) |
| D-30 | Production operations (24/7 on-call, incidents, support) | Before hardening | Options: a small human team assisted by Claude, a managed-service partner, or a mix. Someone accountable must be on call at GA ([18 §1](18-operating-the-service.md#1-operating-model-at-ga)) |

## 2. ADR backlog

Architecture Decision Records live in `docs/adr/NNNN-title.md`. Write these
during the foundations stage, in this order:

| ADR | Title | Depends on |
|-----|-------|-----------|
| 0001 | Modular monolith and deployable composition | D-03 |
| 0002 | Tenancy model, tenant resolution and isolation tiers | — |
| 0003 | Versioned configuration, inheritance and runtime bundles | 0002 |
| 0004 | Flow engine and server-driven UI contract | 0003 |
| 0005 | Unified authorization model (relationships, CEL conditions, Cedar guardrails) | D-05 |
| 0006 | Key management hierarchy and signing-key rotation | D-07 |
| 0007 | Event bus, transactional outbox and delivery guarantees | D-12 |
| 0008 | Audit architecture: completeness guarantees, record schema, hash chain and checkpoints, retention | 0007, D-21 |
| 0009 | API conventions (versioning, pagination, errors, idempotency, IDs) | — |
| 0010 | Extension sandbox and hook contract | D-10 |
| 0011 | Session model and logout propagation | 0004 |
| 0012 | Credential storage and password-hash migration | 0006 |
| 0013 | Cell architecture and global routing | 0002 |
| 0014 | Database migration strategy (expand, migrate, contract) | — |
| 0015 | SDK architecture, code generation and cross-SDK conformance ([23](23-sdks.md)) | 0009 |
| 0016 | Code structure, module boundaries and dependency rules | 0001 |
| 0017 | Testing strategy, test infrastructure and quality gates | 0016 |
| 0018 | OpenAPI toolchain and code generation | D-16, 0009 |
| 0019 | Front-end architecture and design system | D-18 |
| 0020 | Container images, Helm chart and release pipeline | D-20 |
| 0021 | Database change capture and audit reconciliation | D-22, 0008 |
| 0022 | Hierarchical configuration: override policies, merge rules, shared references, templates and push | 0003 |
| 0023 | Tenant moves between regions, cells and customer clouds | 0013 |
| 0024 | AI assistant guardrails: drafts only, permission scoping, audit, opt-out | 0005, D-21 |

## 3. ADR template

```markdown
# ADR-NNNN: <title>

- Status: proposed | accepted | superseded by ADR-XXXX
- Date: YYYY-MM-DD
- Deciders: <names or roles>

## Context
What problem are we solving? What constraints apply (standards, performance,
compliance, deployment models)?

## Options considered
1. Option A: pros, cons
2. Option B: pros, cons

## Decision
What we chose and why.

## Consequences
What becomes easier, what becomes harder, follow-up work, and how we would
reverse this decision if needed.
```
