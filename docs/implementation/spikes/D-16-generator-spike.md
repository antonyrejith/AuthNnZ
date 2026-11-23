# D-16 spike results: OpenAPI server generator, linter, differ (T0.11)

Facts only. Everything below was produced by commands run in this worktree on
Go 1.27.4 (`GOTOOLCHAIN=go1.27.4`), 2026-11-22/23. Raw outputs: `spike/*/test-output.txt`, `spike/evidence/`.

## Layout

| Path | Purpose |
|------|---------|
| `api/openapi.yaml` | Spike spec, `openapi: 3.2.0` (operations, headers, merge-patch, colon method, 202, problem+json, bearer, `x-required-permission`, `x-audit-event`, discriminated `oneOf`, `type: [string, "null"]`) |
| `api/openapi-3.1.1.yaml` | Same, `3.1.1` (only change: tag `summary` -> `description`, a 3.2-only field) |
| `api/openapi-oapi-gen.yaml` | 3.2.0 copy with `:suspend` rewritten to `/suspend` (needed by oapi-codegen std-http, see below) |
| `api/openapi-breaking.yaml`, `-3.1.1-breaking.yaml`, `-additive.yaml`, `-lint-bad.yaml` | oasdiff / vacuum fixtures |
| `api/features/f32-*.yaml` | 3.2-only constructs: `query` method, `additionalOperations`, tag `summary/kind/parent` + `$self` + `itemSchema`, `in: querystring` |
| `api/ruleset.yaml` | vacuum custom ruleset |
| `ogen/`, `oapicodegen/`, `kin/`, `tools/` | Separate Go modules (own `go.mod`/`go.sum`, `toolchain go1.27.4`); root module is untouched. `ogen/` and `oapicodegen/` use `replace github.com/antonyrejith/portun => ../..` to call the real `httpx.WriteProblem`, `httpx.Router`, `httpx.Chain`, `httpx.MaxBytes`, `errs` |

Versions (exact, `go get -tool ...@version`): ogen v1.25.0, oapi-codegen/v2 v2.9.0,
kin-openapi v0.153.0, oapi-codegen/nethttp-middleware v1.3.1 (+ runtime v1.9.1,
nullable v1.2.0), vacuum v0.42.3 (newest listed by `go list -m -versions`), oasdiff v1.39.0 (newest listed).

Licenses (read from the LICENSE file in the module cache): ogen Apache-2.0, oapi-codegen Apache-2.0,
kin-openapi MIT, vacuum MIT, oasdiff Apache-2.0. All dependency modules of every build below
resolved to MIT / BSD-2 / BSD-3 / Apache-2.0 (guessed from LICENSE text by script; no GPL/MPL/unknown file found; lists in `evidence/*-deps.txt`).

Handler harness: both generators serve the same 30-request table
(`ogen/server_test.go`, `oapicodegen/server_test.go`; all pass), behind `httpx.MaxBytes`,
with errors rendered by `httpx.WriteProblem`.

## Comparison

| Criterion | ogen v1.25.0 | oapi-codegen v2.9.0 (+ kin-openapi validator) |
|---|---|---|
| Accepts `3.2.0` | Yes. Generates. Parser rejects only `major != 3` or `minor > 2` (`openapi/parser/version.go`, read, not run for 3.3). Output byte-identical to the 3.1.1 run (`diff -rq` = 0 files) | Yes. Generates (same code as 3.1.1 apart from the embedded spec blob: 0 diff lines). Validation at request time goes through kin-openapi (next column section "kin") |
| Accepts `3.1.1` | Yes | Yes |
| 3.2-only constructs | `query` method: generated (router case `QUERY`). `additionalOperations` (COPY): generated. Tag `summary/kind/parent`, `$self`, `itemSchema`: accepted. `in: querystring`: **error** `unknown parameter location "querystring"` | Not run through the generator for the `features/` files (only via kin: see kin section) |
| Merge-patch content type | `application/merge-patch+json` enforced: `application/json` -> 400 `unexpected content type`. Typed `TenantPatch` with `OptNil*` fields: absent / null / value distinguishable (`req.Description.Set`) | Generated `UpdateTenantApplicationMergePatchPlusJSONRequestBody`; enforced by validator (`application/json` -> `header Content-Type has unexpected value`). Absent/null/value distinguishable only with `output-options.nullable-type: true` (adds `oapi-codegen/nullable` dep); without it `*string` with no `omitempty` (null == absent) |
| Colon custom method `/tenants/{id}:suspend` | Works as-is (own router). Request `.../ten_...:suspend` -> 202 | **Generation fails** for `-generate std-http`: `path "/platform/v1/tenants/{tenant_id}:suspend": segment "{tenant_id}:suspend" mixes a path parameter with literal text; net/http ServeMux requires wildcards to occupy an entire path segment (std-http-server)`. chi/gorilla/gin/echo flavours generate. Workaround run here: generate from a copy with `/{tenant_id}/suspend` and a pre-router rewrite middleware (`:` -> `/`); the published spec and the generation spec then differ |
| Header params / response headers | Typed params (`IfMatch string`), typed header responses (`TenantHeaders{ETag, Location, Response}`). Header name `ETag` is canonicalised to `Etag` with a warning (`WARN Header name is not canonical`), written on the wire correctly (`etag="\"v1\""`). `GetTenantOKHeaders` is reused for the PATCH 200 (identical shape) | `UpdateTenantParams{IfMatch}`, `CreateTenant201ResponseHeaders{ETag, Location}`; `Retry-After`/`Location` as pointers. Missing required header surfaces as `*api.RequiredHeaderError` (from the wrapper) |
| Schema defaults | Applied (`page_size`=50, body `grace_period_seconds`=0) | Not applied (`PageSize *int` nil when absent) |
| Error surfacing / own RFC 9457 bodies | `api.WithErrorHandler` receives every error (security, param decode, body decode/validate, handler errors); we call `httpx.WriteProblem` after mapping `ogenerrors.*`, `*validate.Error` (has `Fields`) to `errs`. 404 / 405 need `WithNotFound` / `WithMethodNotAllowed` (without the latter 405 is an empty body). Typed `...BadRequest = Problem` response variants also generated, but unused when returning `errs` errors. Verified bodies: `application/problem+json` with `code`, `errors[].pointer` | Three hooks: kin validator `ErrorHandlerWithOpts`, wrapper `ErrorHandlerFunc`, strict `Request/ResponseErrorHandlerFunc`; all pointed at `httpx.WriteProblem`. Error types differ per hook (`openapi3filter.RequestError`+`SchemaError`, `api.RequiredHeaderError`, `api.InvalidParamFormatError`), so mapping code covers three families. Verified bodies as left column |
| Request validation strictness | Built into generated code. Pattern, enum, min/max, minLength, required: 422 after our mapping with pointer. Unknown field with `additionalProperties:false`: error `unexpected field "bogus"` (decode error, 400 as surfaced; we could remap). Bad `oneOf` discriminator: 400 `unknown type fax`. Wrong content type: 400. Required header missing: param error | None in generated code. With kin validator in front: pattern, enum, bounds, required, unknown field (`property "bogus" is unsupported`), discriminator, null for non-nullable, content type, required header all rejected. Unknown-field and discriminator come back as `SchemaError` (422 in our mapping). Measured: 30/30 cases match expectation after mapping |
| Order of checks | security -> params -> body decode/validate -> middleware -> handler (`oas_handlers_gen.go`) | `Middlewares` option runs after param binding; validator and an authz middleware can be ordered freely; strict middleware (`operationID` argument) runs after validation |
| Vendor extensions (`x-required-permission`, `x-audit-event`) | Not exposed by generated code (no hook, no embedded spec by default). Verified workaround: `ogen.Parse(specBytes)` -> `Operation.Common.Extensions[...]` (yaml.Node) -> table keyed by `operationId`; `middleware.Request.OperationID` selects it. Table is built at startup from the spec file (needs the spec embedded in the binary) or could be generated | Embedded spec (`embedded-spec: true`) -> `api.GetSpec()` -> `op.Extensions["x-audit-event"]` (plain string); strict middleware receives `operationID`. Verified |
| `net/http` `ServeMux` + our chain | Server is an `http.Handler` with its own router; mounted on prefix `/platform/v1/` of `httpx.Router`. Consequence: access-log/metric route label is the prefix: `route=/platform/v1/` for both GET tenant and `:suspend` (measured, `ogen/route_test.go`) | Registers exact `"METHOD /path/{param}"` patterns on `httpx.Router`; label is the template: `route=/platform/v1/tenants/{tenant_id}` and `.../{tenant_id}/suspend` (measured, `oapicodegen/route_test.go`). `httpx.Router` satisfies the generated `ServeMux` interface |
| Passes our golangci-lint config | `golangci-lint run` with root `.golangci.yml`: **0 issues in generated code** (golangci-lint skips files with the "Code generated ... DO NOT EDIT" header by default). With `exclusions.generated: disable`: 160 issues (wrapcheck 50, gocognit 39, usestdlibvars 35, gochecknoglobals 17, bodyclose 3, errcheck 3, errorlint 3, gofumpt 3, staticcheck 3, tagliatelle 3, gocritic 1) | Same default: 0 in generated code. With generated checks on: 52 issues (usestdlibvars 24, wrapcheck 17, errcheck 3, tagliatelle 3, gochecknoglobals 2, staticcheck 2, gofumpt 1). Own code got one SA1019 (`GetSwagger` deprecated, use `GetSpec`) |
| Dependency footprint (modules compiled into the server binary, excluding the module itself; "new" = provides no package to the root module's build) | Generated `api` package: 28 modules, 13 new (list: `evidence/ogen-deps.txt`). Includes OpenTelemetry API, zap, go-faster/{errors,jx,yaml}, ghodss/yaml, yaml.v2, regexp2, fatih/color, go-colorable, isatty, segmentio/asm, decimal, ogen | Generated `api` package: 9 modules, 6 new (kin-openapi, runtime, nullable, go-jsonmerge, go-openapi/jsonpointer, yaml/v4). Whole wiring incl. `nethttp-middleware` (+ gorilla/mux 1.8.1, used by kin's router): 15 modules, 8 new (`evidence/oc-deps*.txt`) |
| `go.mod` size of spike module (tool + deps, indirect lines) | 24 indirect lines | 18 indirect lines |
| Generation time (warm tool binary, `go tool`) | 0.28-0.40 s (spec 365 lines) | 0.21-0.28 s |
| Cold build of the generator binary (fresh GOCACHE, modules downloaded) | 19.8 s, 23.7 MB | 20.9 s, 23.3 MB |
| Generated code size | 20 files, 11,551 lines, 270 KB (client + server + OTel hooks + JSON codecs) | 1 file, 1,678 lines, 54 KB (types, std-http server, strict server, embedded spec; no client) |
| Strictness of generated types | No `any`; sum types for `oneOf`, enums as typed strings with validators, `Opt*`/`OptNil*` wrappers | `Contact` is `union json.RawMessage` with `AsEmailContact()` etc.; enums typed; optionals `*T` |

### kin-openapi v0.153.0 (`kin/kin_test.go`, `kin/test-output.txt`)

| Check | Result |
|---|---|
| Load `openapi: 3.2.0` | Loads (`doc.OpenAPI == "3.2.0"`) |
| `doc.Validate` on 3.2.0 spike spec | **Error**: `invalid tags: tag "tenants": extra sibling fields: [summary]` (3.2 tag `summary`). Same spec with the tag field removed (`openapi-3.1.1.yaml`) validates |
| Load + `Validate` on 3.1.1 | OK |
| 3.2 features | `query` method and `additionalOperations`: Validate OK. Tag `kind/parent/summary`: Validate error (`extra sibling fields: [kind summary]`). `in: querystring`: Validate error `parameter can't have 'in' value "querystring"` |
| Request validation (`openapi3filter`, `gorillamux` router) on both docs | 15/15 cases as expected for each doc: pattern, enum, bounds, unknown field, null on non-nullable, discriminator, content type, required header, merge-patch with `null`, `type: [string, "null"]` accepts null, colon path `.../ten_...:suspend` routed and validated. The 3.2.0 doc is usable for validation even though `Validate` failed |
| Response validation | Works (`response header "ETag" missing` on an incomplete 201) |
| Extensions | `op.Extensions["x-required-permission"]` readable on all five operations in both docs |

### Linting: vacuum v0.42.3 (`go tool vacuum`, tools module)

| Check | Result |
|---|---|
| Runs as pinned Go tool | `go get -tool github.com/daveshanley/vacuum@v0.42.3`; `go tool vacuum version` -> `v0.42.3` |
| Accepts 3.2.0 and 3.1.1 | Yes; built-in recommended ruleset (56 rules) runs on both: 0 errors, warnings only (descriptions, examples, `camel-case-properties` for snake_case), identical on both |
| Custom rules for `x-required-permission`, `x-audit-event`, problem+json on 4xx/5xx (`api/ruleset.yaml`) | Clean spec: `Quality Score: 100/100`, exit 0. `openapi-lint-bad.yaml` (one op without each extension, one non-problem 404): 3 errors, one per rule, **exit 1** |
| Network | Does an update check by default; run with `--no-update-check` |
| Cost | 88 modules (55 MIT, 19 BSD-3, 12 Apache-2.0, 2 BSD-2; includes bubbletea, goja, esbuild, sqlite via modernc). Cold build 280 s wall (911 s CPU), 122 MB binary. Warm run 0.27 s. Not run: vacuum against `features/f32-*.yaml` |

### Diffing: oasdiff v1.39.0 (`go tool oasdiff`, tools module)

| Check | Result |
|---|---|
| Runs as pinned Go tool | `go tool oasdiff --version` -> `oasdiff version 1.39.0` |
| Accepts 3.2.0 / 3.1.1 and the 3.2 feature files | Yes (all `breaking` runs returned results; no version error). `in: querystring` file: `No changes detected` (diffing identical files, so not a semantic test) |
| Same file twice | `No changes detected`, exit 0 |
| Additive change (optional body property, optional query param) `--fail-on ERR` | `No breaking changes to report`, exit 0; `diff --format text` lists both additions |
| Breaking copy (new required request property `region`; `page_size` max 500->100; path-param pattern `{26}`->`{30}`; response enum value removed) | 2 errors reported, exit 1: `new-required-request-property` (POST /platform/v1/tenants), `request-parameter-max-decreased` (GET /platform/v1/tenants). Same two for the 3.1.1 pair. Enum value removal in a response is `info` only (changelog). **The path-parameter pattern tightening was not reported at any level** (`changelog --level INFO`: "No changes to report, but the specs are different") |
| Other breaking changes tried | header param `minLength` 8->16: `request-parameter-min-length-increased` error (exit 1). PATCH op removed: `api-removed-without-deprecation` error. Colon path renamed `:suspend`->`:pause`: `api-path-removed-without-deprecation` error (colon paths handled as ordinary paths) |
| Cost | 32 modules (17 MIT, 10 Apache-2.0, 5 BSD-3); cold build 21.8 s, 26.5 MB binary; warm run 0.14 s |

## OpenAPI 3.2 blockers found

- ogen: none for the spike spec; `in: querystring` unsupported (error above).
- oapi-codegen: no version error, but std-http generation fails on `{id}:verb` paths (applies to 3.1.1 equally); needs a generation copy plus rewrite middleware.
- kin-openapi (oapi-codegen's validator and its runtime dependency): `doc.Validate` rejects 3.2 tag `summary/kind/parent` and `querystring`; loading and request validation still work.
- vacuum, oasdiff: no 3.2 blocker seen on the spike spec.
- If a 3.2 -> 3.1 bundling step were used: the only construct in the spike spec that differs is tag `summary` (moved to `description`; lost: the short/long distinction). Measured: generated Go output is identical for 3.2.0 and 3.1.1 for both generators, so the spike spec loses nothing in generation. Constructs in `features/` that a 3.1 copy could not express: `query` method and `additionalOperations` (ogen generated both; a 3.1 document has no place for them), `in: querystring` (unsupported by ogen and kin anyway), tag `kind/parent`, `$self`, `itemSchema` (tolerated by ogen, no effect on generated code seen).

## Trade-offs (no recommendation)

- ogen: validation, defaults, typed optional/nullable, client and telemetry hooks in generated code; no separate validator module. Larger generated code (11.5 kLOC) and runtime closure (28 modules, 13 new, incl. OTel API and zap). Own router: colon method works, but route label is the mount prefix unless `httpx` gains a way to set it; extensions need an external lookup by `operationId`; validation errors arrive as decode errors (400 for unknown field / bad discriminator unless remapped).
- oapi-codegen: small generated file, patterns match `ServeMux` and `httpx.Route` templates, extensions readable from the embedded spec; but request validation comes from kin-openapi (extra module `nethttp-middleware`, runtime kin dependency, 3.2 `Validate` gaps), defaults are not applied, nullable needs the `nullable` option, and colon custom methods need a spec copy and a rewrite middleware (spec and generation input diverge).
- vacuum: custom rules work and gate; very heavy to build (280 s cold, 122 MB, 88 modules). oasdiff: light; one missed breaking class seen (path-param pattern).

## Gates at repo root (after adding `spike/`)

`make pincheck` pass; `make lint build` pass (0 issues, layercheck ok); `make test` pass. Root `go.mod`/`go.sum` unchanged (`git status` shows only `spike/`). Not run: `test-integration`, `tidy-check`, `secrets`, `cover-check`.
