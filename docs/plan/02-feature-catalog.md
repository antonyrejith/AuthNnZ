# 02 — Feature Catalog

The complete capability list. Every feature has a stable **ID** so it can become
an epic in the issue tracker, and a **Phase** that matches the
[roadmap](05-roadmap-and-delivery.md):

| Phase | Name |
|-------|------|
| 1 | Core AuthN MVP (private beta) |
| 2 | Enterprise & B2B (public GA) |
| 3 | Fine-grained authorization |
| 4 | Security, governance & compliance |
| 5 | Regulated industries & global scale |
| 6 | Expansion |

A phase like "1→2" means a basic version ships in the first phase and the full
version in the second.

Standards references are listed in [03](03-standards-and-compliance.md).

---

## A. Tenancy & platform (`TEN`)

| ID | Capability | Details | Phase |
|----|-----------|---------|-------|
| TEN-01 | Tenant hierarchy | Platform → Tenant → Environment (dev/stage/prod) → Organization → Sub-organization | 1 |
| TEN-02 | Multi-level tenancy | Tenants create and manage child tenants; white-label; usage roll-up for resellers and ISVs | 4 |
| TEN-03 | Isolation tiers | Pooled (row-level security), database-per-tenant, dedicated cell, customer VPC | 1→5 |
| TEN-04 | Custom domains | `login.customer.com` with automatic TLS (ACME), per tenant, environment or organization | 2 |
| TEN-05 | Data residency | Tenant pinned to a region (US, EU, UK, IN, AU, …); PII never leaves its region | 5 |
| TEN-06 | Configuration inheritance | Platform defaults → tenant → environment → organization → application overrides | 1 |
| TEN-07 | Environment promotion | Diff, promote dev → stage → prod, roll back | 2 |
| TEN-08 | Entitlements & quotas | Plan-based feature gating, quotas and rate limits per tenant, all configurable | 1 |
| TEN-09 | Tenant lifecycle | Provisioning API, suspend, export, delete with crypto-shredding | 1→4 |
| TEN-10 | Self-service sign-up | Create tenant, choose region and plan, upgrade in-app | 2 |

## B. Identity & directory (`IDM`)

| ID | Capability | Details | Phase |
|----|-----------|---------|-------|
| IDM-01 | Dynamic user schema | Per-tenant custom attributes (JSON Schema): types, validation, PII flag, uniqueness, read/write visibility per audience | 1 |
| IDM-02 | Multiple identifiers | Email, phone, username, custom IDs; verified state per identifier; identifier-first login | 1 |
| IDM-03 | Groups | Static groups (1); dynamic rule-based membership with CEL (2) | 1→2 |
| IDM-04 | Account linking & merging | Automatic by verified email (policy-controlled), user-initiated, or admin merge | 2 |
| IDM-05 | Lifecycle states | Staged, invited, active, locked, suspended, deprovisioned; hooks on each transition | 1 |
| IDM-06 | Progressive profiling | Ask for attributes over time based on conditions | 2 |
| IDM-07 | Bulk import / export | CSV/JSON async jobs; password hashes: bcrypt, scrypt, Firebase scrypt, argon2, PBKDF2, salted SHA/MD5 (legacy) with re-hash on login | 1→2 |
| IDM-08 | Lazy migration | Authenticate against a legacy store on first login, then migrate | 2 |
| IDM-09 | SCIM 2.0 server | Inbound provisioning of users and groups from enterprise directories | 2 |
| IDM-10 | SCIM client | Outbound provisioning to downstream applications | 4 |
| IDM-11 | Directory sync | LDAP / Active Directory (through an on-prem agent), Google Workspace, Microsoft Entra ID | 2 |
| IDM-12 | Search | Indexed attribute queries, filters, pagination, saved segments | 1 |
| IDM-13 | Delegated administration | Admin roles scoped to an organization, group or attribute segment | 2 |
| IDM-14 | Device registry | Trusted devices, device attributes and posture | 2 |
| IDM-15 | Identity proofing (KYC) | Pluggable document + selfie and eKYC providers; NIST IAL2 flows; OpenID Identity Assurance claims | 5 |
| IDM-16 | Support tools | User timeline, impersonation with consent and audit, guided recovery | 4 |
| IDM-17 | Guardian relationships | Family, household, parent/child with parental consent and age gates | 5 |

## C. Authentication methods (`AUTH`)

| ID | Capability | Details | Phase |
|----|-----------|---------|-------|
| AUTH-01 | Passwords | NIST SP 800-63B policies; configurable complexity, history, expiry; Argon2id hashing; KMS-held pepper | 1 |
| AUTH-02 | Breached-password detection | k-anonymity range lookups; local corpus option for air-gapped installs | 2 |
| AUTH-03 | Email magic link and OTP | Configurable length, TTL, attempts; same-device and cross-device | 1 |
| AUTH-04 | SMS, voice and WhatsApp OTP | Pluggable providers with failover; SMS-pumping protection; per-tenant spend caps | 2 |
| AUTH-05 | Passkeys / WebAuthn | Platform and roaming authenticators, synced and device-bound, conditional UI (autofill), attestation policy, FIDO MDS3 | 1 |
| AUTH-06 | TOTP / HOTP | Authenticator apps | 1 |
| AUTH-07 | Push MFA | Our own authenticator app and mobile SDK; number matching; biometric confirmation | 4 |
| AUTH-08 | Recovery | Recovery codes; configurable recovery journeys; admin-assisted recovery | 1 |
| AUTH-09 | Social login | Google, Apple, Microsoft, GitHub, plus a generic OAuth2/OIDC connector (1); Facebook, LinkedIn, X, Amazon, Slack, Discord and more (2) | 1→2 |
| AUTH-10 | Certificates and smart cards | X.509 client certificates, PIV/CAC, mutual TLS | 5 |
| AUTH-11 | Kerberos / SPNEGO | Integrated Windows Authentication | 5 |
| AUTH-12 | Device authorization grant | TVs, consoles, CLIs, IoT | 2 |
| AUTH-13 | QR / cross-device login | Approve a desktop login from an authenticated phone | 4 |
| AUTH-14 | CIBA | Decoupled authentication (poll, ping, push) for call centers, banking, agent approvals | 5 |
| AUTH-15 | SIM-based verification | Silent network authentication and number verification (GSMA Open Gateway / CAMARA), SIM-swap signal | 5 |
| AUTH-16 | Verifiable credentials | OpenID4VP presentation of SD-JWT VC and mDL (ISO 18013-5/-7); EU Digital Identity Wallet | 6 |
| AUTH-17 | Step-up authentication | `acr`/`amr` requests and enforcement; RFC 9470 challenges from APIs | 2 |
| AUTH-18 | Adaptive / risk-based MFA | Policies over risk score, device, location, network, behavior | 4 |
| AUTH-19 | Remember device | Trusted-device windows per policy | 2 |
| AUTH-20 | MFA policies | Required factors per app, organization or risk; enrollment grace periods; phishing-resistant-only mode | 2 |
| AUTH-21 | Transaction signing | PSD2 dynamic linking: amount and payee bound to the approval | 5 |
| AUTH-22 | Custom authenticators | Third-party factors through the extension SDK | 4 |
| AUTH-23 | Guest sessions | Anonymous sessions that upgrade to full accounts (carts, trials) | 4 |
| AUTH-24 | Biometric identity verification | Liveness and face match through pluggable providers (re-verification, recovery) | 5 |

## D. Federation & SSO (`FED`)

| ID | Capability | Details | Phase |
|----|-----------|---------|-------|
| FED-01 | OpenID Provider | SSO across the tenant's apps | 1 |
| FED-02 | SAML 2.0 IdP | SP- and IdP-initiated; signed and encrypted assertions; SLO; metadata; attribute mapping | 2 |
| FED-03 | SAML 2.0 inbound | Enterprise IdPs (Okta, Entra ID, AD FS, Ping, Google Workspace, …) | 2 |
| FED-04 | OIDC inbound | Enterprise OIDC IdPs | 2 |
| FED-05 | WS-Federation | IdP and RP, for legacy Microsoft ecosystems | 5 |
| FED-06 | LDAP bind | Authenticate against on-prem directories through an outbound-only agent | 2 |
| FED-07 | Home-realm discovery | Route by email domain, organization, IP range or application | 2 |
| FED-08 | JIT provisioning | Create and update users on federated login using attribute and group mapping expressions | 2 |
| FED-09 | Single logout | OIDC front-channel, back-channel and RP-initiated; SAML SLO | 2 |
| FED-10 | Identity brokering | Tenant acts as a hub between many IdPs and many SPs, with protocol translation (SAML ↔ OIDC) | 2 |
| FED-11 | OpenID Federation 1.0 | Trust chains and automatic registration for federations (education, health, government) | 6 |
| FED-12 | RADIUS server | MFA for VPNs and network devices | 5 |
| FED-13 | App catalog | Pre-built SAML/OIDC templates for popular SaaS apps | 4 |

## E. OAuth 2 / OpenID Connect authorization server (`OAUTH`)

| ID | Capability | Details | Phase |
|----|-----------|---------|-------|
| OAUTH-01 | OAuth 2.1 baseline | Authorization code + PKCE (S256 enforced); implicit and password grants off by default, legacy toggles only | 1 |
| OAUTH-02 | Client credentials | `client_secret_basic/post`, `private_key_jwt`, mTLS client authentication | 1 |
| OAUTH-03 | Refresh tokens | Rotation, reuse detection with family revocation, sliding and absolute lifetimes | 1 |
| OAUTH-04 | API registry | Resource servers, scopes, audiences, resource indicators | 1 |
| OAUTH-05 | Metadata | OIDC Discovery, AS metadata, JWKS, `iss` response parameter | 1 |
| OAUTH-06 | Token formats | JWT access tokens (RFC 9068) or opaque; introspection; revocation | 1 |
| OAUTH-07 | Consent | Consent screens, remembered consent, per-scope revoke, first-party skip | 1 |
| OAUTH-08 | Dynamic client registration | RFC 7591/7592, software statements, registration policies | 2 |
| OAUTH-09 | Token exchange | Delegation, impersonation (`act` claim), cross-domain exchange | 2 |
| OAUTH-10 | JWT bearer assertions | RFC 7523 grants and client authentication | 2 |
| OAUTH-11 | PAR | Pushed authorization requests | 2 |
| OAUTH-12 | JAR and JARM | Signed/encrypted requests and responses | 5 |
| OAUTH-13 | Sender-constrained tokens | DPoP; certificate-bound tokens (mTLS) | 4 |
| OAUTH-14 | Rich authorization requests | RFC 9396 `authorization_details` + Grant Management (open banking consents) | 5 |
| OAUTH-15 | MCP / protected resource metadata | RFC 9728, client ID metadata documents, DCR for MCP clients | 2 |
| OAUTH-16 | Custom grant types | Defined through the extension framework | 4 |
| OAUTH-17 | Transaction tokens | Short-lived, call-chain-scoped tokens for internal microservices (IETF draft) | 6 |
| OAUTH-18 | Native apps | RFC 8252 best practices; app attestation (Play Integrity, App Attest) | 2 |

## F. Sessions, tokens & keys (`SESS`)

| ID | Capability | Details | Phase |
|----|-----------|---------|-------|
| SESS-01 | SSO session | Idle and absolute timeouts per tenant, app, organization or risk level | 1 |
| SESS-02 | Session management | Users and admins list and revoke sessions; sign out everywhere | 1 |
| SESS-03 | Claim mapping | Custom claims with CEL expressions (1); enrichment through Actions (2) | 1→2 |
| SESS-04 | Signing keys | Per tenant; RS256, PS256, ES256, EdDSA; automatic rotation with JWKS overlap | 1 |
| SESS-05 | KMS / HSM | AWS KMS, Azure Key Vault, GCP KMS, HashiCorp Vault, PKCS#11 HSMs; bring your own key and hold your own key | 4 |
| SESS-06 | Continuous access evaluation | Send and receive CAEP and RISC events (Shared Signals Framework) | 4 |
| SESS-07 | Session binding | Device-bound session cookies, hijack anomaly detection | 4 |
| SESS-08 | Token encryption | JWE for ID tokens and userinfo | 5 |
| SESS-09 | Crypto agility | Algorithm policy per tenant; post-quantum readiness (ML-DSA, hybrid TLS) as the ecosystem matures | 6 |

## G. Authorization (`AUTHZ`)

| ID | Capability | Details | Phase |
|----|-----------|---------|-------|
| AUTHZ-01 | Unified authorization model | Resource types, relations, permissions in one schema; text DSL and visual editor | 1→3 |
| AUTHZ-02 | RBAC | Roles, permissions, role hierarchy, organization-scoped roles, default roles, per-tenant custom roles | 1 |
| AUTHZ-03 | ReBAC (Zanzibar-style) | Relationship tuples, nested groups, hierarchies (folders/documents), wildcards | 3 |
| AUTHZ-04 | ABAC | Conditions on subject, resource and context attributes (CEL); conditional relationships | 3 |
| AUTHZ-05 | Policy as code | Cedar permit/forbid guardrails: separation of duties, time, IP, assurance level, risk | 3 |
| AUTHZ-06 | Check API | Single check (1); batch and OpenID AuthZEN Authorization API (3) | 1→3 |
| AUTHZ-07 | Search APIs | "What can subject X access?" and "Who can access resource Y?" | 3 |
| AUTHZ-08 | Explain | Decision trace: which relation, role or policy allowed or denied | 3 |
| AUTHZ-09 | Policy lifecycle | Versioning, policy unit tests, what-if simulation on real data, shadow mode, staged rollout, rollback | 3 |
| AUTHZ-10 | Consistency control | Snapshot tokens ("zookies"); choose latency vs full consistency per request | 3 |
| AUTHZ-11 | Data filtering | Partial evaluation into query filters (SQL, Elasticsearch/OpenSearch, MongoDB) | 4 |
| AUTHZ-12 | Edge / local PDP | Sidecar, embedded library and WASM builds with data sync; < 1 ms checks | 3→4 |
| AUTHZ-13 | Gateway and mesh plugins | Envoy `ext_authz`, Istio, Kong, NGINX, AWS API Gateway, Azure APIM, Apigee, Kubernetes admission | 4 |
| AUTHZ-14 | Decision logs | Sampled or full; streamed to SIEM and analytics | 3 |
| AUTHZ-15 | JIT and time-bound access | Requests, approvals, automatic expiry; break-glass with alerting | 4 |
| AUTHZ-16 | Field and row-level helpers | SDK helpers to mask fields and filter rows | 4 |
| AUTHZ-17 | Permissions in tokens | Scopes, roles and permissions claims with size controls | 1 |
| AUTHZ-18 | Model templates | Multi-tenant SaaS RBAC, document sharing, org/team/repo, healthcare consent, bank accounts and mandates | 3 |
| AUTHZ-19 | Attribute sources (PIP) | Pull attributes from HTTP APIs and databases, with TTL caching | 4 |
| AUTHZ-20 | XACML compatibility | Import or translate legacy XACML policies | 6 |

## H. B2B organizations (`ORG`)

| ID | Capability | Details | Phase |
|----|-----------|---------|-------|
| ORG-01 | Organizations | Members, roles, invitations, metadata, per-org branding | 1 |
| ORG-02 | Domain verification | DNS TXT verification; auto-join; enforce SSO for verified domains | 2 |
| ORG-03 | Per-org connections | Each organization brings its own SSO and SCIM | 2 |
| ORG-04 | Self-service admin portal | A link the customer's IT admin opens to set up SSO and SCIM through guided wizards | 2 |
| ORG-05 | Org policies | MFA, sessions, allowed login methods, IP allowlists per organization | 2 |
| ORG-06 | Org hierarchy | Parent/child organizations, MSP and partner access | 4 |
| ORG-07 | Multi-org membership | Organization switcher; tokens scoped to the active organization | 2 |
| ORG-08 | Per-org data residency | Organization data pinned to a region | 5 |

## I. Machine, workload & AI-agent identity (`M2M`)

| ID | Capability | Details | Phase |
|----|-----------|---------|-------|
| M2M-01 | Service accounts | M2M clients with scopes and quotas | 1 |
| M2M-02 | API keys | Hashed, scoped, prefixed (for secret scanning), expiring, rotatable | 2 |
| M2M-03 | Workload identity federation | Accept OIDC tokens from Kubernetes, GitHub Actions, GitLab, AWS, Azure and GCP, so no static secrets are needed | 4 |
| M2M-04 | SPIFFE | Accept and issue SPIFFE SVIDs (X.509 and JWT) | 5 |
| M2M-05 | IoT device identity | X.509 certificate lifecycle, attestation, device flow | 5 |
| M2M-06 | MCP authorization server | OAuth 2.1 for MCP clients and servers: metadata, registration, consent for tools | 2 |
| M2M-07 | Agent identities | First-class agent principals; on-behalf-of delegation (`act` claim); scoped, time-boxed, revocable | 4 |
| M2M-08 | Human-in-the-loop approvals | CIBA or push approval before an agent performs a sensitive action | 5 |
| M2M-09 | Agent oversight | Agent activity audit, anomaly detection, kill switch | 5 |

## J. Dynamic engines & extensibility (`DYN`)

| ID | Capability | Details | Phase |
|----|-----------|---------|-------|
| DYN-01 | Configuration versioning | Draft → validate → publish → roll back for every configuration object | 1 |
| DYN-02 | Flow (journey) engine | Versioned node graphs per app or organization; built-in default flows (1); A/B variants (2) | 1→2 |
| DYN-03 | Visual flow designer | No-code editor with simulation and step debugger | 2 |
| DYN-04 | Server-driven UI | Flows emit screen descriptors; web and native SDKs render them, so mobile login works without webviews | 2 |
| DYN-05 | Expression language | CEL for conditions, mappings and validation everywhere | 1 |
| DYN-06 | Actions | Sandboxed custom code (WASM; written in JS/TS, Go, Rust, Python) at hook points: pre-registration, post-login, pre-token, pre-MFA, post-password-change, and more; with secrets, timeouts and logs | 2 |
| DYN-07 | Webhooks | Signed (Standard Webhooks), CloudEvents format, retries with backoff, dead-letter queue, replay | 1 |
| DYN-08 | Event streaming | Kafka, Kinesis, EventBridge, Pub/Sub, Event Hubs sinks | 4 |
| DYN-09 | Connector framework | IdPs, messaging (email, SMS, WhatsApp, push), CAPTCHA, KYC, risk, CRM — declared by manifest | 1 (framework), ongoing |
| DYN-10 | Template engine | Emails, SMS and UI text; 40+ locales including RTL; per-org overrides | 1 |
| DYN-11 | Configuration as code | YAML export/import and GitOps sync (2); Terraform provider (2); Pulumi and Kubernetes operator (4) | 2→4 |
| DYN-12 | Gradual rollout | Release a configuration version to a percentage of traffic, with automatic rollback on error spikes | 2 |
| DYN-13 | Forms builder | Registration and profile forms generated from the user schema | 2 |

## K. Security & threat protection (`SEC`)

| ID | Capability | Details | Phase |
|----|-----------|---------|-------|
| SEC-01 | Brute-force and stuffing protection | Per user, IP, ASN and device; progressive delays; lockout policies | 1 |
| SEC-02 | Rate limiting | Per tenant, endpoint and client; configurable | 1 |
| SEC-03 | Bot protection | Pluggable CAPTCHA (Turnstile, hCaptcha, reCAPTCHA, Friendly Captcha), invisible challenges, proof of work | 2 |
| SEC-04 | Risk engine | IP reputation, Tor/VPN/proxy detection, impossible travel, device fingerprint, behavioral signals; rules plus ML scoring | 4 |
| SEC-05 | Toll-fraud protection | SMS-pumping detection, geo permissions, spend caps | 2 |
| SEC-06 | Notifications | New device and suspicious activity alerts to users and admins | 2 |
| SEC-07 | Account takeover response | Automatic session revocation, forced reset, Shared Signals transmission | 4 |
| SEC-08 | Encryption | TLS 1.3; encryption at rest (1); per-tenant data keys, field-level PII encryption, crypto-shredding (4) | 1→4 |
| SEC-09 | Secure defaults | PKCE required, exact redirect matching, CORS allowlists, CSP, CSRF, clickjacking protection, hardened cookies | 1 |
| SEC-10 | Admin hardening | Phishing-resistant MFA for admins, admin IP allowlists, re-authentication for privileged actions, four-eyes approval for sensitive changes | 2 |
| SEC-11 | Leaked secret response | Secret-scanning partnerships; leaked client secrets and API keys revoked automatically | 4 |
| SEC-12 | FIPS mode | FIPS 140-3 validated cryptography for government and regulated tenants | 5 |
| SEC-13 | Network controls | IP allow/deny lists and geo-blocking per tenant and app | 2 |

## L. Governance, audit & privacy (`GOV`)

| ID | Capability | Details | Phase |
|----|-----------|---------|-------|
| GOV-01 | Audit log | Every admin and authentication event; OCSF schema; retention policy (1); tamper-evident hash chain (4) | 1→4 |
| GOV-02 | Log streaming | Splunk, Datadog, Elastic, Microsoft Sentinel, Sumo Logic, S3, generic HTTPS | 2 |
| GOV-03 | Analytics | Sign-ups, logins, MFA adoption, failures, risk, funnel drop-off | 2 |
| GOV-04 | Access reviews | Certification campaigns with reviewer delegation and auto-revoke | 4 |
| GOV-05 | Access requests | Self-service requests with manager and multi-stage approvals | 4 |
| GOV-06 | Separation of duties | Toxic-combination rules and violation reports | 4 |
| GOV-07 | Privacy | Versioned terms and consent records (2); purpose-based consent, DSAR export, right to erasure, minimization (4) | 2→4 |
| GOV-08 | Compliance reports | SOC 2 evidence, PCI DSS requirement 8, HIPAA access reports | 4 |
| GOV-09 | Retention and legal hold | Per data class | 4 |
| GOV-10 | Change history | Who changed which configuration, with diffs | 1 |

## M. Experience: hosted UI, console, portals (`UX`)

| ID | Capability | Details | Phase |
|----|-----------|---------|-------|
| UX-01 | Hosted login | Themes, custom CSS, custom domain, localization, WCAG 2.2 AA accessibility | 1 |
| UX-02 | Embeddable components | Web components plus React, Vue and Angular wrappers | 2 |
| UX-03 | Headless APIs | Build a fully custom UI on the authentication API | 1 |
| UX-04 | Native mobile UI | iOS, Android, Flutter and React Native rendering of server-driven screens | 2 |
| UX-05 | Account portal | Profile, passkeys and MFA, sessions and devices, connected apps and consents, data export and deletion | 2 |
| UX-06 | Admin console | Tenants, users, apps, connections, flows, policies, branding, logs, analytics | 1 |
| UX-07 | Organization admin portal | For our customers' customers | 2 |
| UX-08 | Support console | Scoped, audited support tooling | 4 |

## N. Developer experience (`DX`)

| ID | Capability | Details | Phase |
|----|-----------|---------|-------|
| DX-01 | APIs | Management API (REST, OpenAPI 3.1); gRPC for the data plane | 1 |
| DX-02 | SDKs | Phase 1: JS/TS, React, Next.js, Node.js, Go, Java/Spring, Python. Phase 2: iOS, Android, Flutter, React Native, .NET, PHP/Laravel, Ruby/Rails, Vue, Angular | 1→2 |
| DX-03 | CLI | Log in, manage tenants, export/import configuration, tail logs, mint test tokens | 2 |
| DX-04 | Local emulator | One Docker image for development and CI | 2 |
| DX-05 | Documentation | Quick-starts, sample apps, interactive API explorer | 1 |
| DX-06 | Test mode | Test users, captured OTP delivery, deterministic tokens | 2 |
| DX-07 | Terraform provider | Full management API coverage | 2 |

## O. Operations & commercial platform (`OPS`)

| ID | Capability | Details | Phase |
|----|-----------|---------|-------|
| OPS-01 | Metering and billing | Usage metering (1); billing integration and invoices (2) | 1→2 |
| OPS-02 | Status and SLA | Public status page; per-tenant SLA reporting | 4 |
| OPS-03 | Backup and restore | Per-tenant restore, point-in-time recovery | 4 |
| OPS-04 | Multi-region | Active-active for global tenants; regional failover | 5 |
| OPS-05 | Self-hosted edition | Helm chart (2, internal), Kubernetes operator, air-gapped bundles, license enforcement (5) | 2→5 |
| OPS-06 | Marketplaces | AWS, Azure, GCP listings and private offers | 5 |

## P. Migration (`MIG`)

| ID | Capability | Details | Phase |
|----|-----------|---------|-------|
| MIG-01 | Vendor importers | Auth0, Cognito, Firebase, Keycloak, Okta, Azure AD B2C, Ping: users, hashes where exportable, roles, applications | 2→4 |
| MIG-02 | Lazy migration | See IDM-08 | 2 |
| MIG-03 | Dual running | Federate with the old IdP during cutover | 4 |
| MIG-04 | Full export | Customers can always leave with their data | 1 |

## Q. Industry packs (`IND`)

Bundles of journeys, policies, claim profiles, consent models and reports per
vertical. See [03 — Industry packs](03-standards-and-compliance.md#5-industry-packs).

| ID | Pack | Phase |
|----|------|-------|
| IND-01 | B2B SaaS | 2 |
| IND-02 | Retail & e-commerce | 2 |
| IND-03 | Banking, fintech & open banking | 5 |
| IND-04 | Insurance | 5 |
| IND-05 | Healthcare | 5 |
| IND-06 | Government & public sector | 5 |
| IND-07 | Telecom | 5 |
| IND-08 | Education | 5 |
| IND-09 | Media, gaming & streaming | 4 |
| IND-10 | IoT, automotive & manufacturing | 5 |
| IND-11 | AI agents & API platforms | 4 |
| IND-12 | Travel & hospitality | 6 |
