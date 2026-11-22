# 23 — SDKs: Using the Platform in Your Code

Developers use the platform from their own code through SDKs, UI components,
framework integrations, a CLI and Terraform. Everything is also plain standards
(OIDC, OAuth, SAML, SCIM, AuthZEN), so any certified standard library works too:
the SDKs make it easier, they never lock anyone in.

## 1. SDK families

| Family | Runs in | What it does |
|--------|---------|--------------|
| **Client SDKs** | Browsers, single-page apps, iOS, Android, Flutter, React Native, desktop, game engines, devices | Sign-in (hosted, embedded or headless), passkeys, sessions, secure token storage, refresh, sign-out, step-up, rendering server-driven screens natively |
| **UI components** | Web (web components with React, Vue, Angular and Svelte wrappers), native mobile UI kits | Ready-made, themeable sign-in, sign-up, profile, organization switcher and `<Can>` permission helpers |
| **Server SDKs and framework integrations** | Back ends and API gateways | Token verification, sessions for web apps and BFF, route and resource authorization, data filters, decision reporting to the audit trail |
| **Authorization SDKs** | Any service | Check, batch check and search APIs (AuthZEN), relationship writes, local sidecar or in-process engine |
| **AI and MCP SDKs** | AI agents, MCP servers, agent frameworks | MCP guard, per-tool checks, permission-filtered retrieval, agent identities and delegation ([14 §4](14-integration-and-enforcement.md#4-where-authorization-happens)) |
| **Management SDKs** | Automation, back-office tools, CI/CD | Everything in the Management API: users, organizations, roles, settings, templates, reset, audit search |
| **Tools** | Developers' machines and pipelines | CLI, Terraform provider, local emulator, test kit, code generation |

## 2. Languages and frameworks

| Area | Languages and frameworks |
|------|--------------------------|
| **Web front ends** | TypeScript/JavaScript core; React, Next.js, Vue, Nuxt, Angular, Svelte and SvelteKit, Astro, React Router; web components for anything else |
| **Mobile and desktop** | Swift (iOS, macOS), Kotlin (Android), Flutter, React Native (including Expo), .NET MAUI, Electron |
| **Back ends** | Node.js (Express, Fastify, NestJS, Hono), Go (net/http, Gin, Echo, gRPC), Java and Kotlin (Spring Boot, Quarkus, Micronaut, Ktor), Python (Django, FastAPI, Flask), .NET (ASP.NET Core, Blazor), PHP (Laravel, Symfony), Ruby (Rails, Sinatra), Rust (Axum, Actix) |
| **Edge and serverless** | Cloudflare Workers, Vercel and Netlify edge functions, AWS Lambda, Azure Functions, Google Cloud Run functions, Deno, Bun |
| **Games** | Unity, Unreal Engine, with device-flow sign-in for consoles and TVs |
| **Devices and IoT** | C and Rust libraries for constrained devices: device flow, certificate identity, token handling |
| **AI agents** | Python and TypeScript integrations for popular agent frameworks and the official MCP SDKs |

The exact framework list at GA is confirmed with design partners; new
integrations are thin adapters over each language's core SDK.

## 3. What using it looks like

The examples show the intended developer experience; the exact APIs are
designed during the build.

**Sign-in in a web app (React):**

```tsx
<PortunProvider domain="login.acme.com" clientId="app_01JD...">
  <App />
</PortunProvider>

function Header() {
  const { user, signIn, signOut } = useAuth();
  return user
    ? <UserMenu user={user} onSignOut={signOut} />
    : <button onClick={() => signIn({ passkey: 'preferred' })}>Sign in</button>;
}
```

**Protecting an API (Python, FastAPI):**

```python
authz = Portun(issuer="https://login.acme.com", audience="https://api.acme.com")

@app.post("/invoices/{invoice_id}/approve")
def approve(invoice_id: str, user = Depends(authz.require(P.api.invoices.approve, resource=("invoice", "invoice_id")))):
    ...
```

**Automating administration (TypeScript, Management SDK):**

```ts
const admin = new ManagementClient({ environment: 'env_01HZ9R' }); // credentials from the environment
const user = await admin.users.create({ email: 'new.hire@globex.com', organizationId: 'org_globex' });
await admin.roleAssignments.create({ userId: user.id, roleId: 'rol_finance_manager' });
```

**Signing in on iOS (Swift):**

```swift
let session = try await Portun.shared.signIn(presenting: window) // passkey first, hosted or native screens
let invoices = try await api.get("/invoices", token: session.accessToken)
```

## 4. Consistent behavior in every SDK

Each SDK has a core generated from the OpenAPI and protobuf contracts and a
hand-written, idiomatic layer on top (D-17). Every SDK behaves the same way:

| Concern | Behavior |
|---------|----------|
| Security defaults | PKCE always; tokens kept in the platform's secure storage (Keychain, Android Keystore) and never in browser local storage (BFF for SPAs); DPoP keys generated and stored automatically; refresh-token rotation handled; app attestation on mobile |
| Token verification | Local and fast, with cached JWKS and automatic key rotation; accepts a list of trusted issuers for domain migrations ([17 §6](17-white-label-domains.md#6-changing-domains-safely)) |
| Reliability | Timeouts, retries with backoff only where safe, idempotency keys on writes, pagination helpers |
| Errors | RFC 9457 problem details turned into typed errors with stable codes |
| Observability | OpenTelemetry traces and metrics hooks; logs with secrets redacted; no telemetry sent to us unless the customer opts in |
| Configuration | Environment variables or code, with the same names everywhere |
| Localization | UI components follow the language order in [21 §2](21-localization.md#2-choosing-the-language) |
| Audit | Authorization decisions made by the SDK are reported to the audit trail (AUTHZ-32) |

**One conformance suite for all SDKs:** the same scenarios (sign-in, refresh,
rotation, step-up, errors, authorization checks, retries) run against the local
emulator for every SDK in CI, so all languages behave identically. Client
libraries also pass OpenID relying-party certification where it applies.

## 5. Typed permissions in code

The CLI generates typed permission constants from the customer's permission
catalog ([14 §2.1](14-integration-and-enforcement.md#21-permission-catalog-module--component--action)):

```bash
portunctl codegen permissions --lang typescript --out src/permissions.ts
```

Code then uses `P.api.invoices.approve` instead of the string
`"api:invoices:approve"`, so typos fail at compile time. Re-running the
generator after catalog changes shows exactly which code is affected. SDKs
also report permission keys used in code that are missing from the catalog
(AUTHZ-30).

## 6. Testing customers' own code

Every SDK ships a **test kit**, so customers can test their own applications
without network calls:

- mint test tokens with any claims, signed by a local test key;
- a fake authorization engine with "allow" and "deny" rules for unit tests;
- fixtures for users, organizations and roles;
- the local emulator (one container image) for integration tests in their CI.

## 7. Distribution, versions and trust

- **Published where developers look:** npm, Maven Central, PyPI, NuGet, Go
  modules, Swift Package Manager, Gradle, pub.dev, Packagist, RubyGems,
  crates.io, the Unity Asset Store and Unreal Marketplace.
- **Proprietary license** (D-08): SDKs are free to use with the platform under
  our license; they are not open source.
- **Our own registry where needed:** directories that only accept open-source
  code (for example the WordPress plugin directory) are replaced by our own
  download site, and the Terraform provider is served from our own registry.
- **Signed releases** with provenance and an SBOM for each SDK release; exact
  pinned dependencies, kept to a minimum (D-15,
  [08 §1](08-engineering-standards.md#1-version-policy-pinned-everywhere-upgrades-on-purpose)).
- **Semantic versioning**; each major version supported for at least 24 months
  after the next one ships ([18 §5](18-operating-the-service.md#5-releases-and-support-after-ga)).
- **Docs** for every SDK: quick-starts, API reference generated from code,
  sample apps, and material for AI coding assistants (DX-08).

## 8. Embedding the platform itself

| Need | Option |
|------|--------|
| Authorization decisions inside the application process | **Embedded engine:** a Go library, and a WebAssembly build for other languages, kept in sync with the tenant's model and data (AUTHZ-12) |
| Low-latency decisions next to a service | **Sidecar** next to the service ([14 §6](14-integration-and-enforcement.md#6-where-the-answer-comes-from)) |
| A software vendor shipping identity inside its own product (OEM) | **OEM embedding kit:** the all-in-one engine packaged to run alongside the vendor's product, with the Management SDK, white-label and licensing (OPS-15) |
| A team that wants everything in its own infrastructure | **Self-hosted edition** ([12 §7](12-containers-and-deployment.md#7-self-hosted-packaging)) |

Running the full identity server as a library inside an application process
is deliberately not offered: identity needs its own security updates,
isolation and operations. The sidecar and OEM kit give the same convenience
without those risks.

## 9. Feature IDs

| ID | Capability |
|----|-----------|
| DX-02 | SDKs and framework integrations for the languages and frameworks in §2 |
| DX-09 | Shared SDK behavior: security defaults, local verification, reliability, typed errors, OpenTelemetry hooks, cross-SDK conformance suite, signed open-source releases |
| DX-10 | Test kit for customers' code: test tokens, fake authorization engine, fixtures, emulator integration |
| DX-11 | Typed permission constants generated from the permission catalog |
| DX-12 | Edge and serverless runtime support (Cloudflare Workers, Vercel and Netlify edge, AWS Lambda, Azure Functions, Cloud Run functions, Deno, Bun) |
| DX-13 | Game-engine (Unity, Unreal) and device SDKs (C and Rust for constrained IoT devices) |
| DX-14 | OEM embedding kit: the all-in-one engine packaged to run with a vendor's product, plus Management SDK, white-label and licensing |
