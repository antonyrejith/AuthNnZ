## What

## Why
<!-- Feature IDs (for example AUTH-05) and the task (for example T1.2). -->

## How it was tested
<!-- Commands run and their results. Never claim a test that was not run. -->

## Security and audit impact
<!-- Trust boundaries, tenant isolation, audit events, secrets handling. -->

## Checklist
- [ ] Tests at the right levels, with `// Covers:` feature IDs
- [ ] Audit events for every change; no secrets in logs
- [ ] API spec, docs and tracker updated
- [ ] `make gates` passes locally (pincheck lint build test test-integration tidy-check vuln secrets cover-check), and `make generate-check`
