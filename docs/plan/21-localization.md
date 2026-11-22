# 21 — Localization

Localization is more than translated text. People everywhere should see their
language, their date and number formats, their name and address conventions,
the right legal text for their country, and a design that works in their
script, in every UI, message and API.

## 1. What is localized

| Surface | Languages | Notes |
|---------|-----------|-------|
| Hosted login, sign-up, MFA, recovery, consent and other end-user screens | 40+ | Including right-to-left (Arabic, Hebrew, Persian, Urdu), CJK, Indic scripts, Thai, Vietnamese |
| Emails, SMS and push messages | 40+ | Sent in each user's language |
| Account portal and organization admin portal | 40+ | |
| Admin console, guided tours and in-product help | 40+ | |
| SDK UI components and native mobile screens | 40+ | Follow the device language unless the app chooses one |
| API error messages | 40+ | Human-readable text only; error codes never change |
| Developer documentation and knowledge base | English plus the most-requested languages (chosen with market research during foundations) | |
| Customer support | English 24/7; other major languages during regional business hours | |

The exact GA language list is set during foundations, based on target markets.
Customers can add **any further language** for their own end-user screens and
messages through the translation editor (UX-13).

## 2. Choosing the language

The platform picks the language in this order, using the first one available:

1. The language the user just picked in the language switcher.
2. The language the application asks for through the OpenID Connect
   `ui_locales` parameter (or the SDK).
3. The user's saved preference, updated whenever they switch.
4. The organization's default language.
5. The application's default language.
6. The browser or device languages (`Accept-Language`).
7. The tenant's default language.

The chosen language is returned to applications in the `locale` claim.
Localized profile values (for example a name in several scripts) are returned
according to the OpenID Connect `claims_locales` parameter. Admins choose their
own console language, time zone and formats in their profile.

## 3. Formats and input

All formatting uses the Unicode CLDR data through ICU, so every locale follows
its own conventions:

| Kind | Behavior |
|------|----------|
| Dates and times | Local formats and calendars for display; stored in UTC; shown in the viewer's time zone; schedules (publishing, access expiry) always state their time zone |
| Numbers, currencies, units | Local separators, symbols and plural rules |
| Names | Given-name and family-name order, single names, names in several scripts; no assumption that everyone has a first and last name |
| Addresses | Country-specific fields and order, through the dynamic user schema (IDM-01) |
| Phone numbers | Country picker, local formatting and validation; stored in E.164 |
| Sorting and search | Language-aware sorting (database ICU collations), with accent-insensitive and case-insensitive search where appropriate |

## 4. Unicode and safety

- **Normalization:** usernames, emails and passwords are prepared and compared
  using the PRECIS framework (RFC 8264, RFC 8265) and Unicode normalization, as
  NIST SP 800-63B recommends. The same password typed on different keyboards
  always works.
- **International addresses:** email addresses with non-ASCII characters
  (SMTPUTF8, RFC 6531) and internationalized domain names (IDNA 2008) are
  supported.
- **Look-alike protection:** confusable-character detection (Unicode UTS #39)
  flags usernames, display names, organization names and custom domains that
  imitate others (for example a Cyrillic "а" in place of a Latin "a").
  Mixed-script domains are shown in their punycode form.
- **Hidden-character protection:** invisible and bidirectional control
  characters are removed from names and other display fields, so they cannot
  disguise text.

## 5. Content per country and region

- **Legal text and consents** per country or region (terms, privacy notices,
  marketing consent), with the right version shown automatically.
- **Age rules:** the age of digital consent per country (GDPR allows 13–16
  depending on the country), used by age gates and parental consent (IDM-17).
- **Required fields** per country, for example national identifiers or address
  formats.
- **Regional sign-in options** shown by country: national eIDs, regional social
  providers (AUTH-29), and SIM-based verification where carriers support it.
- **Local language rules:** some jurisdictions require the local language (for
  example French in Quebec); tenants can make a language mandatory per country.

## 6. Messages

- Emails, SMS and push messages are sent in the user's language, falling back
  along the order in §2.
- **SMS length and cost:** non-Latin scripts use a different encoding that
  fits fewer characters per SMS segment. The editor shows the segment count per
  language and warns when a translation costs more.
- Right-to-left emails are laid out and tested as right-to-left.

## 7. Fonts and scripts

- Built-in font sets cover Latin, Greek, Cyrillic, Arabic, Hebrew, Devanagari
  and other Indic scripts, Thai, and Chinese, Japanese and Korean.
- Fonts are self-hosted and split by script, so a page only downloads what its
  language needs.
- If a brand font lacks a script used by an enabled language, the theme studio
  warns and picks a matching fallback ([16](16-look-and-feel.md)).

## 8. Translation process

**Our own product text:**

- Every UI string is extracted automatically in CI and goes through a
  continuous localization pipeline with a translation management system.
- Claude drafts the translations; professional translators and in-country
  reviewers check them before release, with screenshots for context. The owner
  contracts the translation vendor ([25](25-business-setup-guide.md)).
- Pseudo-localization runs on every build: expanded and accented text, plus
  right-to-left mirroring. It catches hard-coded strings and layouts that break.
- A release cannot ship with a missing translation in a GA language.

**Customers' text:** edited in the translation editor (UX-13), with translation
memory, a glossary, machine-translation suggestions marked for review, and
XLIFF or JSON import and export.

## 9. Commercial localization

Prices and invoices in several currencies, local taxes (VAT, GST, sales tax) and
local payment methods (OPS-01).

## 10. How it is tested

- Pseudo-localization, text expansion of 40% and more, right-to-left screenshots
  of every screen and template.
- Rendering checks for CJK, Arabic, Indic and Thai text in hosted screens,
  emails and native SDKs.
- Formatting tests against CLDR for dates, numbers, currencies, names, addresses
  and phone numbers.
- Property tests for normalization and confusable detection: equal input
  always compares equal, and known confusables are always flagged.
- Language-choice tests for every step of the order in §2, including
  `ui_locales`.

## 11. Feature IDs

| ID | Capability |
|----|-----------|
| UX-26 | Every UI, tour, message and API error in 40+ languages with professional translation and in-country review; per-admin language, time zone and formats; continuous localization pipeline with pseudo-localization |
| DYN-15 | Locale-aware formats and input: dates, times and time zones, numbers, currencies, names, addresses, phone numbers, language-aware sorting and search; Unicode normalization (PRECIS), internationalized email and domain names, confusable and hidden-character protection |
| DYN-16 | Language selection and regional content: language order including OIDC `ui_locales` and `claims_locales` and the `locale` claim; legal texts, consent ages, required fields, sign-in options and language rules per country |
