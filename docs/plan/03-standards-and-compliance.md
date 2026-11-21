# 03 — Standards, Compliance & Industry Packs

Standards conformance is a product feature: it lets customers integrate
anything and leave whenever they want, and it is what regulated buyers check
first. Each standard has a target phase (see [roadmap](05-roadmap-and-delivery.md))
and, where one exists, a certification we will pursue.

## 1. Identity protocol standards

### 1.1 OAuth 2.x (IETF)

| Standard | Purpose | Phase |
|----------|---------|-------|
| RFC 6749 / 6750 | OAuth 2.0 framework and bearer tokens | 1 |
| OAuth 2.1 (draft) | Consolidated baseline: PKCE everywhere, no implicit or password grant | 1 |
| RFC 7636 | PKCE | 1 |
| RFC 8414 | Authorization server metadata | 1 |
| RFC 7662 / 7009 | Token introspection / revocation | 1 |
| RFC 9068 | JWT profile for access tokens | 1 |
| RFC 8707 | Resource indicators | 1 |
| RFC 9207 | Authorization server issuer identification (mix-up defense) | 1 |
| RFC 9700 | OAuth 2.0 Security Best Current Practice | 1 (design baseline) |
| RFC 8252 | OAuth for native apps | 2 |
| RFC 7591 / 7592 | Dynamic client registration and management | 2 |
| RFC 8628 | Device authorization grant | 2 |
| RFC 8693 | Token exchange | 2 |
| RFC 7521 / 7522 / 7523 | Assertion framework; SAML and JWT bearer grants | 2 |
| RFC 9126 | Pushed authorization requests (PAR) | 2 |
| RFC 9470 | Step-up authentication challenge | 2 |
| RFC 9728 | Protected resource metadata (used by MCP) | 2 |
| OAuth client ID metadata document (draft) | URL-based client IDs (used by MCP) | 2 |
| RFC 9449 | DPoP | 4 |
| RFC 8705 | Mutual-TLS client authentication and certificate-bound tokens | 4 |
| RFC 9101 | JWT-secured authorization request (JAR) | 5 |
| RFC 9396 | Rich authorization requests (RAR) | 5 |
| Transaction tokens (draft) | Call-chain tokens inside microservice meshes | 6 |
| Token status list (draft) | Revocation status for JWT/SD-JWT tokens and credentials | 6 |

### 1.2 JOSE / JWT (IETF)

RFC 7515 (JWS), 7516 (JWE), 7517 (JWK), 7518 (JWA), 7519 (JWT), 7638 (JWK
thumbprint), 8037 (EdDSA), 8725 (JWT best current practices). Phase 1 (JWE in 5).

### 1.3 OpenID Foundation

| Specification | Purpose | Phase | Certification |
|---------------|---------|-------|---------------|
| OpenID Connect Core 1.0, Discovery, Dynamic Registration | Identity layer | 1→2 | OpenID Certified (Basic, Config, Dynamic and Form Post OP profiles) |
| RP-Initiated, Front-Channel, Back-Channel Logout; Session Management | Logout and session | 2 | OpenID Certified logout profiles |
| JARM | Signed authorization responses | 5 | — |
| CIBA Core | Decoupled authentication | 5 | OpenID Certified FAPI-CIBA |
| FAPI 1.0 Advanced, FAPI 2.0 Security Profile, FAPI 2.0 Message Signing | Financial-grade APIs | 5 | OpenID Certified FAPI |
| Grant Management for OAuth 2.0 | Consent lifecycle (open banking) | 5 | — |
| Shared Signals Framework, CAEP, RISC | Continuous access evaluation and account-security signals | 4 | SSF interop events |
| AuthZEN Authorization API 1.0 | Standard PEP ↔ PDP interface | 3 | AuthZEN interop |
| OpenID Connect for Identity Assurance | Verified claims (KYC) | 5 | — |
| OpenID4VCI / OpenID4VP | Issue and verify verifiable credentials | 6 | — |
| OpenID Federation 1.0 | Multilateral trust | 6 | — |
| IPSIE (in progress) | Interoperability profile for enterprise identity (SSO, lifecycle, sessions, signals) | Track; adopt as published | — |

### 1.4 Other identity standards

| Standard | Body | Phase |
|----------|------|-------|
| SAML 2.0 (Web SSO, SLO, metadata, HTTP-Redirect/POST/Artifact bindings) | OASIS | 2 |
| SCIM 2.0 (RFC 7642 / 7643 / 7644) + Enterprise User extension | IETF | 2 |
| LDAP v3 (RFC 4511) | IETF | 2 |
| WebAuthn Level 3; CTAP 2.x; FIDO Metadata Service (MDS3) | W3C / FIDO Alliance | 1 |
| TOTP (RFC 6238), HOTP (RFC 4226) | IETF | 1 |
| WS-Federation 1.2 | OASIS | 5 |
| Kerberos (RFC 4120), SPNEGO (RFC 4559) | IETF | 5 |
| RADIUS (RFC 2865) | IETF | 5 |
| X.509 / PKIX (RFC 5280), OCSP (RFC 6960) | IETF | 5 |
| SPIFFE / SVID | CNCF | 5 |
| UMA 2.0 | Kantara | 5 (healthcare pack) |
| SMART App Launch v2 (on FHIR) | HL7 | 5 |
| LTI 1.3 | 1EdTech | 5 |
| SD-JWT and SD-JWT VC; ISO/IEC 18013-5 / 18013-7 (mDL) | IETF / ISO | 6 |
| XACML 3.0 (import and compatibility only) | OASIS | 6 |
| Model Context Protocol authorization | MCP specification | 2 |

### 1.5 Integration and operations standards

| Standard | Use |
|----------|-----|
| OpenAPI 3.2, JSON Schema 2020-12 | Contracts for every public API and configuration object ([10](10-api-standards.md)) |
| AsyncAPI 3.1.0, Arazzo 1 | Event and webhook contracts; multi-step API workflows |
| RFC 7396, RFC 9745, RFC 8594 | JSON Merge Patch; `Deprecation` and `Sunset` headers |
| RFC 9457 (Problem Details) | API error format |
| CloudEvents 1.0 | Event and webhook envelope |
| Standard Webhooks | Webhook signing and verification |
| OCSF | Audit and security event schema for SIEM export |
| OpenTelemetry | Traces, metrics, logs |
| SLSA, SBOM (SPDX / CycloneDX), Sigstore | Supply-chain security of our builds |
| OWASP ASVS 5.0 | Secure-coding verification baseline (Level 3 for identity modules) |
| RFC 8785 (JCS), RFC 3161, signed tree heads (as in RFC 9162) | Audit hash chain, trusted timestamps and checkpoints ([13](13-audit-and-change-tracking.md)) |
| NIST SP 800-53 AU family, NIST SP 800-92 | Audit content, protection, non-repudiation, retention and log management |
| WCAG 2.2 AA | Accessibility of every user interface |

### 1.6 Authorization models and references

- NIST RBAC (ANSI INCITS 359): core, hierarchical and constrained RBAC
- NIST SP 800-162: attribute-based access control
- Google Zanzibar (USENIX ATC 2019): relationship-based access control
- Cedar policy language: analyzable policy-as-code
- Common Expression Language (CEL): safe expressions for conditions

## 2. Assurance frameworks

| Framework | What we implement | Phase |
|-----------|-------------------|-------|
| NIST SP 800-63-4 | IAL1–2 (with proofing providers), AAL1–3, FAL1–3; assurance levels exposed in `acr` and policy context | 2→5 |
| eIDAS 2.0 / EU Digital Identity Wallet | Low, substantial and high levels of assurance; wallet presentation | 6 |
| ISO/IEC 29115 | Entity authentication assurance mapping | 5 |
| PSD2 RTS on SCA | Two independent factors, dynamic linking, exemptions engine | 5 |
| Zero Trust (NIST SP 800-207) | Continuous evaluation, device and risk signals, least privilege | 4 |

## 3. Regulations: requirement → platform capability

| Regulation | Key requirement | Capability (feature IDs) |
|------------|-----------------|--------------------------|
| GDPR (EU), UK GDPR | Consent, data subject rights, residency, minimization | GOV-07, TEN-05, IDM-01 PII flags, SEC-08 crypto-shredding |
| CCPA/CPRA (California) | Access, deletion, opt-out | GOV-07 |
| India DPDP Act 2023 | Consent records, data principal rights, children's data | GOV-07, IDM-17 |
| Brazil LGPD, others | Consent and rights | GOV-07 |
| HIPAA (US) | Access control, audit, unique user ID, automatic logoff | AUTHZ, GOV-01, SESS-01, BAA |
| PCI DSS v4.0.x | Requirement 8: MFA for cardholder-data access, password rules, lockout. Requirement 10: log and protect all access | AUTH-01/20, SEC-01, GOV-01, GOV-08 |
| PSD2 / PSD3 (EU), UK Open Banking | SCA, dynamic linking, consent | AUTH-21, OAUTH-14, IND-03 |
| SOX | Access reviews, separation of duties | GOV-04, GOV-06 |
| COPPA (US), age-appropriate design codes | Parental consent, age assurance | IDM-17 |
| FedRAMP, CJIS, StateRAMP | Federal controls, FIPS crypto, PIV | SEC-12, AUTH-10, IND-06 |
| DORA (EU financial sector) | ICT risk, incident reporting, resilience testing | OPS, incident process, exit plan (MIG-04) |
| NIS2 (EU) | Security measures, incident reporting | Security program |

## 4. Company certifications and attestations

| Certification | Target | Notes |
|---------------|--------|-------|
| SOC 2 Type I | At public GA (about week 36) | Controls designed from Phase 0 |
| SOC 2 Type II | End of Phase 4 (about week 56) | First observation window of 3 months starts at GA; then annual 12-month windows; continuous evidence through a compliance-automation tool |
| ISO/IEC 27001:2022 | Phase 4 | Add 27017 (cloud), 27018 (PII in cloud), 27701 (privacy) |
| HIPAA attestation + BAA | Phase 4 | Required for the healthcare pack |
| PCI DSS service provider | Phase 5 | If tenants place us in cardholder-data scope |
| CSA STAR Level 2 | Phase 5 | Cloud trust signal |
| OpenID Certified | Phases 1–5, per profile | Run the OpenID conformance suite in CI from Phase 1 |
| FIDO Certified (server) | Phase 2 | Passkey credibility |
| FIPS 140-3 validated crypto | Phase 5 | Use a validated module |
| FedRAMP Moderate | Phase 6+ | Long lead time; needs a dedicated government environment |
| Regional schemes (C5, ENS, IRAP, MeitY empanelment) | On demand | Driven by sales pipeline |

## 5. Industry packs

A pack is **configuration, not code**: flow templates, policy and
authorization-model templates, claim profiles, consent models, report
templates, and documentation that maps to the relevant controls. Packs prove
the "100% dynamic" claim: if a vertical needs new code, that code becomes a
generic engine feature first.

| Pack | Contents |
|------|----------|
| **B2B SaaS** (IND-01) | Organizations with RBAC, invitations, per-org SSO/SCIM, admin portal, domain capture, audit streaming |
| **Retail & e-commerce** (IND-02) | Passwordless and social, guest checkout to account, progressive profiling, marketing consent, bot and fraud defense |
| **Banking & open banking** (IND-03) | FAPI 2.0, PAR, JAR/JARM, DPoP or mTLS, CIBA, PSD2 SCA with dynamic linking and exemptions, RAR consents and Grant Management, mandate and account authorization models, profiles for UK Open Banking, Berlin Group, Brazil Open Finance, Australia CDR |
| **Insurance** (IND-04) | Agent and broker delegation, policyholder and beneficiary relationships, claims step-up |
| **Healthcare** (IND-05) | SMART App Launch v2 on FHIR, patient/practitioner/proxy relationships, UMA consent, break-glass with justification, HIPAA reports, EPCS-style two-factor signing |
| **Government** (IND-06) | NIST 800-63 IAL/AAL/FAL profiles, PIV/CAC, FIPS mode, national eID connectors (OIDC/SAML), citizen and delegate (power of attorney) models |
| **Telecom** (IND-07) | CIBA for call centers, SIM-based verification and SIM-swap risk (CAMARA), subscriber/account-holder/family models, dealer and partner portals |
| **Education** (IND-08) | LTI 1.3, rostering sync, student/guardian/teacher models, parental consent, OpenID Federation for federations |
| **Media, gaming & streaming** (IND-09) | Device flow for consoles and TVs, household profiles, concurrent-stream limits through sessions, parental controls, age assurance |
| **IoT, automotive & manufacturing** (IND-10) | Device certificates, device flow, SPIFFE for factory workloads, vehicle owner/driver/fleet delegation |
| **AI agents & API platforms** (IND-11) | MCP authorization, agent identities, delegation chains, human-in-the-loop approvals, API keys, usage quotas |
| **Travel & hospitality** (IND-12) | Loyalty-account linking, partner federation, family booking delegation |
