# ADR-0007: Event bus, transactional outbox and delivery guarantees

- Status: accepted
- Date: 2026-11-22
- Deciders: Principal Architect, under the approved plan (D-12)

## Context
State changes must reliably produce audit records and domain events, without
two-phase commits between PostgreSQL and the bus.

## Decision
- **Bus:** NATS JetStream (`internal/platform/bus`). The all-in-one binary and
  tests embed a NATS server; other deployments connect to a cluster.
- **Streams:** `AUDIT` (subjects `audit.<tenant>`), `EVENTS` (subjects
  `evt.<module>.<type>`), `CONFIG` (subjects `config.<tenant>.<env>`).
- **Outbox:** `outbox.messages(id uuid, tenant_id uuid, stream text, subject text,
  payload jsonb, xid xid8, created_at)`, written in the same transaction as the
  change it describes; `xid` is `pg_current_xact_id()`.
- **Relay:** a worker reads committed outbox rows in order, publishes each with
  the JetStream message ID set to the row ID (broker-side deduplication), and
  deletes the row only after the publish is acknowledged.
- **Consumers** are durable, acknowledge after processing, and are idempotent
  on the message ID.

## Consequences
- At-least-once delivery end to end; exactly-once effect through deduplication
  and idempotent consumers.
- If the bus is down, changes still commit and are delivered later.
