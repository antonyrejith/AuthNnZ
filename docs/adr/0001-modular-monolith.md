# ADR-0001: Modular monolith with composable deployables

- Status: accepted
- Date: 2026-11-22
- Deciders: Principal Architect, under the approved plan (D-03, D-09, D-28)

## Context
The platform has many business modules (tenancy, configuration, audit,
identity, OAuth, sessions, authorization, …) and must run as multi-tenant SaaS,
dedicated cells, customer clouds, self-hosted and air-gapped installs, and as a
local emulator. Microservices from day one would multiply operational and
consistency costs before scaling data justifies them.

## Options considered
1. Microservices per module: independent scaling; high coordination,
   distributed transactions, complex local development.
2. One undivided application: simple; no boundaries, hard to split later.
3. Modular monolith: one Go module with strict, compiler-enforced module
   boundaries, compiled into several deployables.

## Decision
Option 3. One Go module `github.com/antonyrejith/portun`.

- Business modules in `internal/modules/<name>` expose a facade package; their
  implementation lives under `internal/modules/<name>/internal/`, so the Go
  compiler forbids other modules from importing it.
- Modules talk only through facades and published events, never through each
  other's tables.
- `cmd/portun` runs every module in one process with an embedded NATS server
  (local development, tests, small and air-gapped installs). Separate
  deployables (`idp-runtime`, `pdp`, `mgmt-api`, `worker`, `router`,
  `ext-runtime`, `edge-pdp`, `connector-agent`) are compositions of the same
  modules, added as their modules are completed.

## Consequences
- In-process calls and local transactions within a module; cross-module work
  goes through facades or events, so modules can be split into services later
  without rewrites.
- One build, one dependency graph, one set of quality gates.
