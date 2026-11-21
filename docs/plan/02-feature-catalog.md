# 02 — Feature Catalog

The complete capability list. Every feature has a stable **ID** so it can become
an epic in the issue tracker.

**All 252 features are part of the first release** (decision D-23). None is
deferred to a later phase; see the [delivery plan](05-roadmap-and-delivery.md).

## Workstream ownership

Each section is built end to end (engine, API, console, SDK, Terraform, docs,
tests) by one workstream ([05 §3](05-roadmap-and-delivery.md#3-workstreams-and-team)):

| Section | Workstream |
|---------|-----------|
| A. Tenancy & platform (`TEN`) | WS-01 Platform core and hierarchy |
| B. Identity & directory (`IDM`), H. B2B organizations (`ORG`), P. Migration (`MIG`) | WS-02 Identity and organizations |
| C. Authentication methods (`AUTH`) | WS-03 Authentication |
| D. Federation & SSO (`FED`), E. OAuth / OIDC (`OAUTH`), F. Sessions, tokens & keys (`SESS`) | WS-04 Protocols and federation |
| G. Authorization (`AUTHZ`) | WS-05 Authorization |
| I. Machine, workload & AI-agent identity (`M2M`) | WS-06 Machine, agent and AI identity |
| J. Dynamic engines & extensibility (`DYN`) | WS-07 Flows and extensibility |
| K. Security & threat protection (`SEC`) | WS-08 Security and risk |
| L. Governance, audit & privacy (`GOV`) | WS-09 Audit and governance |
| M. Experience (`UX`) | WS-10 Experience |
| N. Developer experience (`DX`) | WS-11 Developer experience |
| O. Operations & commercial platform (`OPS`) | WS-12 Infrastructure, SRE and self-hosted |
| Q. Industry packs (`IND`) | WS-13 Industry packs and compliance |

For a single view of every authentication type and every authorization level
(user, role, group, organization, resource, attribute, field, row, …), see
[07 — Access coverage](07-access-coverage.md).

Standards references are listed in [03](03-standards-and-compliance.md).

---

## A. Tenancy & platform (`TEN`)

| ID | Capability | Details |
|----|-----------|---------|
| TEN-01 | Tenant hierarchy | Platform → Partner → Tenant → Environment (dev/stage/prod) → Organization → Sub-organizations, in the data model. See [15](15-hierarchy-and-shared-settings.md) |
| TEN-02 | Multi-level tenancy | Tenants create and manage child tenants; white-label; usage roll-up for resellers and ISVs |
| TEN-03 | Isolation tiers | Pooled (row-level security), database-per-tenant, dedicated cell, customer VPC |
| TEN-04 | Custom domains | `login.customer.com` with automatic TLS (ACME), per tenant, environment or organization |
| TEN-05 | Data residency | Tenant pinned to a region (US, EU, UK, IN, AU, …); PII never leaves its region |
| TEN-06 | Configuration inheritance | Settings inherit down the hierarchy (platform → tenant → environment → organization → sub-organization → application) |
| TEN-07 | Environment promotion | Diff, promote dev → stage → prod, roll back |
| TEN-08 | Entitlements & quotas | Plan-based feature gating, quotas and rate limits per tenant, all configurable |
| TEN-09 | Tenant lifecycle | Provisioning API, suspend, export, delete with crypto-shredding |
| TEN-10 | Self-service sign-up | Create tenant, choose region and plan, upgrade in-app |
| TEN-11 | Override policies and merge rules | Per setting, set by parent admins for their descendants: `overridable`, `bounded`, `additive`, `locked`; merge rules most specific, strictest, intersection, union and ceiling. See [15](15-hierarchy-and-shared-settings.md) |
| TEN-12 | Effective-configuration view | Every setting's value and source (inherited, overridden, locked, template, shared); revert to inherited; diff against parent; API with `include=sources` |
| TEN-13 | Shared library objects | Flows, themes, roles, SSO connections, connectors and policies owned by an ancestor and referenced by descendants; where-used, impact analysis, delete protection; secrets usable but never readable by children |
| TEN-14 | Linked templates and push | Versioned templates for organizations, partner → tenant templates and platform baselines; push modes automatic, staged (waves with health checks), opt-in with deadline, manual, forced (four-eyes); three-way conflict handling; drift reports |
| TEN-15 | Copy mode | Create a child as an independent copy of a template or another node, with "update available" notices |
| TEN-16 | Allocation down the hierarchy | Parents allocate quotas and entitlements (users, AI usage, connections, features) to children within their own limits; usage roll-up |
| TEN-17 | Setting modes | Every setting is in Default, Parent, Template or Own mode, switchable at any time in either direction; new tenants start in Default, new children in the mode the parent chooses (Parent out of the box) |
| TEN-18 | Reset | Reset to Default, Parent or Template for one setting, selected settings, a section, an object or a whole level (complete reset), optionally including descendants; preview, simulation, reason, approval and one-click undo; data is never touched |
| TEN-19 | Versioned product defaults | Each tenant environment uses a pinned defaults version; new versions are reviewed as a diff and adopted deliberately; security minimums arrive as platform baselines |

## B. Identity & directory (`IDM`)

| ID | Capability | Details |
|----|-----------|---------|
| IDM-01 | Dynamic user schema | Per-tenant custom attributes (JSON Schema): types, validation, PII flag, uniqueness, read/write visibility per audience |
| IDM-02 | Multiple identifiers | Email, phone, username, custom IDs; verified state per identifier; identifier-first login |
| IDM-03 | Groups | Static groups and dynamic rule-based membership (CEL) |
| IDM-04 | Account linking & merging | Automatic by verified email (policy-controlled), user-initiated, or admin merge |
| IDM-05 | Lifecycle states | Staged, invited, active, locked, suspended, deprovisioned; hooks on each transition |
| IDM-06 | Progressive profiling | Ask for attributes over time based on conditions |
| IDM-07 | Bulk import / export | CSV/JSON async jobs; password hashes: bcrypt, scrypt, Firebase scrypt, argon2, PBKDF2, salted SHA/MD5 (legacy) with re-hash on login |
| IDM-08 | Lazy migration | Authenticate against a legacy store on first login, then migrate |
| IDM-09 | SCIM 2.0 server | Inbound provisioning of users and groups from enterprise directories |
| IDM-10 | SCIM client | Outbound provisioning to downstream applications |
| IDM-11 | Directory sync | LDAP / Active Directory (through an on-prem agent), Google Workspace, Microsoft Entra ID |
| IDM-12 | Search | Indexed attribute queries, filters, pagination, saved segments |
| IDM-13 | Delegated administration | Admin roles scoped to an organization, group or attribute segment |
| IDM-14 | Device registry | Trusted devices, device attributes and posture |
| IDM-15 | Identity proofing (KYC) | Pluggable document + selfie and eKYC providers; NIST IAL2 flows; OpenID Identity Assurance claims |
| IDM-16 | Support tools | User timeline, impersonation with consent and audit, guided recovery |
| IDM-17 | Guardian relationships | Family, household, parent/child with parental consent and age gates |

## C. Authentication methods (`AUTH`)

| ID | Capability | Details |
|----|-----------|---------|
| AUTH-01 | Passwords | NIST SP 800-63B policies; configurable complexity, history, expiry; Argon2id hashing; KMS-held pepper |
| AUTH-02 | Breached-password detection | k-anonymity range lookups; local corpus option for air-gapped installs |
| AUTH-03 | Email magic link and OTP | Configurable length, TTL, attempts; same-device and cross-device |
| AUTH-04 | SMS, voice and WhatsApp OTP | Pluggable providers with failover; SMS-pumping protection; per-tenant spend caps |
| AUTH-05 | Passkeys / WebAuthn | Platform and roaming authenticators, synced and device-bound, conditional UI (autofill), attestation policy, FIDO MDS3 |
| AUTH-06 | TOTP / HOTP | Authenticator apps |
| AUTH-07 | Push MFA | Our own authenticator app and mobile SDK; number matching; biometric confirmation |
| AUTH-08 | Recovery | Recovery codes; configurable recovery journeys; admin-assisted recovery |
| AUTH-09 | Social login | Google, Apple, Microsoft, GitHub, Facebook, LinkedIn, X, Amazon, Slack, Discord and more, plus a generic OAuth2/OIDC connector |
| AUTH-10 | Certificates and smart cards | X.509 client certificates, PIV/CAC, mutual TLS |
| AUTH-11 | Kerberos / SPNEGO | Integrated Windows Authentication |
| AUTH-12 | Device authorization grant | TVs, consoles, CLIs, IoT |
| AUTH-13 | QR / cross-device login | Approve a desktop login from an authenticated phone |
| AUTH-14 | CIBA | Decoupled authentication (poll, ping, push) for call centers, banking, agent approvals |
| AUTH-15 | SIM-based verification | Silent network authentication and number verification (GSMA Open Gateway / CAMARA), SIM-swap signal |
| AUTH-16 | Verifiable credentials | OpenID4VP presentation of SD-JWT VC and mDL (ISO 18013-5/-7); EU Digital Identity Wallet |
| AUTH-17 | Step-up authentication | `acr`/`amr` requests and enforcement; RFC 9470 challenges from APIs |
| AUTH-18 | Adaptive / risk-based MFA | Policies over risk score, device, location, network, behavior |
| AUTH-19 | Remember device | Trusted-device windows per policy |
| AUTH-20 | MFA policies | Required factors per app, organization or risk; enrollment grace periods; phishing-resistant-only mode |
| AUTH-21 | Transaction signing | PSD2 dynamic linking: amount and payee bound to the approval |
| AUTH-22 | Custom authenticators | Third-party factors through the extension SDK |
| AUTH-23 | Guest sessions | Anonymous sessions that upgrade to full accounts (carts, trials) |
| AUTH-24 | Biometric identity verification | Liveness and face match through pluggable providers (re-verification, recovery) |
| AUTH-25 | Mobile device binding | App PIN or biometric unlocks a key held in Secure Enclave or StrongBox; bank-app style login and transaction approval |
| AUTH-26 | One Tap and FedCM | Google One Tap, browser FedCM, native Sign in with Apple/Google token exchange for mobile apps |
| AUTH-27 | Hardware OATH tokens | Hardware TOTP/HOTP tokens with seed import (PSKC, RFC 6030) |
| AUTH-28 | Legacy compatibility | Security questions (migration only), resource-owner password grant, implicit grant. Off by default, never a sole factor, flagged in the console |
| AUTH-29 | Regional social providers | WeChat, LINE, Kakao, Naver, VK, Yahoo! JAPAN |

## D. Federation & SSO (`FED`)

| ID | Capability | Details |
|----|-----------|---------|
| FED-01 | OpenID Provider | SSO across the tenant's apps |
| FED-02 | SAML 2.0 IdP | SP- and IdP-initiated; signed and encrypted assertions; SLO; metadata; attribute mapping |
| FED-03 | SAML 2.0 inbound | Enterprise IdPs (Okta, Entra ID, AD FS, Ping, Google Workspace, …) |
| FED-04 | OIDC inbound | Enterprise OIDC IdPs |
| FED-05 | WS-Federation | IdP and RP, for legacy Microsoft ecosystems |
| FED-06 | LDAP bind | Authenticate against on-prem directories through an outbound-only agent |
| FED-07 | Home-realm discovery | Route by email domain, organization, IP range or application |
| FED-08 | JIT provisioning | Create and update users on federated login using attribute and group mapping expressions |
| FED-09 | Single logout | OIDC front-channel, back-channel and RP-initiated; SAML SLO |
| FED-10 | Identity brokering | Tenant acts as a hub between many IdPs and many SPs, with protocol translation (SAML ↔ OIDC) |
| FED-11 | OpenID Federation 1.0 | Trust chains and automatic registration for federations (education, health, government) |
| FED-12 | RADIUS server | MFA for VPNs and network devices |
| FED-13 | App catalog | Pre-built SAML/OIDC templates for popular SaaS apps |

## E. OAuth 2 / OpenID Connect authorization server (`OAUTH`)

| ID | Capability | Details |
|----|-----------|---------|
| OAUTH-01 | OAuth 2.1 baseline | Authorization code + PKCE (S256 enforced); implicit and password grants off by default, legacy toggles only |
| OAUTH-02 | Client credentials | `client_secret_basic/post`, `private_key_jwt`, mTLS client authentication |
| OAUTH-03 | Refresh tokens | Rotation, reuse detection with family revocation, sliding and absolute lifetimes |
| OAUTH-04 | API registry | Resource servers, scopes, audiences, resource indicators |
| OAUTH-05 | Metadata | OIDC Discovery, AS metadata, JWKS, `iss` response parameter |
| OAUTH-06 | Token formats | JWT access tokens (RFC 9068) or opaque; introspection; revocation |
| OAUTH-07 | Consent | Consent screens, remembered consent, per-scope revoke, first-party skip |
| OAUTH-08 | Dynamic client registration | RFC 7591/7592, software statements, registration policies |
| OAUTH-09 | Token exchange | Delegation, impersonation (`act` claim), cross-domain exchange |
| OAUTH-10 | JWT bearer assertions | RFC 7523 grants and client authentication |
| OAUTH-11 | PAR | Pushed authorization requests |
| OAUTH-12 | JAR and JARM | Signed/encrypted requests and responses |
| OAUTH-13 | Sender-constrained tokens | DPoP; certificate-bound tokens (mTLS) |
| OAUTH-14 | Rich authorization requests | RFC 9396 `authorization_details` + Grant Management (open banking consents) |
| OAUTH-15 | MCP / protected resource metadata | RFC 9728, client ID metadata documents, DCR for MCP clients |
| OAUTH-16 | Custom grant types | Defined through the extension framework |
| OAUTH-17 | Transaction tokens | Short-lived, call-chain-scoped tokens for internal microservices (IETF draft) |
| OAUTH-18 | Native apps | RFC 8252 best practices; app attestation (Play Integrity, App Attest) |

## F. Sessions, tokens & keys (`SESS`)

| ID | Capability | Details |
|----|-----------|---------|
| SESS-01 | SSO session | Idle and absolute timeouts per tenant, app, organization or risk level |
| SESS-02 | Session management | Users and admins list and revoke sessions; sign out everywhere |
| SESS-03 | Claim mapping | Custom claims with CEL expressions and enrichment through Actions |
| SESS-04 | Signing keys | Per tenant; RS256, PS256, ES256, EdDSA; automatic rotation with JWKS overlap |
| SESS-05 | KMS / HSM | AWS KMS, Azure Key Vault, GCP KMS, HashiCorp Vault, PKCS#11 HSMs; bring your own key and hold your own key |
| SESS-06 | Continuous access evaluation | Send and receive CAEP and RISC events (Shared Signals Framework) |
| SESS-07 | Session binding | Device-bound session cookies, hijack anomaly detection |
| SESS-08 | Token encryption | JWE for ID tokens and userinfo |
| SESS-09 | Crypto agility | Algorithm policy per tenant; post-quantum readiness (ML-DSA, hybrid TLS) as the ecosystem matures |
| SESS-10 | BFF and server-side sessions | Backend-for-frontend token handler for SPAs (tokens never reach the browser); server-side session helpers for classic web apps |

## G. Authorization (`AUTHZ`)

| ID | Capability | Details |
|----|-----------|---------|
| AUTHZ-01 | Unified authorization model | Resource types, relations, permissions in one schema; text DSL and visual editor |
| AUTHZ-02 | RBAC | Roles, permissions, role hierarchy, organization-scoped roles, default roles, per-tenant custom roles |
| AUTHZ-03 | ReBAC (Zanzibar-style) | Relationship tuples, nested groups, hierarchies (folders/documents), wildcards |
| AUTHZ-04 | ABAC | Conditions on subject, resource and context attributes (CEL); conditional relationships |
| AUTHZ-05 | Policy as code | Cedar permit/forbid guardrails: separation of duties, time, IP, assurance level, risk |
| AUTHZ-06 | Check API | Single and batch checks; OpenID AuthZEN Authorization API |
| AUTHZ-07 | Search APIs | "What can subject X access?" and "Who can access resource Y?" |
| AUTHZ-08 | Explain | Decision trace: which relation, role or policy allowed or denied |
| AUTHZ-09 | Policy lifecycle | Versioning, policy unit tests, what-if simulation on real data, shadow mode, staged rollout, rollback |
| AUTHZ-10 | Consistency control | Snapshot tokens ("zookies"); choose latency vs full consistency per request |
| AUTHZ-11 | Data filtering | Partial evaluation into query filters (SQL, Elasticsearch/OpenSearch, MongoDB) |
| AUTHZ-12 | Edge / local PDP | Sidecar, embedded library and WASM builds with data sync; < 1 ms checks |
| AUTHZ-13 | Gateway and mesh plugins | Envoy `ext_authz`, Istio, Kong, NGINX, AWS API Gateway, Azure APIM, Apigee, Kubernetes admission |
| AUTHZ-14 | Decision logs | Every decision recorded by default through a durable spool, with strict mode (refuse rather than leave unrecorded); explanation references; SIEM and analytics streaming. See [13](13-audit-and-change-tracking.md) |
| AUTHZ-15 | JIT and time-bound access | Requests, approvals, automatic expiry; break-glass with alerting |
| AUTHZ-16 | Field and row-level helpers | SDK helpers to mask fields and filter rows |
| AUTHZ-17 | Permissions in tokens | Scopes, roles and permissions claims with size controls |
| AUTHZ-18 | Model templates | Multi-tenant SaaS RBAC, document sharing, org/team/repo, healthcare consent, bank accounts and mandates |
| AUTHZ-19 | Attribute sources (PIP) | Pull attributes from HTTP APIs and databases, with TTL caching |
| AUTHZ-20 | Policy import and compatibility | Import or translate legacy XACML policies and OPA/Rego policies where they map to the model |
| AUTHZ-21 | Direct user permissions | Grant a permission to one user without a role; explicit per-user deny |
| AUTHZ-22 | Group-based roles | Assign roles to groups, including nested groups; members inherit |
| AUTHZ-23 | UI / feature authorization | Permissions endpoint for front ends; SDK helpers (`useCan`, `<Can>`) to show or hide menus, pages and buttons |
| AUTHZ-24 | Product entitlements | Plan- or subscription-based features and quotas for our customers' own products, per organization |
| AUTHZ-25 | API endpoint authorization | SDK middleware mapping routes to permissions (Go, Node.js, Java, Python, .NET) |
| AUTHZ-26 | Effective permissions | Per user or resource: every permission and its source (direct, role, group, org, relationship, delegation), with explanations |
| AUTHZ-27 | Admin roles | Built-in roles for console and Management API (Owner, Admin, Developer, Security admin, Support, Auditor, Billing) and custom admin roles scoped to environment or organization |
| AUTHZ-28 | User-to-user delegation | Assistant, proxy and power-of-attorney access; time-bound and revocable |
| AUTHZ-29 | Permission catalog | Modules (UI, API, AI, MCP, data, custom) → nested components → actions; `module:component:action` keys, wildcards with impact view, conditions; roles pick permissions by module. See [14](14-integration-and-enforcement.md) |
| AUTHZ-30 | Component import and drift detection | Import components from OpenAPI documents, MCP tool lists and UI manifests; SDKs report permission keys used in code but missing from the catalog |
| AUTHZ-31 | AI authorization | Feature and model-tier gating, per-tool checks for model tool calls on behalf of the user, usage quotas per role and organization, permission-filtered RAG retrieval for vector and search stores |
| AUTHZ-32 | Decision reporting from enforcement points | SDK middleware, guards and sidecars report every decision (including checks answered from token claims) to the audit trail |
| AUTHZ-33 | Change propagation | Push invalidation of SDK caches and UI permission snapshots, sidecar sync; targets: PDP immediate, sidecars 5 s, SDK caches 30 s; measured and alerted per tenant |

## H. B2B organizations (`ORG`)

| ID | Capability | Details |
|----|-----------|---------|
| ORG-01 | Organizations | Members, roles, invitations, metadata, per-org branding |
| ORG-02 | Domain verification | DNS TXT verification; auto-join; enforce SSO for verified domains |
| ORG-03 | Per-org connections | Each organization brings its own SSO and SCIM |
| ORG-04 | Self-service admin portal | A link the customer's IT admin opens to set up SSO and SCIM through guided wizards |
| ORG-05 | Org policies | MFA, sessions, allowed login methods, IP allowlists per organization |
| ORG-06 | Org hierarchy | Nested organizations up to 10 levels with inherited settings and per-organization membership inheritance; MSP and partner access across organizations |
| ORG-07 | Multi-org membership | Organization switcher; tokens scoped to the active organization |
| ORG-08 | Per-org data residency | Organization data pinned to a region |

## I. Machine, workload & AI-agent identity (`M2M`)

| ID | Capability | Details |
|----|-----------|---------|
| M2M-01 | Service accounts | M2M clients with scopes and quotas |
| M2M-02 | API keys | Hashed, scoped, prefixed (for secret scanning), expiring, rotatable |
| M2M-03 | Workload identity federation | Accept OIDC tokens from Kubernetes, GitHub Actions, GitLab, AWS, Azure and GCP, so no static secrets are needed |
| M2M-04 | SPIFFE | Accept and issue SPIFFE SVIDs (X.509 and JWT) |
| M2M-05 | IoT device identity | X.509 certificate lifecycle, attestation, device flow |
| M2M-06 | MCP authorization server | OAuth 2.1 for MCP clients and servers: metadata, registration, consent for tools |
| M2M-07 | Agent identities | First-class agent principals; on-behalf-of delegation (`act` claim); scoped, time-boxed, revocable |
| M2M-08 | Human-in-the-loop approvals | CIBA or push approval before an agent performs a sensitive action |
| M2M-09 | Agent oversight | Agent activity audit, anomaly detection, kill switch |
| M2M-10 | MCP guard | For customers' MCP servers: token verification, `tools/list` filtered to permitted tools, per-tool and argument-level checks, human approval and step-up for sensitive tools, agent intersection rule; MCP gateway mode in front of many servers |

## J. Dynamic engines & extensibility (`DYN`)

| ID | Capability | Details |
|----|-----------|---------|
| DYN-01 | Configuration versioning | Draft → validate → publish → roll back for every configuration object |
| DYN-02 | Flow (journey) engine | Versioned node graphs per app or organization; built-in default flows; A/B variants |
| DYN-03 | Visual flow designer | No-code editor with simulation and step debugger |
| DYN-04 | Server-driven UI | Flows emit screen descriptors; web and native SDKs render them, so mobile login works without webviews |
| DYN-05 | Expression language | CEL for conditions, mappings and validation everywhere |
| DYN-06 | Actions | Sandboxed custom code (WASM; written in JS/TS, Go, Rust, Python) at hook points: pre-registration, post-login, pre-token, pre-MFA, post-password-change, and more; with secrets, timeouts and logs |
| DYN-07 | Webhooks | Signed (Standard Webhooks), CloudEvents format, retries with backoff, dead-letter queue, replay |
| DYN-08 | Event streaming | Kafka, Kinesis, EventBridge, Pub/Sub, Event Hubs sinks |
| DYN-09 | Connector framework | IdPs, messaging (email, SMS, WhatsApp, push), CAPTCHA, KYC, risk, CRM — declared by manifest |
| DYN-10 | Template engine | Emails, SMS and UI text; 40+ locales including RTL; per-org overrides |
| DYN-11 | Configuration as code | YAML export/import and GitOps sync; Terraform provider; Pulumi; Kubernetes operator |
| DYN-12 | Gradual rollout | Release a configuration version to a percentage of traffic, with automatic rollback on error spikes |
| DYN-13 | Forms builder | Registration and profile forms generated from the user schema |

## K. Security & threat protection (`SEC`)

| ID | Capability | Details |
|----|-----------|---------|
| SEC-01 | Brute-force and stuffing protection | Per user, IP, ASN and device; progressive delays; lockout policies |
| SEC-02 | Rate limiting | Per tenant, endpoint and client; configurable |
| SEC-03 | Bot protection | Pluggable CAPTCHA (Turnstile, hCaptcha, reCAPTCHA, Friendly Captcha), invisible challenges, proof of work |
| SEC-04 | Risk engine | IP reputation, Tor/VPN/proxy detection, impossible travel, device fingerprint, behavioral signals; rules plus ML scoring |
| SEC-05 | Toll-fraud protection | SMS-pumping detection, geo permissions, spend caps |
| SEC-06 | Notifications | New device and suspicious activity alerts to users and admins |
| SEC-07 | Account takeover response | Automatic session revocation, forced reset, Shared Signals transmission |
| SEC-08 | Encryption | TLS 1.3; encryption at rest; per-tenant data keys, field-level PII encryption, crypto-shredding |
| SEC-09 | Secure defaults | PKCE required, exact redirect matching, CORS allowlists, CSP, CSRF, clickjacking protection, hardened cookies |
| SEC-10 | Admin hardening | Phishing-resistant MFA for admins, admin IP allowlists, re-authentication for privileged actions, four-eyes approval for sensitive changes |
| SEC-11 | Leaked secret response | Secret-scanning partnerships; leaked client secrets and API keys revoked automatically |
| SEC-12 | FIPS mode | FIPS 140-3 validated cryptography for government and regulated tenants |
| SEC-13 | Network controls | IP allow/deny lists and geo-blocking per tenant and app |

## L. Governance, audit & privacy (`GOV`)

| ID | Capability | Details |
|----|-----------|---------|
| GOV-01 | Audit log | 100% of changes, authentication events and sensitive reads; OCSF mapping; per-tenant gapless sequence and hash chain; append-only storage; retention per plan. See [13](13-audit-and-change-tracking.md) |
| GOV-02 | Log streaming | Splunk, Datadog, Elastic, Microsoft Sentinel, Sumo Logic, S3, generic HTTPS |
| GOV-03 | Analytics | Sign-ups, logins, MFA adoption, failures, risk, funnel drop-off |
| GOV-04 | Access reviews | Certification campaigns with reviewer delegation and auto-revoke |
| GOV-05 | Access requests | Self-service requests with manager and multi-stage approvals |
| GOV-06 | Separation of duties | Toxic-combination rules and violation reports |
| GOV-07 | Privacy | Versioned terms and consent records, purpose-based consent, DSAR export, right to erasure, minimization |
| GOV-08 | Compliance reports | SOC 2 evidence, PCI DSS requirement 8, HIPAA access reports |
| GOV-09 | Retention and legal hold | Per data class |
| GOV-10 | Change history | History tab on every object (configuration and data): who, when, channel, reason, diff; one-click restore |
| GOV-11 | Complete change capture | Every create, update and delete from every channel (console, API, CLI, Terraform, GitOps, SCIM, sync, jobs, Actions) written in the same transaction as the change, with field-level before/after values; fail closed |
| GOV-12 | Database change reconciliation | Change data capture from the PostgreSQL write-ahead log matched to audit events; unaudited-change alerts in production and build failures in CI |
| GOV-13 | Point-in-time views | "As-of" view of any object and of a user's complete access (configuration, identity data and relationships) at any past moment |
| GOV-14 | Audit alerts | Rules that notify on chosen audit events (policy weakened, admin role granted, break-glass used) by email, Slack, webhook or SIEM |
| GOV-15 | Access transparency | Our staff's access to tenant data requires tenant approval, is time-boxed and appears in the tenant's audit log |
| GOV-16 | Audit the auditors | Every search, view and export of audit logs is itself audited |
| GOV-17 | Customer-owned audit copy | Continuous export to the customer's own write-once bucket |
| GOV-18 | Integrity verification | Signed Merkle checkpoints with external RFC 3161 timestamp anchoring; verification API and CLI |
| GOV-19 | Platform audit trail | Our own Git, CI/CD, GitOps, OpenTofu, Kubernetes, cloud, KMS, database and access-system events in a write-once security account |
| GOV-20 | Reasons and approvals | Required reasons for sensitive actions, change-ticket references on any change, recorded four-eyes approvals |

## M. Experience: hosted UI, console, portals (`UX`)

| ID | Capability | Details |
|----|-----------|---------|
| UX-01 | Hosted login | Fully brandable without code (theme studio, screen builder, templates), custom domain, 40+ languages, WCAG 2.2 AA accessibility. See [16](16-look-and-feel.md) |
| UX-02 | Embeddable components | Web components plus React, Vue and Angular wrappers |
| UX-03 | Headless APIs | Build a fully custom UI on the authentication API |
| UX-04 | Native mobile UI | iOS, Android, Flutter and React Native rendering of server-driven screens |
| UX-05 | Account portal | Profile, passkeys and MFA, sessions and devices, connected apps and consents, data export and deletion |
| UX-06 | Admin console | Tenants, users, apps, connections, flows, policies, branding, logs, analytics |
| UX-07 | Organization admin portal | For our customers' customers |
| UX-08 | Support console | Scoped, audited support tooling |
| UX-09 | Theme studio | No-code brand kit (logos, colors, fonts, radius, density, buttons, inputs), layouts, backgrounds, light/dark/high-contrast; live preview on every screen, device and language; live contrast and quality checks with one-click fixes |
| UX-10 | Brand import and design assistant | Generate an accessible theme from a website address or logo; optional assistant suggests themes and wording, always previewed and approved before use |
| UX-11 | Template gallery | Professionally designed, fully editable themes and layouts, including industry-pack themes; stay linked to receive improvements or make them your own |
| UX-12 | No-code screen builder | Drag-and-drop blocks on every hosted screen, conditional visibility rule builder, reusable header and footer, guardrails for required elements; one layout for web, components and native apps |
| UX-13 | Content and translations | Click-to-edit text in the preview, variables and plurals from menus, 40+ languages, XLIFF/JSON import and export, machine-translation suggestions with review |
| UX-14 | Message designer | Drag-and-drop responsive email designer inheriting the theme; SMS and push editors; client previews and test sends |
| UX-15 | Asset library | Logos, images, illustrations, icons and fonts; self-hosted fonts; automatic image optimization; where-used; shared down the hierarchy |
| UX-16 | White-labeling | Product name, logo, colors, domain, email sender, help and support links across console, portals and emails for partners and tenants |
| UX-17 | Design publishing | Draft, shareable preview links, approvals, visual diff, scheduled publish with automatic revert, rollback, A/B design experiments with conversion metrics |
| UX-18 | Pro mode (optional) | Sanitized custom CSS and HTML templates under a strict Content Security Policy; never required |
| UX-19 | Runtime design tokens | Tokens and screen descriptors delivered at runtime so hosted pages, web components and native SDKs match without rebuilds or app releases |
| UX-20 | Rich console interactions | Command palette, shortcuts, side panels, inline editing, undo everywhere, saved views, bulk actions, presence and conflict warnings, guided setup, personalization |

## N. Developer experience (`DX`)

| ID | Capability | Details |
|----|-----------|---------|
| DX-01 | APIs | Public REST APIs described in OpenAPI 3.2 ([10](10-api-standards.md)); gRPC for the data plane |
| DX-02 | SDKs | JS/TS, React, Next.js, Vue, Angular, Node.js, Go, Java/Spring, Python, .NET, PHP/Laravel, Ruby/Rails, iOS, Android, Flutter, React Native |
| DX-03 | CLI | Log in, manage tenants, export/import configuration, tail logs, mint test tokens |
| DX-04 | Local emulator | One Docker image for development and CI |
| DX-05 | Documentation | Quick-starts, sample apps, interactive API explorer |
| DX-06 | Test mode | Test users, captured OTP delivery, deterministic tokens |
| DX-07 | Terraform provider | Full management API coverage |

## O. Operations & commercial platform (`OPS`)

| ID | Capability | Details |
|----|-----------|---------|
| OPS-01 | Metering and billing | Usage metering, billing integration and invoices |
| OPS-02 | Status and SLA | Public status page; per-tenant SLA reporting |
| OPS-03 | Backup and restore | Per-tenant restore, point-in-time recovery |
| OPS-04 | Multi-region | Active-active for global tenants; regional failover |
| OPS-05 | Self-hosted edition | Helm chart, Kubernetes operator, air-gapped bundles, license enforcement |
| OPS-06 | Marketplaces | AWS, Azure, GCP listings and private offers |

## P. Migration (`MIG`)

| ID | Capability | Details |
|----|-----------|---------|
| MIG-01 | Vendor importers | Auth0, Cognito, Firebase, Keycloak, Okta, Azure AD B2C, Ping: users, hashes where exportable, roles, applications |
| MIG-02 | Lazy migration | See IDM-08 |
| MIG-03 | Dual running | Federate with the old IdP during cutover |
| MIG-04 | Full export | Customers can always leave with their data |

## Q. Industry packs (`IND`)

Bundles of journeys, policies, claim profiles, consent models and reports per
vertical. See [03 — Industry packs](03-standards-and-compliance.md#5-industry-packs).

| ID | Pack |
|----|------|
| IND-01 | B2B SaaS |
| IND-02 | Retail & e-commerce |
| IND-03 | Banking, fintech & open banking |
| IND-04 | Insurance |
| IND-05 | Healthcare |
| IND-06 | Government & public sector |
| IND-07 | Telecom |
| IND-08 | Education |
| IND-09 | Media, gaming & streaming |
| IND-10 | IoT, automotive & manufacturing |
| IND-11 | AI agents & API platforms |
| IND-12 | Travel & hospitality |
