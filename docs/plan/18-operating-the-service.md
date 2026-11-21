# 18 — Operating the Service

Building the platform is half the job. This document covers how it is run once
customers depend on it: support, incidents, breach notification, releases after
GA, trust, abuse prevention, connectivity, integration health, tenant moves and
cost.

## 1. Operating model at GA

| Function | Responsibility | Staffing at GA |
|----------|----------------|----------------|
| **Site reliability (SRE)** | 24/7 on-call in follow-the-sun rotations, incident command, capacity, disaster recovery | Infrastructure workstream plus 4 additional SREs |
| **Customer support** | Tiered support per edition, troubleshooting, escalations to engineering | 6 support engineers |
| **Customer success** | Onboarding, migrations, health reviews for larger customers | 3 |
| **Security operations** | Detection and response for the platform, threat hunting, vulnerability management | 2, plus the security team |
| **Trust and safety** | Abuse prevention and takedowns (§7) | 1 |

These 16 people join during hardening (weeks 64–90), so they are trained and
rehearsed before GA ([05 §3](05-roadmap-and-delivery.md#3-workstreams-and-team)).

## 2. Support

| Edition | Channels | First response, severity 1 (service down) | First response, severity 2 (major impact) |
|---------|----------|------------------------------------------|-------------------------------------------|
| Developer | Community forum, docs | — | — |
| Pro | Support portal, email | 4 business hours | 1 business day |
| Business | Portal, email, chat | 1 hour, 24/7 | 4 hours |
| Enterprise and Regulated | Portal, email, chat, phone, shared Slack or Teams channel, named contacts | 15 minutes, 24/7 | 1 hour, 24/7 |

- Support staff use the scoped, audited support console (UX-08). Any access to a
  tenant's data needs the tenant's approval and shows up in their audit log
  (GOV-15).
- Customers troubleshoot on their own first: the audit explorer follows any
  login or request by trace ID, the flow debugger replays journeys, and the
  integration health center (§9) flags problems before they cause tickets.
- A public knowledge base and the status page answer common questions.

## 3. Incident management

- **Severities** from 1 (service or security impact for many tenants) to 4
  (minor), each with response and update intervals.
- **Roles:** an incident commander, a communications lead and subject-matter
  responders; runbooks for every alert.
- **Communication:** the public status page (OPS-02) for broad incidents;
  targeted notifications to affected tenants (console, email, webhook) for
  incidents limited to some tenants or cells.
- **After the incident:** a blameless review with actions tracked to completion;
  a customer-facing summary for every severity 1 and 2 incident.
- **SLA:** monthly availability reported per tenant; service credits calculated
  automatically under the contract.

## 4. Security incidents and breach notification

| Obligation | Timeline | How we meet it |
|------------|----------|----------------|
| Customers (contract, GDPR processor duty) | Without undue delay; target within 24 hours of confirming an incident that affects their data | Per-tenant notification with the facts known, updates as they develop, final report |
| GDPR (customers as controllers) | Customers must notify authorities within 72 hours | Our notification and audit evidence give them what they need in time |
| NIS2 (EU) | Early warning within 24 hours, notification within 72 hours, final report within one month | Playbook with pre-approved templates |
| DORA (EU financial entities) | Our financial customers must report major ICT incidents | We provide the incident data they need as their ICT provider |
| Cyber Resilience Act (EU, self-hosted edition) | Actively exploited vulnerabilities reported to the authorities: early warning within 24 hours | Vulnerability handling process, security advisories, SBOM per release |

- **Forensic readiness:** the tamper-evident audit trail
  ([13](13-audit-and-change-tracking.md)) and platform audit trail let us
  reconstruct exactly what happened.
- **Rehearsed:** tabletop exercises for breach scenarios before GA and twice a
  year after.
- **Vulnerability disclosure:** a published policy, `security.txt`, a bug bounty,
  and security advisories with CVE identifiers for the self-hosted edition.

## 5. Releases and support after GA

| Delivery | Policy |
|----------|--------|
| **SaaS** | Continuous delivery, released cell by cell with canaries ([12 §4](12-containers-and-deployment.md#4-build-and-release-pipeline)); no breaking changes to published APIs or configuration; behavior changes announced in the changelog in advance |
| **Self-hosted** | Monthly releases; a **long-term-support (LTS)** release every 12 months with 24 months of security fixes; customers pin a version and upgrade deliberately, in line with the version policy ([08 §1](08-engineering-standards.md#1-version-policy-pinned-everywhere-upgrades-on-purpose)) |
| **Upgrades** | Supported directly from any supported release to the next LTS; zero-downtime upgrades through the Kubernetes operator; configuration migrations applied automatically and reversibly |
| **End of life** | Announced at least 12 months ahead, for releases, API versions, SDK versions and features |
| **SDKs** | Semantic versioning; each major version supported for at least 24 months after the next one ships |

## 6. Trust center

A public trust center gives buyers and auditors what they need without a sales
call:

- security overview and architecture summary;
- certifications and reports (SOC 2 and penetration test summaries under NDA);
- the sub-processor list, with advance notice of changes to subscribed customers;
- data residency regions and where each kind of data is stored;
- uptime history and SLA terms;
- the data processing agreement and standard security questionnaire answers
  (CAIQ, SIG).

## 7. Platform abuse prevention

White-label login pages, custom domains and branded email are exactly what a
phishing operation would want. The platform protects itself and everyone's
users:

- **Tenant sign-up screening:** risk scoring of new tenants, sanctions and
  restricted-party screening, and business verification before white-label,
  custom domains or branded email senders are enabled.
- **Graduated limits:** new tenants start with sending and volume limits that
  grow with a good track record.
- **Content scanning:** custom pages, templates and emails are scanned for
  phishing patterns and impersonation of well-known brands before publishing.
- **Reputation monitoring** of sender domains and custom domains.
- **Abuse reporting** (an `abuse@` address and form), a takedown process with
  evidence kept in the audit trail, and an appeal path.

## 8. Private connectivity and network

- **Private endpoints:** customers can reach our APIs through AWS PrivateLink,
  Azure Private Link or Google Private Service Connect, without the public
  internet.
- **Static egress IPs** per region for webhooks, connectors and calls to customer
  identity providers, so customers can allowlist us.
- **IP allowlists** for the console and APIs per tenant (SEC-13).

## 9. Integration health center

Proactive monitoring of every tenant's integrations, with alerts and suggested
fixes in the console:

- identity-provider signing certificates and metadata that are about to expire
  or have changed;
- SCIM and directory sync errors;
- failing webhook endpoints and event streams;
- connector errors (email, SMS, CAPTCHA, KYC);
- client secrets, API keys and certificates nearing expiry;
- unused applications, connections and credentials.

## 10. Moving tenants

A tenant can be moved between regions (with data-residency approval), between
cells, or to a dedicated cell or the customer's own cloud, with near-zero
downtime:

1. replicate the tenant's data to the target;
2. keep it in sync while traffic still goes to the source;
3. switch traffic at the edge router (domains and URLs do not change);
4. verify, then delete the source copy with crypto-shredding.

Every step is audited and can be rolled back until the source is deleted.

## 11. Cost management

- **Cost attribution** per tenant and per cell: compute, storage, audit volume,
  data transfer, and SMS and voice pass-through.
- **Unit economics:** cost per monthly active user, per million authorization
  decisions, per gigabyte of audit, used for pricing and quotas.
- **Budgets and anomaly alerts** per cell and for unusually expensive tenants.

## 12. Feature IDs

| ID | Capability |
|----|-----------|
| OPS-07 | Private connectivity (AWS PrivateLink, Azure Private Link, Google Private Service Connect) and static egress IPs per region |
| OPS-08 | Integration health center: expiring IdP certificates and secrets, sync and webhook failures, connector errors, unused integrations |
| OPS-09 | Trust center: security overview, reports under NDA, sub-processors with change notices, residency, uptime, DPA, questionnaires |
| OPS-10 | Platform abuse prevention: tenant screening and verification, graduated limits, content scanning, reputation monitoring, abuse reporting and takedown |
| OPS-11 | Cost attribution per tenant and cell, unit economics, budgets and anomaly alerts |
| OPS-12 | Release channels and long-term support for the self-hosted edition, with supported upgrade paths and end-of-life notices |
| OPS-13 | Incident and breach notifications: targeted per-tenant notices, regulatory timelines, post-incident summaries, automatic SLA credits |
| TEN-24 | Tenant moves between regions, cells, dedicated cells and customer clouds with near-zero downtime |
