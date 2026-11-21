# 06 — Decisions & ADR Backlog

## 1. Decisions needed during the foundations stage

Each decision has a recommendation. Decisions D-01 to D-08 block kickoff;
the rest during the foundations stage (weeks 0–8).

**Accepted so far:** D-03 (Go, pinned exact release), D-15 (exact pinned
versions everywhere, explicit upgrades only), D-21 (100% audit) and D-23 (one
release with every feature).

| ID | Decision | Options | Recommendation | Why |
|----|----------|---------|----------------|-----|
| D-01 | Product name and brand | Keep "AuthNnZ" or rename | Pick a market name during foundations (trademark and domain check); keep AuthNnZ as the code name | The name appears in domains, SDK package names and token issuers, so change it before SDKs ship |
| D-02 | Go-to-market focus at launch | B2B SaaS · B2C CIAM · regulated industries · enterprise workforce | **Every segment is supported at GA (D-23).** Sales and marketing lead with B2B SaaS and developer-led CIAM; regulated industries are sold with the industry packs and partners | The product scope is complete; the decision is where sales effort goes first |
| D-03 | Backend language | Go · Java/Kotlin · Rust | **✅ Accepted: Go, pinned to an exact release (1.27.4 at start)** | Static binaries for edge and on-prem, strong identity ecosystem, native FIPS 140-3 crypto module, hiring pool |
| D-04 | Protocol layer: build or adopt | Own implementation on vetted primitives · fosite · zitadel/oidc · Spring Authorization Server · fork Keycloak | **Own implementation on vetted primitives**, using existing frameworks as references | The authorization server is the core product: it must be fully dynamic, multi-tenant and FAPI-ready. Forking a large server brings its architecture and limits with it |
| D-05 | Authorization engine | Embed OpenFGA · embed SpiceDB · own Zanzibar; Cedar · OPA | **Decide by a foundations spike.** Start from an embeddable Apache-2.0 Zanzibar engine behind our own DSL and interface, plus `cedar-go` for guardrails | Proven semantics now, freedom to replace later |
| D-06 | Deployment models at GA | SaaS only · some models later · all at GA | **All at GA:** multi-tenant SaaS, dedicated cells, customer VPC, self-hosted and air-gapped, from one codebase (the all-in-one binary exists from the first weeks) | Customers in every segment need their model on day one; one codebase keeps the cost of each extra model low |
| D-07 | Primary cloud | AWS · Azure · GCP | **AWS first**, cloud-agnostic through Kubernetes and abstractions (KMS, storage, bus) | Largest marketplace, CloudHSM and KMS maturity. Add Azure next for Microsoft-centric enterprises |
| D-08 | Licensing model | Proprietary · open core · source-available | **Proprietary core, open-source SDKs, connectors and Terraform provider**, free self-hosted developer edition | Protects the business while keeping the developer funnel open |
| D-09 | Repository strategy | Monorepo · polyrepo | **Monorepo for services and web apps; separate repositories per public SDK** | Atomic changes across modules; SDKs need their own release cadence and package registries |
| D-10 | Extension runtime | WASM (wazero) · V8 isolates · containers or microVMs | **WASM (wazero)** | Pure Go, strong isolation, millisecond start, multi-language |
| D-11 | Global data strategy | Region-homed PostgreSQL · distributed SQL | **Region-homed PostgreSQL per cell** for data residency; **distributed SQL (CockroachDB or YugabyteDB, PostgreSQL-compatible)** for tenants that need global active-active; chosen by a foundations spike | Residency and simplicity for most tenants; global active-active available at GA; SQL kept portable |
| D-12 | Event bus | NATS JetStream · Kafka | **NATS JetStream internally**, Kafka and cloud streams as customer sinks | Light enough for edge and on-prem |
| D-13 | Compliance automation | Vanta · Drata · Secureframe · others | Choose during foundations by price and auditor network | Cuts SOC 2 and ISO effort substantially |
| D-14 | Budget, team and timeline | — | Confirm the team plan in [05 §3](05-roadmap-and-delivery.md#3-workstreams-and-team) | Everything else depends on capacity |
| D-15 | Version policy | Floating or automatically updated versions · exact pins with explicit upgrades | **✅ Accepted: exact pinned versions everywhere**, starting from the latest stable release; nothing upgrades automatically; every upgrade is an intentional, reviewed pull request ([08 §1](08-engineering-standards.md#1-version-policy-pinned-everywhere-upgrades-on-purpose)) | Builds and deployments are reproducible, and every change of version is deliberate, reviewed and audited |
| D-16 | OpenAPI → Go server generator | ogen · oapi-codegen · hand-written | **Decide by a foundations spike**: strict typed interfaces, request validation, OpenAPI 3.1/3.2 support | Generated interfaces keep code and spec identical |
| D-17 | SDK generation | OpenAPI Generator · commercial generators · hand-written | **Generated core + hand-written idiomatic layer**; evaluate generators during foundations on output quality per language | SDK quality decides developer adoption |
| D-18 | Front-end stack | React · Vue · Svelte; component library | **React + TypeScript + TanStack + Radix UI + Tailwind CSS; Lit for the hosted login** ([11 §8](11-admin-ui.md#8-front-end-technology)) | Largest ecosystem and hiring pool; Lit keeps the login page small and embeddable anywhere |
| D-19 | Service-to-service mTLS | Service mesh (Istio ambient, Linkerd) · SPIFFE/SPIRE-issued certificates · application-level mTLS | Decide during foundations with spike measurements | Needed for Zero Trust inside cells; adds operational weight |
| D-20 | Container base image | Distroless static · Chainguard/Wolfi · `scratch` | **Distroless static (Debian 13, nonroot)** | No shell or package manager, includes CA certificates and time zones, free and widely scanned |
| D-21 | Audit strictness | Best effort · 100% with fail-closed capture | **✅ Accepted: 100% audit**; changes are rejected if they cannot be audited; decision and login events use durable spools with strict mode ([13](13-audit-and-change-tracking.md)) | Customers and auditors need provable completeness |
| D-22 | Database change capture | Own reader on PostgreSQL logical replication · Debezium · triggers | **Own Go reader on logical replication**, running in the `worker` | No extra infrastructure (no Kafka Connect); works the same in SaaS and self-hosted |
| D-23 | Release strategy | Phased releases · one release with every feature | **✅ Accepted: one release; all 252 features at GA**, built in parallel by 13 workstreams with integration checkpoints every 6 weeks ([05](05-roadmap-and-delivery.md)) | Customers get the complete platform at launch; nothing is deferred |

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
| 0015 | SDK architecture and code generation | 0009 |
| 0016 | Code structure, module boundaries and dependency rules | 0001 |
| 0017 | Testing strategy, test infrastructure and quality gates | 0016 |
| 0018 | OpenAPI toolchain and code generation | D-16, 0009 |
| 0019 | Front-end architecture and design system | D-18 |
| 0020 | Container images, Helm chart and release pipeline | D-20 |
| 0021 | Database change capture and audit reconciliation | D-22, 0008 |
| 0022 | Hierarchical configuration: override policies, merge rules, shared references, templates and push | 0003 |

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
