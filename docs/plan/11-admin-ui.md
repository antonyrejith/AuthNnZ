# 11 — User Interfaces & Configuration Screens

Everything in the platform is configured through screens. Every screen is
built on the public Management API ([10](10-api-standards.md)), so anything
done in the UI can also be done through the API, the CLI, Terraform or YAML.

## 1. The five user interfaces

| UI | Users | Purpose |
|----|-------|---------|
| **Admin console** | Our customers' admins and developers | Configure everything for a tenant and its environments |
| **Organization admin portal** | IT admins at our customers' business customers (B2B) | Set up their company's SSO, SCIM, domains, members and policies |
| **Account portal** | End users | Manage profile, security, sessions, devices, consents and privacy |
| **Hosted login** | End users | Sign-up, login, MFA, recovery, consent, device and approval screens |
| **Platform console** | Our own operators and resellers | Manage tenants, plans, cells, feature flags, incidents and support access |

## 2. Shared UX patterns (all configuration screens)

1. **Draft → publish bar.** Every configuration screen edits a draft. A bar at
   the top shows the state (*Draft · 3 changes · last published v41 by j.doe*)
   with **Validate**, **Simulate**, **Diff**, **Publish** and **Roll back**.
2. **View as code.** A toggle shows the same object as YAML, JSON, a Terraform
   resource or a `curl` call, and changes made in code mode are reflected back in
   the form.
3. **Inline validation** from the same JSON Schemas the API uses, with
   type-checked CEL and policy editors (autocomplete, errors as you type).
4. **Simulate before publish.** "Test this flow as user X from country Y", "What
   changes if I publish this policy?", "Explain this decision".
5. **History on every object.** Who changed what, when, from which channel
   and why, with a diff, one-click restore and an "as of" date picker to see
   the object at any past moment.
6. **Inheritance badges.** Every setting shows where its value comes from
   (*inherited from Acme*, *overridden here*, *locked by platform*, *from
   template v4*, *shared*), with "revert to inherited" and a diff against the
   parent ([15](15-hierarchy-and-shared-settings.md)).
7. **Reason prompts.** Sensitive actions (delete, impersonate, weaken a policy,
   break-glass) ask for a reason and optional ticket reference, recorded in the
   audit log.
8. **Environment switcher** (dev / stage / prod) always visible; production is
   color-coded and sensitive changes can require a second approver.
9. **Permission-aware.** Admins only see and can only change what their admin
   role allows; read-only views show why something is locked (for example
   "enforced by platform policy").
10. **Search and command palette** (`Ctrl/⌘ K`) across users, apps, settings and docs.
11. **Accessible and international:** WCAG 2.2 AA, keyboard navigation, screen
   reader labels, 40+ languages, right-to-left layouts, light and dark themes.
12. **Help in context:** each screen links to docs, quick-starts and example
    configurations.

## 3. Admin console: screen inventory

### 3.1 Home

| Screen | What it shows or configures |
|--------|-----------------------------|
| Dashboard | Logins, sign-ups, MFA adoption, failures, risk events, active users, usage against plan; setup checklist; alerts |
| Getting started | Guided setup: create first app → choose login methods → brand the login page → test login → go live |

### 3.2 Applications and APIs

| Screen | What it configures |
|--------|--------------------|
| Applications list | All apps by type, status, last login; create wizard by type: web app, single-page app, native/mobile, machine-to-machine, SAML service provider, MCP server or AI agent, device (TV/CLI) |
| Application › Settings | Name, logo, type, redirect and logout URLs, allowed origins (CORS), grant types, PKCE and token endpoint authentication |
| Application › Credentials | Client secrets, `private_key_jwt` keys, mTLS certificates; rotation |
| Application › Login experience | Which flow, which connections, branding override, organization behavior (none, optional, required) |
| Application › Tokens | Token format, lifetimes, refresh rotation, sender-constraint (DPoP/mTLS), **claim mapping editor (CEL)** with live preview of a sample token |
| Application › API access | Which APIs and scopes the app may request; consent settings |
| Application › Quick-start | Generated code for the app's stack with its real settings |
| APIs (resource servers) list and detail | Identifier (audience), scopes, token profile, signing algorithm, permission claims in tokens, M2M access rules |

### 3.3 Users and directory

| Screen | What it shows or configures |
|--------|-----------------------------|
| Users list | Search, filters, saved segments, bulk actions (block, delete, assign role, export) |
| User › Profile | Form generated from the **dynamic user schema**; verified identifiers; metadata |
| User › Security | Password status, passkeys, MFA factors, recovery codes; reset or revoke |
| User › Sessions and devices | Active sessions and trusted devices; revoke one or all |
| User › Access | Roles, groups, organizations, direct permissions and **effective permissions with their sources** |
| User › Linked identities | Social, enterprise and legacy identities; link, unlink, merge |
| User › Consents | Apps and agents with access; revoke |
| User › Activity | Timeline of logins, changes and risk events |
| User actions | Block, unblock, force password reset, reset MFA, impersonate (with reason, audited), export data, delete |
| Groups | Static and **dynamic (CEL rule)** groups; members; assigned roles |
| User schema | **Attribute builder**: name, type, validation, required, unique, PII flag, who can read and edit (user, admin, app); used by forms and tokens |
| Forms | Registration and profile forms built from the schema, per app or organization |
| Imports and exports | Bulk import with password-hash format; progress, errors; scheduled exports |
| Directory sync | Active Directory / LDAP agents, Google Workspace and Entra ID sync status |

### 3.4 Organizations (B2B)

| Screen | What it configures |
|--------|--------------------|
| Organizations list | Search, create, bulk invite |
| Organization › Overview | Name, display name, metadata, parent organization, plan or entitlements |
| Organization › Members and invitations | Members, roles in this organization, pending invitations |
| Organization › Connections | The organization's own SSO (SAML/OIDC) and SCIM endpoint |
| Organization › Domains | Verified domains; auto-join; enforce SSO for the domain |
| Organization › Policies | MFA, session, allowed login methods, IP allowlist for this organization |
| Organization › Branding | Logo, colors, login text overrides |
| Organization › Admin portal | Generate a setup link for the customer's IT admin; choose which tabs they see |

### 3.5 Authentication

| Screen | What it configures |
|--------|--------------------|
| Login methods | Enable and tune each method: password (policy, breached check), passkeys (attestation, autofill), email link/OTP, SMS/voice/WhatsApp OTP, TOTP, push, certificates, device flow |
| Social connections | Provider catalog; client IDs and secrets; scopes; attribute mapping; per-app enablement |
| Enterprise connections | SAML, OIDC, LDAP/AD, WS-Fed, Kerberos: metadata import, certificates, attribute and group mapping (CEL), JIT provisioning, test connection |
| MFA and step-up policies | Required factors per app, organization, segment or risk; enrollment grace period; phishing-resistant-only mode; remember device |
| Sessions and tokens | SSO session idle/absolute lifetimes, refresh token defaults, logout behavior, back-channel logout |
| **Flows** | List of flows per type (login, sign-up, recovery, step-up, consent) with the app/organization bindings |
| **Flow designer** | Visual canvas: node palette, drag-and-drop, branch conditions in CEL, per-node settings, version history, A/B split, **simulator and step debugger** |
| Hooks and Actions binding | Which Actions run at which hook point, in what order |

### 3.6 Authorization

| Screen | What it configures |
|--------|--------------------|
| **Permission catalog** | Modules (UI, API, AI, MCP, data, custom) as a tree of components and actions; import from OpenAPI, MCP tool lists and UI manifests; diff on re-import; "used in code but not in catalog" report; wildcard impact |
| **Roles and permissions** | Simple RBAC: permissions per API, roles as checklists of permissions, role inheritance, default roles; a role × permission matrix view |
| Role assignments | Assign roles to users, groups and organization members, with optional expiry |
| **Model editor** | Resource types, relations and permissions in the DSL with a live graph view; templates (SaaS RBAC, document sharing, org/team/repo, healthcare, banking) |
| Relationships explorer | Browse, add and remove relationship tuples; graph view from any user or resource |
| **Policies** | Cedar policy editor with validation, linting, policy tests and "only narrows access" analysis |
| **Playground** | Ask "Can X do Y on Z?" against draft or published configuration; see the explanation trace; "who can access Z?" and "what can X access?" |
| Access requests and JIT | Request types, approvers, maximum duration, break-glass rules |
| Decision logs | Search decisions by subject, resource, result; open the explanation |

### 3.7 Security

| Screen | What it configures |
|--------|--------------------|
| Attack protection | Brute-force thresholds, lockout, credential-stuffing defense, bot detection (CAPTCHA provider and when to challenge), breached-password response |
| Risk rules | Signals and weights, CEL rules, actions per risk band (allow, challenge, block, notify); test against recent traffic |
| Network | IP allow/deny lists, geo-blocking, trusted proxies |
| Rate limits | Per endpoint, client and tenant, within plan limits |
| Signing keys | Active, next and retired keys; algorithm; rotation schedule; emergency rotation; bring-your-own-key |
| Secrets | Secrets for Actions and connectors (write-only) |

### 3.8 Extensibility

| Screen | What it configures |
|--------|--------------------|
| Actions | Code editor (TypeScript, Go, Rust or Python compiled to WASM), secrets, dependencies, test runner with sample events, logs, versions |
| Webhooks | Endpoints, event types, signing secret, delivery log, retry and replay |
| Event streams | Kafka, Kinesis, EventBridge, Pub/Sub, Event Hubs destinations and filters |
| Connectors | Email, SMS, WhatsApp and push providers; CAPTCHA; KYC; SIEM; CRM — credentials, failover order, test send |

### 3.9 Branding and experience

| Screen | What it configures |
|--------|--------------------|
| Themes | Logo, colors, fonts, layout, dark mode, custom CSS; live preview on desktop and mobile |
| Custom domains | Add a domain, DNS instructions, automatic certificate status |
| Email and SMS templates | Per message type and language, with variables, preview and test send |
| Text and languages | Every hosted-page string per language; enabled languages; per-organization overrides |
| Hosted page preview | Walk through every login screen with the current theme and flow |

### 3.10 Governance and privacy

| Screen | What it shows or configures |
|--------|-----------------------------|
| Audit explorer | Search by actor, target, type, channel, outcome, time and text; diff viewer; follow a request by trace ID; saved queries; export |
| Access as-of | "Who had access to X on date Y?" and "What could user Z do on date Y?" |
| Audit alerts | Rules that notify on chosen audit events |
| Audit integrity | Chain and checkpoint verification status; download verification bundle for auditors |
| Retention and legal hold | Retention per data class within plan and regulatory floors; legal holds |
| Access transparency | Requests from our support staff to access tenant data: approve, deny, review what they did |
| Log streams | SIEM destinations, filters and delivery health |
| Analytics | Funnels (sign-up, login, MFA enrollment), failures by reason, method adoption, risk trends |
| Access reviews | Campaigns, reviewers, progress, automatic revocation |
| Consent and privacy | Terms and privacy-policy versions, consent purposes, data subject requests (export, deletion) |
| Reports | Compliance reports (SOC 2 evidence, PCI DSS requirement 8, HIPAA access) |

### 3.11 Releases and settings

| Screen | What it shows or configures |
|--------|-----------------------------|
| **Hierarchy explorer** | Tree of partners, tenants, environments, organizations and sub-organizations; search; drift and override markers |
| **Effective settings** | Any node's resolved settings with sources; diff against parent or template; revert overrides |
| **Override policies** | Per setting, what descendants may do: inherit, override, bounded (limits), additive, locked |
| **Shared library** | Objects shared with descendants; where-used; impact analysis before changes |
| **Templates and push** | Template versions, linked children, push wizard (mode, waves, conflict rule, preview and simulation), live push status, rollback, drift report |
| **Releases** | All drafts and published versions across the environment; diff; promote dev → stage → prod; approval queue; rollback; gradual rollout status; GitOps sync status |
| Environments | Create and manage environments; data residency (read-only after creation) |
| Admin team | Admins, admin roles (built-in and custom), scope per environment or organization, admin MFA requirements |
| Management API clients | Machine credentials for automation, with scopes |
| Billing and usage | Plan, usage per meter, invoices, limits and alerts |
| Tenant settings | Name, support contacts, defaults, data export, tenant deletion |

## 4. Organization admin portal

| Screen | Purpose |
|--------|---------|
| SSO setup wizard | Step-by-step guides with screenshots for Okta, Entra ID, Google Workspace, AD FS, Ping, OneLogin and generic SAML/OIDC; test connection before enabling |
| Directory sync (SCIM) setup | Endpoint and token, guides per IdP, sync status and errors |
| Domains | Verify domains through DNS |
| Members and roles | Invite, remove, assign organization roles |
| Security policies | MFA, sessions, allowed methods (within limits set by the tenant) |
| Audit log | Events for this organization only |

## 5. Account portal (end users)

| Screen | Purpose |
|--------|---------|
| Profile | Edit allowed attributes, verify email and phone |
| Security | Change password, add or remove passkeys and MFA factors, recovery codes |
| Sessions and devices | See where you are signed in; sign out a device or everywhere |
| Connected apps and agents | What each app or AI agent can access; revoke |
| Organizations | Switch, leave, accept invitations |
| Privacy | Consents, download my data, delete my account |

## 6. Hosted login screens

Rendered from the flow engine's server-driven UI, so the same screens work on
web, iOS and Android, in every theme and language:

Identifier entry · password · passkey (including autofill) · email/SMS code ·
magic link sent · MFA challenge (TOTP, push with number matching, security key) ·
MFA enrollment · sign-up form (from schema) · verify email or phone · forgot and
reset password · account recovery · consent · terms acceptance · organization
picker · account linking · enterprise SSO redirect · device code entry ·
CIBA / push approval · step-up prompt · blocked or risk challenge (CAPTCHA) ·
error · logout and logged-out.

## 7. Key screen layouts (wireframes)

### 7.1 Flow designer

```
┌─ Flows › login-default ──────────────────────────────────────────────── [dev ▾] ─┐
│ Draft · 2 changes · published v41   [Validate] [Simulate] [Diff] [Publish ▸]     │
├──────────────┬──────────────────────────────────────────────┬────────────────────┤
│ NODES        │                                              │ NODE: risk         │
│ ▸ Identify   │  (Identify) ──► (Passkey) ──┐                │ Type: risk.eval    │
│ ▸ Password   │      │                      ▼                │ Branches:          │
│ ▸ Passkey    │      ├──► (Password) ──► (Risk) ──► (MFA)    │ score>=80 → Deny   │
│ ▸ OTP        │      │                      ▲        │       │ score>=40 → MFA    │
│ ▸ Federate   │      └──► (Enterprise SSO) ─┘        ▼       │ else → Consent     │
│ ▸ Risk       │                          (Consent) ► ✓       │ [Test condition]   │
│ ▸ MFA        │                                              │                    │
│ ▸ Action     │  [Zoom] [Fit] [View as YAML]                 │ CEL ✓ type-checked │
├──────────────┴──────────────────────────────────────────────┴────────────────────┤
│ SIMULATOR  user: alice@acme.com  country: DE  device: new                        │
│ Path: Identify › Passkey › Risk (score 52) › MFA › Consent › ✓   [Step through]  │
└──────────────────────────────────────────────────────────────────────────────────┘
```

### 7.2 Roles and permissions

```
┌─ Authorization › Roles ─────────────────────────────────────────────── [prod ▾] ─┐
│ Draft · 1 change                                     [Simulate] [Publish ▸]      │
├────────────────────────┬──────────┬──────────┬──────────┬────────────────────────┤
│ Permission \ Role      │ Viewer   │ Editor   │ Admin    │ Billing (custom)       │
├────────────────────────┼──────────┼──────────┼──────────┼────────────────────────┤
│ invoice:read           │    ✓     │    ✓     │    ✓     │    ✓                   │
│ invoice:create         │          │    ✓     │    ✓     │                        │
│ invoice:approve        │          │          │    ✓     │    ✓ if ≤ limit        │
│ invoice:delete         │          │          │    ✓ *   │                        │
├────────────────────────┴──────────┴──────────┴──────────┴────────────────────────┤
│ * Guardrail: invoice:delete requires phishing-resistant MFA (Cedar policy)       │
│ Inherits: Editor ⊃ Viewer · Admin ⊃ Editor        [Add role] [Add permission]    │
└──────────────────────────────────────────────────────────────────────────────────┘
```

## 8. Front-end technology

| Concern | Choice |
|---------|--------|
| Console, organization portal, account portal | React + TypeScript, Vite, TanStack Router (typed routes) and TanStack Query |
| Hosted login renderer | Lit web components: small, fast, embeddable in any framework, themeable with CSS variables |
| Design system | `web/packages/ui`: Radix UI primitives + Tailwind CSS tokens; documented in Storybook |
| Forms | React Hook Form + Zod, with schemas generated from the API's JSON Schemas |
| Code editors | Monaco Editor with language support for CEL, the authorization DSL, Cedar, YAML and Action code |
| Graphs | XYFlow for the flow designer and relationship explorer; ECharts for analytics |
| API access | Client generated from OpenAPI; console sessions through a backend-for-frontend with `HttpOnly` cookies |
| Internationalization | i18next with ICU messages; translation files per locale |

Versions are pinned exactly, per [08 §1](08-engineering-standards.md#1-version-policy-pinned-everywhere-upgrades-on-purpose).

## 9. Delivery

Every screen in this document is part of the first release. The Experience
workstream (WS-10) builds the console shell, design system, portals and hosted
login; each feature workstream builds its screens with them, because a feature
is not done until its screens exist ([05](05-roadmap-and-delivery.md)).
