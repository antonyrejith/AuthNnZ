# ADR-0018: OpenAPI generator and API toolchain (decision D-16)

- Status: accepted
- Date: 2026-11-23
- Deciders: Principal Architect, from the measured spike in
  [D-16 generator spike](../implementation/spikes/D-16-generator-spike.md)

## Context
APIs are written spec-first in OpenAPI 3.2 ([ADR-0009](0009-api-conventions.md)).
The plan left the Go server generator and the lint and diff tools to a spike
(D-16). The spike generated, built and exercised a realistic spec (custom
methods such as `/tenants/{tenant_id}:suspend`, merge patch, `If-Match`/`ETag`,
`202` operations, RFC 9457 errors, vendor extensions, `oneOf` with a
discriminator, 3.1-style nullables) with each candidate.

## Options considered
1. **oapi-codegen v2.9.0 + kin-openapi v0.153.0 validation.** Small generated
   code and route templates on `net/http.ServeMux`. But its `std-http` output
   cannot express custom-method paths (`{id}:verb`): the workaround makes the
   generator input differ from the published spec, which defeats spec-first.
   No validation or defaults in generated code; three error hooks; merge-patch
   null handling needs an extra dependency.
2. **ogen v1.25.0.** Accepts 3.2.0; custom-method paths work through its own
   router; validation, defaults and merge-patch absent/null/value are in the
   generated code; one error handler receives every error, so all responses
   are RFC 9457 problems through `httpx.WriteProblem`. Costs: larger generated
   code, vendor extensions not exposed by generated code, and a coarse route
   label unless the operation's route is passed to `httpx`.

## Decision
- **Generator: ogen v1.25.0** (Apache-2.0), run as a pinned tool from the tools
  module; generated code is committed and checked by `make generate-check`.
- **Error handling:** `WithErrorHandler`, `WithNotFound` and
  `WithMethodNotAllowed` all route to `httpx.WriteProblem`; decode errors map
  to `invalid_request` / `validation_failed` with JSON pointers.
- **Vendor extensions** (`x-required-permission`, `x-audit-event`,
  `x-audit-reason`): read once at startup by parsing the embedded spec into a
  table keyed by `operationId`; an ogen middleware enforces the permission and
  exposes the audit event for the request. A unit test fails if any operation
  lacks either extension.
- **Route labels:** the ogen middleware records the operation's path template
  in the request state so access logs and metrics use the template, not the
  mount prefix (small `httpx` hook).
- **Spec lint: vacuum v0.42.3** (MIT) with a Portun ruleset (both extensions on
  every operation, `application/problem+json` on every error response, plus the
  recommended rules), run with `--no-update-check`.
- **Breaking changes: oasdiff v1.39.0** (Apache-2.0), `--fail-on ERR` against
  the previous release's spec in CI. Its known gap (path-parameter pattern
  changes are not reported) is covered by a Portun lint rule that forbids
  changing parameter patterns of published operations.
- **Parsing and response validation in tests: kin-openapi v0.153.0** (MIT).
- **OpenAPI 3.2 features** not supported by the toolchain (`in: querystring`,
  tag `kind`/`parent` in kin's validator) are not used until support lands.

## Consequences
- Generated servers validate requests exactly as the spec says, so handlers
  receive typed, validated input.
- The generated code and the ogen runtime (with its zap and OpenTelemetry
  dependencies) are part of the binary; all dependencies are permissively
  licensed.
- vacuum is a large tool (≈ 120 MB, slow cold build); it lives in its own
  tools module like gitleaks so it does not slow other tool builds.
