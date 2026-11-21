# 16 — Look & Feel: No-Code Customization and Design Quality

Two commitments:

1. **Customers can make every screen their users see look exactly like their
   brand, without writing code.** Point-and-click tools, live previews and
   ready-made templates do the work; code is optional, never required.
2. **Our own product is rich, fast and beautiful.** The console, portals and
   default screens are designed and built to the standard of the best developer
   and SaaS products, and measured against that standard.

## 1. What customers can customize

| Surface | Who sees it | Customizable without code |
|---------|-------------|---------------------------|
| Hosted login, sign-up, MFA, recovery, consent, device and approval screens | End users | Theme, layout, every block on every screen, text, languages, images |
| Account portal | End users | Theme, layout, which sections appear, custom links |
| Organization admin portal | The customer's business customers' IT admins | Theme, product name, logo, help and support links |
| Emails, SMS and push messages | End users and admins | Full visual email designer, text per language |
| Embeddable web components and SDK screens | End users inside the customer's app | Inherit the theme automatically |
| Native mobile screens (iOS, Android, Flutter, React Native) | End users | Inherit the theme and screen layouts automatically, without an app release |
| Error, maintenance and logged-out pages | End users | Theme, text, images |
| Admin console (white-label) | Partners' and resellers' customers | Product name, logo, colors, domain, help and support links |

## 2. No-code tools

### 2.1 Theme studio

A visual editor with a live preview beside it:

- **Brand kit:** logos (light and dark), favicon, color palette, fonts, corner
  radius, spacing density, shadows, button and input styles. Edited with color
  pickers, sliders and presets.
- **Layouts:** centered card, split screen with image, full page, side panel,
  or a template from the gallery.
- **Backgrounds:** color, gradient, image or subtle pattern.
- **Light, dark and high-contrast variants**, generated automatically and
  editable.
- **Live preview** of every screen, on desktop, tablet and mobile, in light and
  dark, in every enabled language, including right-to-left scripts.
- **Built-in quality checks** as you edit: color contrast (WCAG 2.2 AA) with
  one-click fixes, minimum text sizes, visible focus states, image weight.

```
┌─ Branding › Theme studio › "Acme Blue" ─────────────────────────────── [prod ▾] ─┐
│ Draft · 4 changes          [Preview link] [Schedule] [Visual diff] [Publish ▸]   │
├───────────────────────────┬──────────────────────────────────────────────────────┤
│ BRAND                     │ [Desktop] Tablet  Mobile     Light | Dark    [EN ▾]  │
│ Logo     [acme.svg] [Up]  │ Screen: [Sign in ▾]                                  │
│ Primary  [#4F46E5]  ...   │ ┌────────────────────────┬─────────────────────────┐ │
│ Accent   [#F59E0B]  ...   │ │  ACME                  │                         │ │
│ Font     [Inter       ▾]  │ │                        │                         │ │
│ Radius   ---o------ 10px  │ │  Welcome back          │      (brand image)      │ │
│ Density  (•) Std ( ) Tight│ │  [ email           ]   │                         │ │
│                           │ │  [    Continue     ]   │                         │ │
│ LAYOUT                    │ │  ───────  or  ───────  │                         │ │
│ ( ) Centered (•) Split    │ │  [Google] [Microsoft]  │                         │ │
│ ( ) Full page             │ │  [  Sign in with a  ]  │                         │ │
│                           │ │  [     passkey      ]  │                         │ │
│ BACKGROUND                │ │  Terms · Privacy · Help│                         │ │
│ (•) Image ( ) Color       │ └────────────────────────┴─────────────────────────┘ │
│ ( ) Gradient              │                                                      │
│                           │ BLOCKS (drag to reorder)               [+ Add block] │
│ ✓ Contrast AA passed      │ Logo · Heading · Email · Continue · Divider ·        │
│ [Import from website]     │ Social · Passkey · Footer links                      │
│ [Ask design assistant]    │ Rule: show "Enterprise SSO" when org has SSO         │
└───────────────────────────┴──────────────────────────────────────────────────────┘
```

### 2.2 Brand import and design assistant

- **Import from a website or logo:** paste the company's website address or
  upload a logo; the studio proposes a palette, fonts and a complete accessible
  theme.
- **Design assistant (optional, can be turned off):** describe the look you want
  in words ("calm, trustworthy, banking") and get theme and wording suggestions.
  Suggestions are always shown in the preview first and are never applied
  without the admin's approval. It works only with the customer's own brand
  assets.

### 2.3 Template gallery

Professionally designed starting points, each fully editable: minimal,
corporate, split hero, bold, dark, and industry-specific themes delivered with
the industry packs (banking, healthcare, government, retail, education,
gaming). Templates follow the setting modes, so a customer can stay linked to
a template and receive its improvements, or make it their own
([15 §2](15-hierarchy-and-shared-settings.md#2-setting-modes-defaults-and-resets)).

### 2.4 Screen builder

Every hosted screen is built from **blocks** that can be added, removed and
reordered by drag and drop:

- **Blocks:** logo, heading, text, image, banner or announcement, identifier
  field, password field, passkey button, social login buttons, enterprise SSO
  button, custom fields from the user schema, terms and consent checkboxes,
  links, divider, language switcher, marketing panel, footer.
- **Show or hide blocks by condition** with a simple rule builder (application,
  organization, language, device, country, new or returning user). Advanced
  users can switch to a CEL expression.
- **Reusable blocks** such as a shared header and footer, defined once and used
  on every screen.
- **Guardrails:** the builder keeps every screen working and safe. A password
  screen always has its password field; a consent screen always shows the
  application and the permissions requested.
- **One layout for every platform.** Screens are stored as server-driven UI
  descriptors ([04 §4.3](04-architecture.md#43-server-driven-ui-contract)), so the
  same design appears in the browser, in embeddable components and in native
  mobile apps, and changes reach mobile users without an app release.

### 2.5 Content and translations

- **Click any text in the preview to edit it**, per language.
- Variables (`{user.first_name}`, `{app.name}`) and plural forms (ICU) are
  inserted from a menu, without writing syntax.
- **Translation workflow:** 40+ built-in languages; import and export (XLIFF,
  JSON) for translation agencies; machine-translation suggestions marked for
  review; missing-translation warnings before publishing.

### 2.6 Message designer

A drag-and-drop designer for emails (responsive, dark-mode aware) that inherits
the theme automatically, plus simple editors for SMS and push text. Preview
across common email clients and send test messages before publishing.

### 2.7 Asset library

Logos, images, illustrations, icons and fonts in one place. Fonts can be chosen
from a curated library or uploaded, and are always served from our CDN (no
calls to third-party font services, for privacy and Content Security Policy).
Images are optimized automatically: modern formats and responsive sizes. The
library shows where each asset is used, and assets can be shared down the
hierarchy.

### 2.8 White-labeling

Partners and tenants can present the platform as their own product: product
name, logo, colors, custom domain, email sender, help and support links,
across the console, organization admin portal, account portal and emails.
Nothing in a white-labeled experience mentions AuthNnZ unless the customer
chooses to.

## 3. Guardrails: beautiful, accessible and safe by default

| Guardrail | How it works |
|-----------|--------------|
| Accessibility | Contrast, text size, focus visibility and reading order are checked live; publishing is blocked for WCAG 2.2 AA failures unless an admin explicitly accepts them, which is audited |
| Required elements | Screens cannot lose the elements they need to work or to stay honest (credential fields, consent details, required legal links) |
| Performance | Image and font sizes are optimized automatically; the preview warns when a screen would exceed the performance budget |
| Security | No scripts in no-code customization; everything is sanitized and served under a strict Content Security Policy; external resources only from an allowlist |
| Anti-phishing | Hosted screens always run on the customer's verified domain; elements that help users recognize the real login page cannot be hidden |
| Privacy | Fonts and images are self-hosted; no third-party trackers |

## 4. Publishing, previews and experiments

Themes, screen layouts, texts and message templates are versioned
configuration, like everything else ([04 §2](04-architecture.md#2-how-100-dynamic-works)):

- **Draft → preview → approve → publish → roll back.**
- **Shareable preview links** let marketing or legal review a draft on their own
  phone; links expire and are audited.
- **Visual diff:** side-by-side screenshots of every screen that changes before
  publishing.
- **Scheduled publishing:** for example a seasonal theme that goes live on a date
  and reverts automatically afterwards.
- **A/B experiments:** compare two designs on real traffic and see sign-up
  completion and drop-off per variant ([GOV-03](02-feature-catalog.md#l-governance-audit--privacy-gov)).
- Every change is audited with before and after values
  ([13](13-audit-and-change-tracking.md)).

## 5. Across the hierarchy

Look and feel follows the same rules as every other setting
([15](15-hierarchy-and-shared-settings.md)):

- Acme publishes its theme once; all customer organizations follow it
  (**Parent** mode).
- Globex replaces only its logo and primary color (**Own** for those two
  settings) and keeps following Acme for everything else.
- A partner gives all its tenants a white-labeled base theme (**Template**) and
  pushes improvements down.
- Any level can return a setting, a section ("all branding") or everything to
  **Default** or **Parent** at any time, with preview and undo.

## 6. Same look everywhere

```mermaid
flowchart LR
    ST[Theme studio and<br/>screen builder] --> CFG[Versioned theme, layouts,<br/>texts, assets]
    CFG --> CMP[Compiler]
    CMP --> TOK[Design tokens<br/>W3C Design Tokens format]
    CMP --> CSS[Precompiled CSS per theme<br/>content-hashed on CDN]
    CMP --> SDUI[Server-driven<br/>screen descriptors]
    TOK & CSS & SDUI --> WEB[Hosted pages and<br/>web components]
    TOK & SDUI --> NAT[iOS, Android, Flutter,<br/>React Native SDKs]
    TOK & CSS --> MAIL[Emails]
```

- Design tokens and screen descriptors are delivered at runtime, so web pages,
  embeddable components and native apps all match, and a design change needs
  no rebuild or app release.
- Themes are resolved per organization through the hierarchy and cached, so a
  customized login page is as fast as the default one.

## 7. Optional pro mode

For customers who want code, a pro mode allows scoped, sanitized custom CSS and
custom HTML templates for screens and emails. It runs under the same Content
Security Policy, never allows scripts, is versioned and audited like everything
else, and is never needed for anything the no-code tools can do.

## 8. Design quality of our own product

### 8.1 Principles

1. **Clear:** one obvious next step on every screen; plain language.
2. **Fast:** instant feedback; nothing waits without a visible reason.
3. **Guided:** setup checklists, sensible defaults, inline help and examples.
4. **Trustworthy:** every change shows its impact before it happens and can be undone.
5. **Delightful:** polished visuals, motion with purpose, attention to detail.

### 8.2 Design system

- **Foundations:** design tokens for color, type, spacing (8-point grid),
  radius, elevation and motion; light, dark and high-contrast themes; a
  consistent icon set; illustrations for empty states and onboarding.
- **Components:** accessible primitives (Radix UI) styled with the tokens,
  documented and tested in Storybook ([11 §8](11-admin-ui.md#8-front-end-technology)).
- **Data visualization:** consistent, accessible charts for analytics and
  dashboards.
- **Motion:** short, purposeful transitions; `prefers-reduced-motion` respected.

### 8.3 Rich, easy interactions in the console

| Interaction | Example |
|-------------|---------|
| Command palette and keyboard shortcuts | `Ctrl/⌘ K` to jump to any user, app, setting or doc |
| Visual editors | Flow designer, screen builder, theme studio, role matrix, relationship graph |
| Inline editing with instant validation | Edit a value where it is shown; errors explain how to fix them |
| Side panels instead of page jumps | Open a user or app without losing your place |
| Undo everywhere | Toast with "Undo" after every change; full history per object |
| Optimistic updates and skeleton loading | The UI responds immediately; loading states keep their layout |
| Saved views and bulk actions | Filtered user lists, multi-select actions |
| Presence and conflict warnings | See who else is editing an object; no silent overwrites |
| Helpful empty states | Every empty list explains what to do next, with a one-click start |
| Guided setup | Checklists and wizards for first app, SSO, branding and go-live |
| Personalization | Light or dark, compact or comfortable density, pinned and recent items |
| Notifications center | Pending approvals, push progress, alerts |

### 8.4 Targets

| Measure | Target |
|---------|--------|
| Time from sign-up to a branded login page | ≤ 5 minutes |
| System Usability Scale (SUS) with design partners | ≥ 80 |
| Task success for key tasks (create app, set up SSO, change theme, assign roles) | ≥ 90% without help |
| Console route change | < 200 ms |
| Hosted login: largest contentful paint on a typical 4G phone | < 1.5 s |
| Interaction to next paint (all UIs) | < 200 ms |
| Accessibility | WCAG 2.2 AA on every UI, verified by an external audit before GA |

### 8.5 How design quality is ensured

- **Design team:** product designers, a visual and brand designer, a content
  designer and a UX researcher inside the Experience workstream
  ([05 §3](05-roadmap-and-delivery.md#3-workstreams-and-team)).
- **Design review is part of the definition of done** for every UI change:
  a feature is not done until design QA signs it off.
- **Usability tests with design partners** at every integration checkpoint;
  findings are tracked like bugs.
- **Content design:** consistent terminology, and error messages that say what
  happened and what to do.

## 9. How it is tested

- **Visual regression:** every component and every default theme and template is
  screenshot-tested in light, dark and high-contrast, on desktop and mobile,
  in a left-to-right and a right-to-left language.
- **Customization fuzzing:** random themes and layouts are generated and checked
  for contrast, overflow, broken layouts and missing required elements.
- **Renderer parity:** the same screen descriptor is rendered on web, iOS and
  Android and compared.
- **Accessibility:** automated axe checks on every screen and template, plus
  manual screen-reader testing and an external audit.
- **Performance:** each template is measured against the targets in §8.4.

## 10. Feature IDs

| ID | Capability |
|----|-----------|
| UX-09 | Theme studio: brand kit, layouts, backgrounds, light/dark/high-contrast, live multi-device and multi-language preview, live quality checks |
| UX-10 | Brand import from website or logo, and optional design assistant |
| UX-11 | Template gallery, including industry-pack themes |
| UX-12 | No-code screen builder: drag-and-drop blocks, conditional visibility, reusable blocks, guardrails, one layout for web and native |
| UX-13 | Content and translation editor: click-to-edit text, variables and plurals from menus, translation workflow |
| UX-14 | Message designer for emails, SMS and push |
| UX-15 | Asset library with self-hosted fonts, automatic image optimization and where-used |
| UX-16 | White-labeling of console, portals and emails for partners and tenants |
| UX-17 | Design publishing: preview links, approvals, visual diff, scheduled publishing, A/B experiments |
| UX-18 | Optional pro mode: sanitized custom CSS and HTML under strict CSP |
| UX-19 | Runtime design tokens shared by hosted pages, web components and native SDKs |
| UX-20 | Rich console interactions: command palette, shortcuts, side panels, undo, saved views, bulk actions, presence, guided setup, personalization |
