# 01 — Product Vision & Business Model

## 1. Problem

Every software company rebuilds the same identity stack: sign-up, login, MFA,
password reset, roles, permissions, SSO for enterprise customers, user
provisioning, audit logs. Then the requirements grow:

- **Enterprise buyers** require SAML/OIDC SSO, SCIM provisioning, audit
  streaming to their SIEM, and admin delegation before they will sign.
- **Regulated industries** require FAPI 2.0 (banking), SMART on FHIR
  (healthcare), NIST 800-63 assurance levels and PIV/CAC (government),
  PSD2 strong customer authentication, and certified implementations.
- **Authorization** outgrows `if user.role == "admin"`. Products need sharing,
  hierarchies, per-tenant custom roles and attribute conditions (Google
  Drive-style sharing, GitHub-style org/team/repo permissions).
- **AI agents** now act on behalf of users. They need their own identity,
  delegated and scoped tokens, and human approval for risky actions.

Existing options force trade-offs. CIAM vendors are strong on login but treat
fine-grained authorization as a separate product or leave it out. Enterprise
IAM suites are heavy and expensive. Open-source servers leave hosting,
scaling, compliance and SLAs to the customer. Authorization-only vendors still
need a separate identity provider.

## 2. Vision

> **A single identity and access platform that any organization can configure,
> not code, to authenticate any principal (human, organization, workload or AI
> agent) and authorize any action on any resource, to the standards of any
> industry, deployed wherever they need it.**

## 3. What "100% dynamic" means

Customers can configure everything below at runtime through the console,
the Management API, Terraform or GitOps. No code change and no deploy on our
side or theirs. Every change is versioned, can be tested before publishing, and
can be rolled back.

| Dimension | Examples of what tenants configure |
|-----------|------------------------------------|
| Tenancy | Environments (dev/stage/prod), organizations, sub-organizations, child tenants (resellers) |
| Identity schema | Custom user, organization and device attributes with types, validation, PII flags, uniqueness and visibility |
| Login journeys | Any sequence of identify → authenticate → verify → risk → consent → enrich, with branching on conditions |
| Authentication methods | Which factors, in what order, for which app, organization, risk level or user segment |
| Policies | Password, MFA, session, lockout, IP and geo restrictions, token lifetimes, inherited platform → tenant → org → app |
| Tokens | Claims, scopes, audiences, formats (JWT or opaque), signing algorithms, lifetimes, mapping expressions |
| Authorization | Resource types, relations, roles, permissions, attribute conditions, guardrail policies |
| Federation | IdP and SP connections, attribute mappings, home-realm discovery, JIT provisioning rules |
| Extensibility | Custom code (sandboxed) at hook points, webhooks, event streams, connectors |
| Experience | Branding, themes, custom domains, email and SMS templates, 40+ locales, per-organization overrides |
| Security | Risk rules, bot protection, rate limits, allow/deny lists, breached-password policy |
| Governance | Audit retention, log streaming, consent texts and versions, access review campaigns |

The platform supports **progressive complexity**: a startup clicks "Enable
Google login + RBAC" and is live in 10 minutes. A bank edits a FAPI 2.0 journey
graph with CIBA, transaction signing and Cedar guardrail policies. Both use the
same engines.

## 4. Target segments

| Segment | Buys for | Must-have features |
|---------|----------|--------------------|
| **B2C / CIAM** (retail, media, gaming, consumer fintech) | Conversion, security, scale | Social and passwordless login, passkeys, progressive profiling, bot protection, branding |
| **B2B SaaS vendors** | Close enterprise deals faster | Organizations, per-org SSO and SCIM, self-service admin portal, RBAC, audit logs |
| **Enterprises** (internal and partner apps) | Consolidation, Zero Trust | SAML/OIDC SSO, AD/LDAP, MFA, access reviews, SIEM integration |
| **Regulated industries** (banking, insurance, health, government, telecom) | Compliance | FAPI 2.0, CIBA, PSD2 SCA, SMART on FHIR, NIST AAL3, PIV/CAC, certified profiles |
| **Platform, API and AI-agent builders** | Secure APIs and agents | M2M, API keys, token exchange, MCP authorization, fine-grained authorization |
| **ISVs and resellers (OEM)** | Resell identity inside their product | Multi-level tenancy, white-labeling, delegated billing |

### Personas

- **Developer.** Integrates SDKs and wants quick-starts, clear APIs and a local emulator.
- **Security / IAM architect.** Designs policies, wants standards compliance, audit and control.
- **Tenant admin.** Manages apps, users and branding in the console.
- **Organization admin** (our customer's customer). Sets up their company's SSO and SCIM through the self-service portal.
- **End user.** Signs in, manages their security settings, devices and consents.
- **Compliance officer.** Needs evidence, access reviews and reports.
- **Support agent.** Helps users through scoped, audited tools.

## 5. Competitive landscape

| Category | Examples | Typical gaps we target |
|----------|----------|------------------------|
| CIAM SaaS | Auth0 (Okta Customer Identity), Microsoft Entra External ID, AWS Cognito, Google Identity Platform / Firebase Auth | Fine-grained authorization is separate or missing; mostly SaaS-only; price climbs steeply at scale; limited journey customization in some offerings |
| Developer-first auth | Clerk, Stytch, Descope, FusionAuth, Frontegg, SuperTokens | Lighter on regulated profiles (FAPI, SMART), governance and on-prem options (varies by vendor) |
| B2B enablement | WorkOS | Focused on SSO and SCIM for SaaS; not a full CIAM or authorization platform |
| Enterprise IAM suites | Okta Workforce, Ping Identity (incl. ForgeRock), Microsoft Entra ID | Heavy, expensive, slow to configure; less suited to embedding in products |
| Open source | Keycloak, Ory, Zitadel, Authentik | Customer carries hosting, scaling, compliance and on-call; enterprise features or SLAs often paid extras |
| Authorization only | AuthZed (SpiceDB), Okta FGA / OpenFGA, Permit.io, Cerbos, Oso, Aserto/Topaz | Need a separate identity provider; two audit trails and two SDKs |

Validate this table with a detailed competitor feature and pricing teardown in
the foundations stage (task PM-02 in the delivery plan). Vendors change offerings quickly.

## 6. Differentiators

1. **Unified AuthN + AuthZ.** One identity graph, one model for roles,
   relationships and attributes, one decision log, one SDK. Login context
   (assurance level, risk, device) flows straight into authorization decisions.
2. **Truly dynamic.** Versioned configuration with environments, promotion,
   diff, simulation ("what would change if I publish this?"), staged rollout
   and one-click rollback.
3. **Deploy anywhere.** Multi-tenant SaaS, dedicated single-tenant cell,
   customer VPC (bring your own cloud), on-prem and air-gapped. A hybrid edge
   PDP gives sub-millisecond checks next to the customer's services.
4. **Standards-first, no lock-in.** OIDC, SAML, SCIM, AuthZEN, Shared Signals
   and Standard Webhooks. Full export, including password hashes, signing keys
   (where policy allows) and authorization data.
5. **Industry packs.** Pre-built, pre-certified bundles of journeys, policies,
   claims and reports for banking, healthcare, government, telecom, education
   and more (see [03](03-standards-and-compliance.md#5-industry-packs)).
6. **Built for AI agents.** MCP-compatible authorization server, first-class
   agent principals, on-behalf-of delegation with `act` claims, time-boxed
   scopes, and human-in-the-loop approval through CIBA or push.
7. **Multi-level tenancy.** ISVs can resell white-labeled identity to their own
   customers, with delegated administration and usage roll-up.
8. **Provable 100% audit.** Every change, login, decision and staff action is
   recorded with who, what, when, where, how and why, and customers can verify
   cryptographically that nothing is missing or altered
   (see [13](13-audit-and-change-tracking.md)).
9. **Beautiful and brandable without code.** Every screen, email and portal
   can be made to match the customer's brand with point-and-click tools, live
   previews, themes and effects such as glass; web and native apps update without a release
   (see [16](16-look-and-feel.md)).
10. **Migration without pain.** Lazy migration plus importers for the major
   vendors, with password-hash compatibility, so users never reset passwords.

## 7. Packaging and pricing (draft)

### Editions

| Edition | For | Includes |
|---------|-----|----------|
| **Developer** (free) | Prototypes, small apps | Core login, social, passkeys, MFA, RBAC, hosted UI, capped MAU, community support |
| **Pro** | Startups, B2C apps | Custom domain, branding, Actions, webhooks, more MAU, email support |
| **Business** | B2B SaaS | Organizations, enterprise SSO and SCIM connections, admin portal, fine-grained authorization, log streaming |
| **Enterprise** | Large companies | Advanced security (risk, adaptive MFA), governance, 99.99% SLA, data residency, dedicated cell option, premium support |
| **Regulated** | Banks, health, government | Enterprise + industry packs, HSM keys, FAPI/CIBA, compliance reporting, certified configurations |
| **Self-hosted** | Air-gapped, sovereign | Enterprise or Regulated feature set; annual subscription by identities under management, with support and updates |

### Metering dimensions

- Monthly active users (B2C)
- Monthly active organizations, and enterprise SSO/SCIM connections (B2B)
- Machine-to-machine token issuances
- Authorization decisions (per million)
- Active AI-agent identities
- Pass-through costs: SMS, voice, WhatsApp
- Add-ons: advanced security, governance, industry packs, data residency
  regions, dedicated cell, HSM, premium SLA and support

Set price points after market research during the foundations stage. Usage
metering is built in from the start, so any pricing model can be applied; this is a dynamic,
configurable rate plan in itself.

The detailed edition-by-feature table, pricing principles, routes to market and
commercial readiness are in [22 — Commercialization](22-commercialization.md).

## 8. Go-to-market notes

- **Developer-led acquisition:** free tier, excellent docs, quick-starts,
  a local emulator, open-source SDKs.
- **Switching accelerators:** migration tooling for Auth0, Cognito, Firebase,
  Keycloak, Okta and Azure AD B2C. Lazy migration means no forced password reset.
- **Trust signals as sales enablers:** SOC 2 Type II, ISO 27001/27017/27018/27701,
  OpenID Certified, FIDO Certified, FAPI certification, HIPAA BAA, and a public
  status page.
- **Channels:** AWS, Azure and GCP marketplaces; system integrator and MSP
  partner program; OEM program for ISVs.

## 9. Success metrics

| Metric | Target |
|--------|--------|
| Time from sign-up to first successful login in a sample app | < 10 minutes |
| Data plane availability | ≥ 99.99% monthly |
| Login p99 latency (in-region, excluding external IdPs) | < 300 ms end-to-end server time |
| Authorization check p99 (central / edge) | < 10 ms / < 1 ms |
| Free → paid conversion | Set from the design-partner baseline |
| Net revenue retention | ≥ 120% (expansion through MAU, orgs, add-ons) |
| Security | Zero critical vulnerabilities open longer than 7 days |
