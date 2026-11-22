# ADR-0009: API conventions

- Status: accepted
- Date: 2026-11-22
- Deciders: Principal Architect, under the approved plan ([10](../plan/10-api-standards.md))

## Decision
- **Spec first:** OpenAPI 3.2 documents in `api/openapi/`, split per domain,
  bundled and linted in CI; Go server interfaces generated (generator chosen by
  the D-16 spike in T0.11).
- **Paths:** platform API under `/platform/v1`, Management API under
  `/v1/environments/{environment_id}/…`, protocol endpoints per standard.
- **JSON:** `snake_case`; IDs as type-prefixed strings; RFC 3339 UTC times.
- **Errors:** RFC 9457 `application/problem+json` with `type`
  (`https://docs.portun.io/errors/<code>`, a placeholder domain until D-01's
  domain is secured), `title`, `status`, `code`, `detail`, `instance`,
  `trace_id` and `errors[]` with JSON pointers. Codes come from
  `internal/platform/errs`.
- **Lists:** `page_size` (default 50, max 500), `page_token`,
  `next_page_token`; keyset pagination underneath.
- **Writes:** `Idempotency-Key` on `POST` (results kept 24 hours); `ETag` and
  `If-Match` on configuration objects; `Change-Reason` and `Change-Ticket`
  recorded in audit events.
- **Every operation** declares `x-required-permission` and `x-audit-event`;
  tests enforce both (ADR-0017).

## Consequences
- The console, CLI, Terraform and SDKs all use the same public APIs.
