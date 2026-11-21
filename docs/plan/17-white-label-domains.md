# 17 — White-Label Domains & URLs

Every URL, email address, certificate and identifier that end users,
developers or administrators see can carry the customer's brand. With
white-labeling enabled, nothing visible reveals AuthNnZ unless the customer
chooses to show it.

## 1. What runs on the customer's domain

| Surface | Example URL |
|---------|-------------|
| Hosted login, sign-up, MFA, recovery, consent | `https://login.acme.com` |
| Account portal | `https://account.acme.com` |
| Organization admin portal | `https://admin.acme.com` |
| Admin console (partners and resellers) | `https://console.partner.com` |
| OIDC issuer, discovery, JWKS, authorize, token, userinfo, logout | `https://login.acme.com/.well-known/openid-configuration` |
| SAML entity ID, SSO and logout endpoints, metadata | `https://login.acme.com/saml2/...` |
| SCIM endpoint | `https://login.acme.com/scim/v2` |
| MCP authorization server and protected resource metadata | `https://login.acme.com/.well-known/oauth-authorization-server` |
| Management, authorization and account APIs (partners) | `https://api.partner.com` |
| Developer docs and status page (partners) | `https://docs.partner.com`, `https://status.partner.com` |
| Links in emails and SMS | Always point to the customer's domain |
| Email sender | `no-reply@acme.com` |
| SMS sender | The customer's sender ID or short code, where the country allows it |

**Nothing else gives the platform away:** cookie names, HTTP headers, error
pages, JavaScript globals, email headers and API error type URLs use neutral or
customer-defined names when white-labeling is on.

## 2. Domains at every level

Domains follow the hierarchy and setting modes
([15](15-hierarchy-and-shared-settings.md#2-setting-modes-defaults-and-resets)):

| Level | Example | Notes |
|-------|---------|-------|
| **Platform default** | `acme.eu.authnnz.io` | Used only when no other domain is set and the customer is not under a white-label partner |
| **Partner base domain** | `{tenant}.login.partner.com` | Every tenant of the partner gets a white-label URL automatically, without configuring anything; our domain never appears |
| **Tenant domains** | `login.acme.com`, `login.acme.de` | One primary and any number of aliases, per environment (for example `login.dev.acme.com` for dev) |
| **Organization domains** | `globex.login.acme.com` (automatic) or `login.globex.com` (own) | Organizations follow the tenant's domain (**Parent** mode), get an automatic vanity subdomain from a tenant base domain, or use their own domain (**Own**) |
| **Application** | Per-app choice of which tenant or organization domain its login uses | For companies with several brands |

The edge router maps each incoming domain to its partner, tenant, environment,
organization and region ([04 §6](04-architecture.md#6-multi-tenancy-and-isolation)),
so a custom domain is always served from the tenant's data-residency region.

## 3. Setting up a domain, without code

A guided wizard in the console ([11 §3.9](11-admin-ui.md#39-branding-and-experience)):

1. **Enter the domain** and choose what it serves (login, portals, APIs, email).
2. **Copy the DNS records** shown, with one-click copy and provider-specific
   instructions:
   - a TXT record that proves ownership;
   - a CNAME for subdomains, or A/AAAA records (or ALIAS/ANAME) for apex domains.
3. **Live verification:** the wizard checks DNS every few seconds and shows
   progress.
4. **Certificate:** issued automatically and renewed before expiry, or the
   customer uploads its own (for organizations that require their own
   certificate authority).
5. **Activate:** choose primary or alias; test pages confirm everything works
   before real users are switched over.

## 4. What the platform handles automatically

| Area | What happens |
|------|--------------|
| **Certificates** | Issued through ACME from more than one certificate authority, so one CA's outage or rate limits never block issuance. Wildcard certificates for automatic organization subdomains. Private keys encrypted with KMS. Expiry and renewal monitored, with alerts |
| **Bring your own certificate** | Upload certificate and key; expiry alerts start 30 days ahead |
| **TLS and HTTP security** | TLS 1.3, HSTS (preload optional), CAA record guidance, strict security headers |
| **Protocol identifiers** | Each domain is its own OIDC issuer (discovery, JWKS and `iss` all match the domain used), as the standard requires. SAML entity IDs, SCIM URLs and MCP metadata follow the domain |
| **Sessions and cookies** | When the login domain is on the same site as the customer's app (`login.acme.com` and `app.acme.com`), session cookies are first-party. Single sign-on and silent token renewal then keep working in browsers that block third-party cookies |
| **Passkeys** | See §5 |
| **Native apps** | The platform serves the files that link the domain to the customer's iOS and Android apps (`apple-app-site-association`, `assetlinks.json`) from the tenant's settings. This makes universal links and passkeys in native apps work |
| **Email sender domains** | The console shows the SPF, DKIM and DMARC records to add and verifies them; an optional BIMI logo shows the brand in supporting inboxes. Until verified, mail goes from the partner's domain (or ours when there is no partner) |
| **SMS senders** | Guided registration of sender IDs, short codes or long codes per country, through the messaging providers |
| **Allowed origins and redirects** | New domains are added to CORS, Content Security Policy and redirect-URI checks for the right applications |

## 5. Passkeys and domains

Passkeys are bound to a domain (the WebAuthn "relying party ID"), so the
domain plan must be decided carefully:

- **Choose the relying party ID at setup,** usually the customer's registrable
  domain (`acme.com`). Passkeys then work on `login.acme.com`, `app.acme.com` and
  any other subdomain.
- **Several brands or countries** (`acme.com` and `acme.de`) can share passkeys
  through WebAuthn related origins: the platform publishes the list of related
  origins at `/.well-known/webauthn` on the relying party domain. Browser
  support varies, so the console shows which users' browsers are covered.
- **Changing the relying party ID later** would make existing passkeys unusable.
  The console warns before any such change, keeps the old domain working as a
  related origin, and runs a guided re-enrollment campaign for affected users.

## 6. Changing domains safely

| Situation | How it is handled |
|-----------|-------------------|
| Move from the default or partner domain to a custom domain | The old domain becomes an alias that redirects browser traffic; protocol endpoints keep answering on both domains during a transition window |
| Change the primary domain | Both issuers stay valid during the transition. Our SDKs accept a list of trusted issuers, so APIs keep accepting tokens from both. The console shows which applications still use the old issuer |
| Remove a domain | The preview lists affected applications, redirect URIs, SAML partners, passkeys and email senders; removal needs confirmation and is audited |
| Customers' own reverse proxy or CDN | A **reverse-proxy mode** lets customers serve login through their own CDN or proxy, including under a path such as `acme.com/auth`, with documented header and IP trust settings |

## 7. Domain safety

- **Ownership proof before anything is served.** A domain is never served until
  its TXT record is verified.
- **Continuous re-verification.** Ownership is re-checked daily. If verification
  fails, the domain is paused and the customer is alerted, which prevents
  takeover of abandoned DNS entries.
- **No hijacking.** A domain verified by one tenant cannot be claimed by another
  without proving ownership again; every claim, transfer and removal is audited
  ([13](13-audit-and-change-tracking.md)).
- **Dangling-record alerts.** If a customer's DNS still points to us after a
  domain was removed, the customer is told to remove the record.
- **Health dashboard.** For each domain: DNS status, certificate status and
  expiry, email authentication status, and traffic.

## 8. How it is tested

- End-to-end domain lifecycle: add, verify, certificate issuance, activation,
  alias, primary change, removal.
- Takeover tests: lapsed verification, claims by another tenant, dangling records.
- Certificate chaos: CA outage and rate limiting fall back to the second CA.
- OpenID conformance run against a custom domain, as well as the default domain.
- Passkeys across subdomains and related origins, and in native apps through the
  association files.
- A white-label scan crawls every page, header, cookie, email and API response
  of a white-labeled tenant and fails if the platform name appears anywhere.

## 9. Feature IDs

| ID | Capability |
|----|-----------|
| TEN-04 | Custom domains for every surface (login, portals, console, protocol endpoints, APIs, docs and status pages), per partner, tenant, environment, organization and application, with primary and aliases |
| TEN-20 | Partner base domains: white-label default URLs for every tenant of a partner, and automatic organization subdomains from a tenant base domain |
| TEN-21 | Domain lifecycle and safety: guided DNS setup, automatic certificates from multiple CAs or bring-your-own, health monitoring, daily re-verification, takeover protection, safe primary-domain changes with dual issuers |
| TEN-22 | Reverse-proxy mode: serve login through the customer's own CDN or proxy, including path-based hosting |
| AUTH-30 | Passkeys on white-label domains: relying-party ID choice, WebAuthn related origins, native-app association files, guided re-enrollment on change |
| DYN-14 | Branded message senders: verified email sender domains (SPF, DKIM, DMARC, optional BIMI) and SMS sender IDs and short codes per country |
| UX-16 | White-labeling of console, portals, emails, cookies, headers and error pages, with no platform name visible |
