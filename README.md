# AuthNnZ — Enterprise Identity & Access Platform

> **Working name.** A fully configuration-driven ("100% dynamic"), multi-tenant
> **Authentication (AuthN) + Authorization (AuthZ)** service that can be sold as
> multi-tenant SaaS, a dedicated cloud, or self-hosted software, and that any
> product, enterprise or regulated institution can adopt without writing
> identity code.

**Status:** Planning. This repository currently contains the product and
engineering plan. Code follows once the decisions in
[`06-decisions.md`](docs/plan/06-decisions.md) are confirmed.

---

## Plan documents

**Product**

| # | Document | Answers |
|---|----------|---------|
| 1 | [Product vision & business model](docs/plan/01-product-vision.md) | Who buys it, why they switch, how we package and price it |
| 2 | [Feature catalog](docs/plan/02-feature-catalog.md) | Every capability (215), with an ID and a delivery phase |
| 3 | [Standards, compliance & industry packs](docs/plan/03-standards-and-compliance.md) | Which RFC/OpenID/OASIS/W3C standards, certifications and regulations we cover, and the vertical bundles |
| 7 | [Authentication & authorization coverage](docs/plan/07-access-coverage.md) | Every authentication type and every authorization level (user, role, group, organization, resource, attribute, field, row, …) |

**Architecture & engineering**

| # | Document | Answers |
|---|----------|---------|
| 4 | [Architecture](docs/plan/04-architecture.md) | How "100% dynamic" works, system design, data model, tech stack, SLOs, security |
| 8 | [Engineering standards](docs/plan/08-engineering-standards.md) | Latest-version policy, repository and module structure, Go and TypeScript coding standards, reviews |
| 9 | [Testing strategy](docs/plan/09-testing-strategy.md) | Unit, integration, contract, API, security, conformance, E2E, fuzz, performance and chaos testing, with examples and gates |
| 10 | [API standards](docs/plan/10-api-standards.md) | OpenAPI 3.2 contracts, conventions, errors, versioning, toolchain, developer portal |
| 11 | [User interfaces & configuration screens](docs/plan/11-admin-ui.md) | Every screen in the admin console, organization portal, account portal and hosted login |
| 12 | [Containers & deployment](docs/plan/12-containers-and-deployment.md) | Images, local development, Kubernetes, release pipeline, environments, self-hosted packaging |

**Delivery**

| # | Document | Answers |
|---|----------|---------|
| 5 | [Roadmap & delivery](docs/plan/05-roadmap-and-delivery.md) | Phases, milestones, exit criteria, team, risks |
| 6 | [Decisions & ADR backlog](docs/plan/06-decisions.md) | What must be decided now, with a recommendation for each |

---

## The plan at a glance

**One platform, five markets:** consumer identity (CIAM), B2B SaaS
(organizations, SSO, SCIM), workforce SSO, fine-grained authorization, and
machine, workload and AI-agent identity.

**What "100% dynamic" means:** we build engines, not hard-coded features. Every behavior
(login journeys, user schema, MFA rules, token claims, authorization models,
policies, branding, connectors, risk rules) is **tenant-scoped, versioned
configuration**. Customers change it at runtime through the console, the API,
Terraform or GitOps, with draft → simulate → publish → roll back, and no deploy
on either side.

**Why customers pick it over Auth0, Okta, Cognito or Keycloak:**
1. AuthN and fine-grained AuthZ in one model, one SDK and one audit trail.
2. True runtime configurability with environments, promotion, simulation and rollback.
3. Deploy anywhere: SaaS, dedicated cell, customer VPC, air-gapped; edge PDP for sub-millisecond checks.
4. Standards-first with no lock-in (OIDC, SAML, SCIM, AuthZEN, Shared Signals) and full data export, password hashes included.
5. Pre-built, pre-certified industry packs (FAPI 2.0 and open banking, SMART on FHIR, NIST AAL3, PIV/CAC, telecom).
6. Native support for AI agents: an MCP-ready authorization server, delegated agent identities and human-in-the-loop approvals.

**How it is built:**
- Go (latest stable, 1.27 today) for every backend service; React and TypeScript for the UIs. Every
  dependency is on its latest stable version, pinned and kept current automatically.
- A modular monolith with compiler-enforced module boundaries and clean
  architecture inside each module; strict lint and review rules.
- Tests at every level, from table-driven unit tests to conformance suites,
  fuzzing, load and chaos tests, with tenant-isolation checks on every endpoint.
- OpenAPI 3.2 contracts for every API; the console, CLI, Terraform and SDKs all
  use the same public APIs.
- Signed, minimal container images; Helm on Kubernetes with GitOps and canary
  releases; the same images for SaaS, customer cloud and air-gapped installs.

**Delivery (indicative; team of about 14 growing to about 36; weeks from kickoff):**

| Phase | Weeks | Outcome |
|-------|-------|---------|
| 0. Foundations | 0–6 | Decisions, skeleton, CI/CD, threat model, core libraries |
| 1. Core AuthN MVP | 6–22 | OIDC/OAuth 2.1 provider, passwords, passkeys, MFA, social login, RBAC, console, hosted login. **Private beta** |
| 2. Enterprise & B2B | 20–36 | Organizations, SAML, SCIM, LDAP/AD, flow designer, Actions, MCP auth. **Public GA** |
| 3. Fine-grained AuthZ | 24–40 | ReBAC + ABAC + Cedar policies, AuthZEN API, simulation, edge PDP |
| 4. Security & governance | 36–56 | Risk engine, adaptive MFA, Shared Signals, access reviews, SOC 2 Type II, ISO 27001 |
| 5. Regulated & global | 52–78 | FAPI 2.0, CIBA, open banking, healthcare, government packs, multi-region, self-hosted edition |
| 6. Expansion | 78+ | Verifiable credentials and wallets, OpenID Federation, post-quantum readiness, advanced IGA |

**Next step:** confirm decisions D-01 to D-08 in
[`06-decisions.md`](docs/plan/06-decisions.md), then start Phase 0.
