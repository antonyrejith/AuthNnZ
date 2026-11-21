# 03 — Standards, Compliance & Industry Packs

Standards conformance is a product feature: it lets customers integrate
anything and leave whenever they want, and it is what regulated buyers check
first. Every standard listed here is implemented in the first release (see the
[delivery plan](05-roadmap-and-delivery.md)), together with the certification
we will pursue where one exists. Standards still in draft are implemented at
their current draft and updated when they become final.

## 1. Identity protocol standards

### 1.1 OAuth 2.x (IETF)

| Standard | Purpose |
|----------|---------|
| RFC 6749 / 6750 | OAuth 2.0 framework and bearer tokens |
| OAuth 2.1 (draft) | Consolidated baseline: PKCE everywhere, no implicit or password grant |
| RFC 7636 | PKCE |
| RFC 8414 | Authorization server metadata |
| RFC 7662 / 7009 | Token introspection / revocation |
| RFC 9068 | JWT profile for access tokens |
| RFC 8707 | Resource indicators |
| RFC 9207 | Authorization server issuer identification (mix-up defense) |
| RFC 9700 | OAuth 2.0 Security Best Current Practice |
| RFC 8252 | OAuth for native apps |
| RFC 7591 / 7592 | Dynamic client registration and management |
| RFC 8628 | Device authorization grant |
| RFC 8693 | Token exchange |
| RFC 7521 / 7522 / 7523 | Assertion framework; SAML and JWT bearer grants |
| RFC 9126 | Pushed authorization requests (PAR) |
| RFC 9470 | Step-up authentication challenge |
| RFC 9728 | Protected resource metadata (used by MCP) |
| OAuth client ID metadata document (draft) | URL-based client IDs (used by MCP) |
| RFC 9449 | DPoP |
| RFC 8705 | Mutual-TLS client authentication and certificate-bound tokens |
| RFC 9101 | JWT-secured authorization request (JAR) |
| RFC 9396 | Rich authorization requests (RAR) |
| Transaction tokens (draft) | Call-chain tokens inside microservice meshes |
| Token status list (draft) | Revocation status for JWT/SD-JWT tokens and credentials |

### 1.2 JOSE / JWT (IETF)

RFC 7515 (JWS), 7516 (JWE), 7517 (JWK), 7518 (JWA), 7519 (JWT), 7638 (JWK
thumbprint), 8037 (EdDSA), 8725 (JWT best current practices).

### 1.3 OpenID Foundation

| Specification | Purpose | Certification |
|---------------|---------|---------------|
| OpenID Connect Core 1.0, Discovery, Dynamic Registration | Identity layer | OpenID Certified (Basic, Config, Dynamic and Form Post OP profiles) |
| RP-Initiated, Front-Channel, Back-Channel Logout; Session Management | Logout and session | OpenID Certified logout profiles |
| JARM | Signed authorization responses | — |
| CIBA Core | Decoupled authentication | OpenID Certified FAPI-CIBA |
| FAPI 1.0 Advanced, FAPI 2.0 Security Profile, FAPI 2.0 Message Signing | Financial-grade APIs | OpenID Certified FAPI |
| Grant Management for OAuth 2.0 | Consent lifecycle (open banking) | — |
| Shared Signals Framework, CAEP, RISC | Continuous access evaluation and account-security signals | SSF interop events |
| AuthZEN Authorization API 1.0 | Standard PEP ↔ PDP interface | AuthZEN interop |
| OpenID Connect for Identity Assurance | Verified claims (KYC) | — |
| OpenID4VCI / OpenID4VP | Issue and verify verifiable credentials | — |
| OpenID Federation 1.0 | Multilateral trust | — |
| IPSIE (in progress) | Interoperability profile for enterprise identity (SSO, lifecycle, sessions, signals) | — |

### 1.4 Other identity standards

| Standard | Body |
|----------|------|
| SAML 2.0 (Web SSO, SLO, metadata, HTTP-Redirect/POST/Artifact bindings) | OASIS |
| SCIM 2.0 (RFC 7642 / 7643 / 7644) + Enterprise User extension | IETF |
| LDAP v3 (RFC 4511) | IETF |
| WebAuthn Level 3; CTAP 2.x; FIDO Metadata Service (MDS3) | W3C / FIDO Alliance |
| TOTP (RFC 6238), HOTP (RFC 4226) | IETF |
| WS-Federation 1.2 | OASIS |
| Kerberos (RFC 4120), SPNEGO (RFC 4559) | IETF |
| RADIUS (RFC 2865) | IETF |
| X.509 / PKIX (RFC 5280), OCSP (RFC 6960) | IETF |
| SPIFFE / SVID | CNCF |
| UMA 2.0 | Kantara |
| SMART App Launch v2 (on FHIR) | HL7 |
| LTI 1.3 | 1EdTech |
| SD-JWT and SD-JWT VC; ISO/IEC 18013-5 / 18013-7 (mDL) | IETF / ISO |
| XACML 3.0 (import and compatibility only) | OASIS |
| Model Context Protocol authorization | MCP specification |

### 1.5 Integration and operations standards

| Standard | Use |
|----------|-----|
| OpenAPI 3.2, JSON Schema 2020-12 | Contracts for every public API and configuration object ([10](10-api-standards.md)) |
| AsyncAPI 3.1.0, Arazzo 1 | Event and webhook contracts; multi-step API workflows |
| ACME (RFC 8555) | Automatic certificates for customer domains ([17](17-white-label-domains.md)) |
| SPF (RFC 7208), DKIM (RFC 6376), DMARC (RFC 7489), BIMI | Authenticated, branded email from customer sender domains |
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

| Framework | What we implement |
|-----------|-------------------|
| NIST SP 800-63-4 | IAL1–2 (with proofing providers), AAL1–3, FAL1–3; assurance levels exposed in `acr` and policy context |
| eIDAS 2.0 / EU Digital Identity Wallet | Low, substantial and high levels of assurance; wallet presentation |
| ISO/IEC 29115 | Entity authentication assurance mapping |
| PSD2 RTS on SCA | Two independent factors, dynamic linking, exemptions engine |
| Zero Trust (NIST SP 800-207) | Continuous evaluation, device and risk signals, least privilege |

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

| Certification | Target (weeks from kickoff) | Notes |
|---------------|-----------------------------|-------|
| SOC 2 Type I | Week 64 (start of hardening) | Controls designed and operating from the foundations stage |
| SOC 2 Type II | At GA (week 90) | 3-month observation window during hardening; then annual 12-month windows; continuous evidence through a compliance-automation tool |
| ISO/IEC 27001:2022 | At GA | Add 27017 (cloud), 27018 (PII in cloud), 27701 (privacy) |
| HIPAA attestation + BAA | At GA | Required for the healthcare pack |
| PCI DSS service provider | At GA | If tenants place us in cardholder-data scope |
| CSA STAR Level 2 | At GA | Cloud trust signal; builds on SOC 2 and ISO 27001 |
| OpenID Certified | Before GA, every profile | The OpenID conformance suite runs in CI from the first weeks |
| FIDO Certified (server) | Before GA | Passkey credibility |
| FIPS 140-3 validated crypto | At GA | FIPS mode uses a validated cryptographic module |
| FedRAMP Moderate | Readiness at GA; authorization after GA | Authorization is an external government process that typically takes a year or more; it needs a dedicated government environment |

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
