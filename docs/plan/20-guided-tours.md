# 20 — Guided Tours

Every user interface has a **"Take a tour"** option. Tours are interactive:
they highlight the real screen, explain each part, and let people try it
themselves, so new admins, developers, organization admins and end users
become confident quickly without reading documentation first.

## 1. Tours for every role and every UI

| Audience | Tour | Starts |
|----------|------|--------|
| New tenant owner | **Welcome tour:** navigation, environments, the draft → publish bar, history and undo, command palette, help | On first sign-in (can be skipped) |
| Developer | **First login in 5 minutes:** create an app, copy the quick-start, test a login, read the token | From the setup checklist |
| Security admin | MFA and session policies, attack protection, security posture center, audit explorer | First visit to Security |
| Authorization admin | Permission catalog, roles, assignments, playground and explanations | First visit to Authorization |
| Brand or marketing owner | Theme studio, effects and glass, screen builder, preview links, publishing | First visit to Branding |
| Partner admin | Hierarchy explorer, setting modes and reset, templates and push, white-labeling | First visit as a partner |
| Organization admin (our customer's customer) | Organization admin portal: SSO setup, SCIM, domains, members and roles | First visit to the portal |
| End user | Account portal: **"Secure your account"** (add a passkey, save recovery codes, review devices and connected apps) | First visit, or after sign-up if the tenant enables it |

**Feature tours** exist for every complex screen: flow designer, screen builder,
policy editor, relationship explorer, template push, effective settings and
reset, integration health, AI admin assistant. Every screen has a **"?"** menu
with "Take a tour" and links to the relevant docs.

## 2. How tours work

- **Interactive, not slides:** a step highlights a real element and can ask the
  user to do something ("Click Add application"), waiting until they do it.
  Short videos or animations are optional extras.
- **Always available:** start, replay or resume any tour from **Help → Take a
  tour** or the **"?"** on each screen. Tours remember progress across
  sessions and devices; they can be skipped, snoozed or turned off.
- **Learning center:** a side panel lists the tours for the user's role, shows
  progress, and links each finished tour to the next useful one.
- **Adapted to the person:** tours only show steps the user's admin role allows,
  use the partner's names and branding under white-label, and follow the
  user's language ([21](21-localization.md)).
- **Accessible:** keyboard navigation, screen-reader announcements, visible
  focus, and no motion when the user prefers reduced motion.

## 3. Practice mode

Admins can take any tour in a **practice environment** filled with sample
users, applications, organizations, roles and audit history. They can click,
change and publish freely without touching real data. Practice mode is
clearly marked, can be reset to its starting state with one click, and is never
mixed with real environments.

## 4. What's new

After each release, short **"What's new" tours** introduce new or changed
features to the roles they matter to. An announcements panel and the changelog
list everything that changed. Admins can turn "What's new" tours off for their
team.

## 5. Tour builder for customers (no code)

Tenants and partners can build their own tours and announcements for the
people they serve:

- **Where:** the organization admin portal, the account portal, and a
  first-login walkthrough on hosted screens (for example "Set up a passkey").
  Partners can also add tours to the white-labeled console for their tenants.
- **How:** click the element to highlight, write the text in each language,
  choose who sees it (role, organization, plan, first visit, after a date) and
  when.
- **Managed like all configuration:** versioned, previewed, published and rolled
  back; inherited down the hierarchy with the usual setting modes
  ([15](15-hierarchy-and-shared-settings.md#2-setting-modes-defaults-and-resets)).
- **Analytics** (with consent): how many people start and finish each tour, and
  the step where people drop off.

## 6. Quality

- **Tours cannot silently break.** Each step is anchored to a stable element
  identifier. A UI change that removes or renames an anchor fails the build, and
  an end-to-end test walks every tour in every role.
- **Tours are part of the definition of done:** a new or changed screen
  includes its tour updates.
- **Usability testing:** design partners try tours at every integration
  checkpoint, and completion and drop-off rates guide improvements.

## 7. Feature IDs

| ID | Capability |
|----|-----------|
| UX-23 | Guided tours for every UI and role: interactive steps, feature tours, "?" on every screen, learning center, resumable, permission-aware, accessible, localized; practice mode with sample data |
| UX-24 | Tour builder (no code) for tenants and partners: tours and announcements for organization admins, end users and white-labeled consoles; versioned, inherited, with analytics |
| UX-25 | "What's new" tours and in-product announcements after each release, per role, with an opt-out |
