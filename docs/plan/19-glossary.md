# 19 — Glossary

Terms used across the plan, in alphabetical order within each group.

## Platform and tenancy

| Term | Meaning |
|------|---------|
| **Application** | A customer's app registered with the platform (web, mobile, API, machine, MCP server or agent) |
| **Cell** | An independent copy of the data plane (services and databases) serving a group of tenants; limits the blast radius of failures |
| **Control plane** | The part that handles administration and configuration (console, Management API, compilation) |
| **Data plane** | The part that serves live traffic: logins, tokens and authorization decisions |
| **Design partner** | An early customer who uses pre-release builds and gives feedback before GA |
| **Environment** | A separate configuration space within a tenant, such as dev, stage or prod |
| **GA (general availability)** | The single release in which all catalog features ship |
| **Organization** | A business customer of our tenant (for example Globex inside Acme); can be nested into sub-organizations |
| **Partner** | A reseller or ISV that offers the platform, often white-labeled, to its own tenants |
| **Tenant** | Our customer (for example Acme), with its own isolated data and configuration |
| **Workstream** | One of the 13 build teams, each owning sections of the feature catalog |

## Configuration and hierarchy

| Term | Meaning |
|------|---------|
| **Merge rule** | How values from different levels combine: most specific, strictest, intersection, union or ceiling |
| **Override policy** | What a parent lets its children do with a setting: `overridable`, `bounded`, `additive` or `locked` |
| **Runtime bundle** | The compiled, immutable form of a tenant's configuration that the data plane loads for speed |
| **Setting mode** | Where a setting's value comes from: **Default** (product default), **Parent** (live from the level above), **Template** (pushed template versions) or **Own** (set at this level) |
| **Static stability** | The data plane keeps working with its last known configuration and keys when other components fail |
| **Template push** | Sending a new template version down to linked children: automatic, staged, opt-in, manual or forced |

## Authentication

| Term | Meaning |
|------|---------|
| **AAL / IAL / FAL** | NIST assurance levels for authentication, identity proofing and federation strength |
| **CIBA** | Client-Initiated Backchannel Authentication: approving a login or action on another device, such as a phone |
| **FedCM** | A browser feature for federated login that works without third-party cookies |
| **MFA** | Multi-factor authentication: two or more independent factors |
| **Passkey / WebAuthn** | Phishing-resistant sign-in with a key stored on the user's device or synced by their platform |
| **Relying-party ID** | The domain a passkey is bound to |
| **Step-up** | Asking for stronger authentication before a sensitive action |

## Protocols and standards

| Term | Meaning |
|------|---------|
| **DPoP / mTLS-bound tokens** | Tokens tied to a key or certificate, so a stolen token cannot be reused |
| **FAPI 2.0** | High-security OAuth profile used in banking and open banking |
| **JWT / JWKS** | Signed JSON tokens; the published set of public keys used to verify them |
| **MCP** | Model Context Protocol, used by AI applications to call tools; secured with OAuth |
| **OAuth 2.1 / OIDC** | Standards for delegated access (OAuth) and sign-in on top of it (OpenID Connect) |
| **PAR / JAR / RAR** | OAuth extensions for pushed, signed and detailed authorization requests |
| **PKCE** | A protection that stops stolen authorization codes from being used |
| **SAML 2.0** | An older XML-based standard for enterprise single sign-on |
| **SCIM 2.0** | A standard API for creating, updating and removing users and groups automatically |
| **Shared Signals (SSF, CAEP, RISC)** | Standards for sharing security events such as "session revoked" in real time |

## Authorization

| Term | Meaning |
|------|---------|
| **ABAC** | Attribute-based access control: decisions based on attributes of the user, resource and context |
| **AuthZEN** | OpenID standard API between enforcement points and decision points |
| **Cedar** | An analyzable policy language used for guardrail rules (`permit` and `forbid`) |
| **CEL** | Common Expression Language: safe, fast expressions used for conditions and mappings |
| **Consistency token** | A marker that makes a check at least as fresh as a given change (a "zookie" in Zanzibar) |
| **PBAC** | Policy-based access control: rules written as policies |
| **PDP / PEP / PIP** | Policy decision point (decides), policy enforcement point (enforces in the app), policy information point (supplies attributes) |
| **Permission catalog** | The customer's list of modules → components → actions, for example `api:invoices:approve` |
| **RBAC** | Role-based access control: permissions grouped into roles |
| **ReBAC** | Relationship-based access control: access derived from relationships, as in Google's Zanzibar |
| **Relationship tuple** | A stored fact such as "Alice is a viewer of document 42" |
| **Separation of duties (SoD)** | Rules that stop one person from holding or using conflicting permissions |
| **Sidecar (edge PDP)** | A local decision point running next to the customer's service for sub-millisecond checks |

## Audit and security

| Term | Meaning |
|------|---------|
| **BYOK / HYOK** | Bring your own key / hold your own key: the customer controls the encryption key |
| **CDC** | Change data capture: reading every database change from the write-ahead log |
| **Checkpoint** | A signed summary of audit events that proves nothing before it was changed |
| **Crypto-shredding** | Making data unreadable by destroying its encryption key |
| **Four-eyes approval** | A change that needs a second person to approve it |
| **Hash chain** | Each audit record includes a hash of the previous one, so any change is detectable |
| **KMS / HSM** | Key management service / hardware security module that protects keys |
| **Outbox** | A table written in the same transaction as a change, so its event or audit record can never be lost |
| **WORM storage** | Write-once storage that cannot be altered or deleted before its retention ends |

## Engineering and operations

| Term | Meaning |
|------|---------|
| **ADR** | Architecture decision record: a short document explaining a significant decision |
| **Design tokens** | Named design values (colors, spacing, effects) shared by every UI surface |
| **Locale** | A language and region combination (for example `fr-CA`) that decides language and formats |
| **Practice mode** | A sample environment where admins can try every feature without touching real data |
| **Pseudo-localization** | Automatically altered text (longer, accented, mirrored) used to find layout and translation problems before real translation |
| **LTS** | Long-term support release of the self-hosted edition |
| **RPO / RTO** | How much data may be lost / how long recovery may take after a disaster |
| **RTL** | Right-to-left scripts such as Arabic and Hebrew, which need mirrored layouts |
| **SBOM** | Software bill of materials: the exact list of components and versions in a release |
| **Server-driven UI** | Screens described as data by the server and rendered by web and native clients |
| **SLA / SLO** | Contractual service level / internal reliability target |
| **SLSA** | A framework for supply-chain integrity of builds |
| **Trust center** | Public site with security, compliance and privacy information for buyers and auditors |
