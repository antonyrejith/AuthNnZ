# 25 — Business Setup Guide for the Owner

Claude builds the platform (D-28). Some work needs a person or a company behind
it: owning the name, signing contracts, holding accounts and keys, passing
audits, working with partners and customers. This guide explains that work, in
order, and what Claude can prepare for each step (D-31).

> This is general guidance, not legal, tax or financial advice. Confirm each
> step with qualified professionals in your country and target markets.

## 1. Name and brand protection

The product name is **Portun** (D-01). Before it appears in public packages and
domains:

1. **Trademark search** in your home country and target markets: the WIPO
   Global Brand Database, the US (USPTO), the EU (EUIPO) and national offices.
   Ask a trademark lawyer to check for similar names in software and identity.
   Typical classes are 9 (downloadable software) and 42 (software as a
   service); ask whether others apply, such as identity-verification services.
2. **Linguistic check:** confirm the name has no unfortunate meaning in major
   languages of your target markets.
3. **Domains:** register the main domain (ideally `.com`) and alternatives
   (`.io`, `.dev`, country domains for key markets), plus common misspellings.
   Turn on registrar lock and two-factor authentication.
4. **Package and account names**, so nobody else takes them:

| Where | What to reserve | Note |
|-------|-----------------|------|
| npm | Organization `@portun` | |
| PyPI, crates.io, RubyGems, Packagist, pub.dev, NuGet | `portun` package names; NuGet prefix reservation | |
| Maven Central | Namespace such as `com.portun` | Requires proving domain ownership |
| Go modules | Module path on the company domain | Requires the domain |
| GitHub | Organization for the company | Move the repository there and rename it to `portun` |
| Container registries | `portun` namespace | |
| Apple App Store, Google Play | Developer accounts | For the authenticator app and mobile SDK samples |
| Unity Asset Store, Unreal Marketplace | Publisher accounts | For the game SDKs |
| Social media | `portun` handles | |

**Claude can prepare:** the list of names to reserve and the exact package
metadata; the repository and docs updates once domains are chosen.

## 2. Company and legal foundation

- **Legal entity** in the right jurisdiction for selling software
  internationally; ask an accountant and a lawyer.
- **Bank account and accounting.**
- **Tax registration** for selling SaaS and software across borders (VAT, GST,
  sales tax). A payments provider that acts as merchant of record, or a tax
  engine, can handle much of this.
- **Intellectual property:** make sure the company owns the code and the brand.
  Move the repository into a company GitHub organization. Ask your lawyer to
  confirm ownership of AI-generated code under the terms of the tools you use.
  Keep the SBOM-based license notices for third-party components
  ([22 §7](22-commercialization.md#7-legal-and-commercial-readiness)).

## 3. Contracts and policies

Have a lawyer experienced in SaaS and software licensing prepare or review:

| Document | Purpose |
|----------|---------|
| Terms of service and acceptable use policy | Self-service customers |
| Master subscription agreement, order form, SLA | Enterprise customers |
| Data processing agreement with standard contractual clauses | GDPR and other privacy laws |
| Privacy policy and sub-processor list | Website and trust center |
| Business associate agreement (BAA) | Healthcare customers (HIPAA) |
| Proprietary SDK license and self-hosted license agreement | D-08: SDKs and the self-hosted edition are proprietary |
| Reseller, partner, OEM and system-integrator agreements | Partner channel (D-27) |
| Design-partner (early-access) agreement | Confidentiality, feedback, data handling, no SLA |

**Claude can prepare:** first drafts and checklists for the lawyer, the trust
center content, and the technical annexes (security measures, sub-processors,
data locations).

## 4. Accounts, infrastructure and keys

These are owned by the company and held by you:

- **Cloud accounts** on AWS, Azure and Google Cloud (D-07), each set up as an
  organization with separate accounts or projects for dev, staging and
  production, billing alerts and budgets. Startup programs (AWS Activate,
  Microsoft for Startups, Google for Startups Cloud Program) can provide credits.
- **Domain registrar and DNS.**
- **Email and SMS providers** for sending messages; **payments and billing**.
- **Package registries and app stores** from §1.
- **Signing keys** for releases, code and mobile apps, kept in hardware or a
  cloud key-management service, never on a laptop.

**Keep them safe:** use hardware security keys for every admin login, store
recovery codes offline, and plan a break-glass process in case you are
unavailable. Give Claude only the access each task needs, through the
environment's secret settings, never by pasting secrets into chat.

## 5. Compliance program

1. **Choose a compliance-automation tool** (D-13) by price and auditor network.
2. **Policies:** information security, access control, change management,
   incident response, business continuity, vendor management, data retention.
   Claude drafts them; you approve and adopt them.
3. **Controls:** most technical controls are part of the platform and its
   infrastructure (audit, encryption, access, monitoring); people controls
   (security training, background checks where lawful) apply to everyone who
   joins.
4. **Auditors:** a CPA firm for SOC 2; an accredited certification body for
   ISO 27001. SOC 2 Type I first, then Type II after an observation window of
   3–12 months; ISO 27001 through stage 1 and stage 2 audits.
5. **HIPAA** (risk assessment, policies, BAA) and **PCI DSS** if customers put
   you in cardholder-data scope.

**About code review (D-29):** auditors assess how changes are reviewed and
approved. Document the automated review passes and CI gates clearly. A simple
extra control many auditors accept is the owner approving each production
release, without reviewing every pull request; it can be added at any time.

**Claude can prepare:** policy drafts, control descriptions, evidence
collection automation, and answers to security questionnaires.

## 6. Security testing, certifications and export control

| Item | What to arrange |
|------|-----------------|
| Penetration tests | An independent firm for two rounds before GA, then twice a year |
| Bug bounty | A platform such as HackerOne, Bugcrowd or Intigriti; private before GA, public from GA |
| OpenID certification | OpenID Foundation certification for each profile (fees apply; membership reduces them) |
| FIDO certification | FIDO Alliance server certification (fees apply) |
| Export control | Classification of the software under encryption export rules (for example the US EAR and the EU dual-use regulation) with counsel, before self-hosted downloads |
| Insurance | Cyber liability and technology errors-and-omissions cover sized for enterprise contracts |

## 7. Design partners

Because sales focus is on all segments equally (D-02), aim for at least one
design partner per segment: a B2B SaaS company, a consumer app, a bank or
fintech, a healthcare organization, a public-sector or telecom organization, and
an AI or agent builder.

- **Where to find them:** your network, developer and security communities,
  startup programs, industry associations, LinkedIn.
- **What to offer:** free or discounted early access, influence on the product,
  hands-on migration help, a reference discount later.
- **What you ask for:** a named contact, feedback at each six-week checkpoint,
  permission to become a public reference if the pilot succeeds.
- **Agreement:** the design-partner agreement from §3.

**Claude can prepare:** the outreach message, a one-page overview, demo
environments and the pilot success criteria.

## 8. Vendors to line up

| Vendor | When |
|--------|------|
| Compliance-automation tool | During foundations |
| Email, SMS and WhatsApp providers; CAPTCHA provider | Before the first public preview |
| Identity-verification (KYC) and risk-data providers | During the build, for the related features |
| Payments, billing and tax | Before the release candidate |
| Translation vendor with in-country reviewers | During the build |
| Penetration-testing firm, auditors, certification labs, bug bounty platform | Before hardening |
| Cloud marketplace seller accounts (AWS, Azure, Google Cloud) | Before the release candidate |

## 9. Deferred decisions and how to approach them

| ID | Decide by | How to decide |
|----|-----------|---------------|
| D-14 Budget | Before cloud accounts are opened | Main costs: cloud infrastructure on three clouds, third-party services (audits, penetration tests, certifications, translation), any people hired. Claude can prepare a cost model once accounts and regions are known |
| D-30 Production operations | Before hardening | Compare a small hired team, a managed-service partner, or a mix; someone accountable must be on call at GA ([18 §1](18-operating-the-service.md#1-operating-model-at-ga)) |
| D-26 Go-to-market team | Before hardening | Options in [22 §8](22-commercialization.md#8-go-to-market-team-and-timeline) |
| D-25 Pricing | Before the release candidate | Competitor price benchmarks, willingness-to-pay surveys (for example the Van Westendorp method) and design-partner interviews; the edition table in [22 §2](22-commercialization.md#2-editions) is the starting point |

## 10. Checklist by stage

| Stage | Owner tasks |
|-------|-------------|
| **Now** | Trademark search and filing; domains; reserve package and account names; company GitHub organization |
| **Foundations** | Legal entity, bank, accounting; recruit design partners; choose the compliance tool; adopt policies |
| **Build** | Contracts drafted with a lawyer; cloud accounts and budget (D-14); messaging and KYC vendors; translation vendor |
| **Before hardening** | Penetration testers, auditors, certification labs, bug bounty; decide operations (D-30) and go-to-market team (D-26); insurance |
| **Before the release candidate** | Pricing (D-25); payments, billing and tax; marketplace seller accounts; export classification |
| **Before GA** | Audit reports and certifications; trust center published; reference customers confirmed |
