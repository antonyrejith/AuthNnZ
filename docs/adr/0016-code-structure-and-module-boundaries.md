# ADR-0016: Code structure, module boundaries and dependency rules

- Status: accepted
- Date: 2026-11-22
- Deciders: Principal Architect, under the approved plan ([08 §2](../plan/08-engineering-standards.md#2-repository-and-code-structure))

## Context
Module boundaries must survive years of work by many contributors and allow
modules to become services later ([08 §2](../plan/08-engineering-standards.md)).

## Options considered
1. Conventions only: erode without enforcement.
2. One Go module per business module: heavy versioning overhead.
3. One Go module with `internal/` boundaries plus lint-enforced layer rules.
   Chosen.

## Decision
```
internal/platform/<pkg>          technical packages, no business rules
internal/modules/<m>/<m>.go      facade: Module type, New, exported service API and DTOs
internal/modules/<m>/events.go   public event types
internal/modules/<m>/<m>test/    fakes, builders, contract suites for other modules' tests
internal/modules/<m>/internal/domain/    entities, rules, domain errors (no I/O)
internal/modules/<m>/internal/app/       use cases; declares the ports it needs
internal/modules/<m>/internal/adapters/  postgres, http, grpc, events
cmd/<deployable>/                         wiring only
```
- `domain` imports only the standard library, `platform/ids` and `platform/errs`.
- `app` imports `domain` and `platform`, never `adapters`.
- `platform` never imports a module.
- Modules use each other only through facades or events.
- Dependencies are passed through constructors; no global mutable state, no
  `init()` side effects, no dependency-injection framework.

## Consequences
- The compiler enforces module privacy through `internal/`. The layer rules
  (domain imports, app never imports adapters, platform never imports a module)
  are depguard rules with file globs in `.golangci.yml`, plus banned packages.
