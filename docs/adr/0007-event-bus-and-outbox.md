# ADR-0007: Event bus, transactional outbox and delivery guarantees

- Status: accepted (revised after review of T0.1–T0.3: R6)
- Date: 2026-11-22
- Deciders: Principal Architect, under the approved plan (D-12)

## Context
State changes must reliably produce audit records and domain events, without
two-phase commits between PostgreSQL and the bus.

## Options considered
1. Publish to the bus after commit: a crash between commit and publish loses
   the event. Rejected.
2. Two-phase commit across PostgreSQL and the bus: not supported by NATS and
   operationally fragile. Rejected.
3. Transactional outbox with a relay. Chosen.

## Decision
- **Bus:** NATS JetStream (`internal/platform/bus`). The all-in-one binary and
  tests embed a NATS server (token-protected); other deployments connect to a
  cluster.
- **Streams:** `AUDIT` (subjects `audit.<tenant>` and `audit.platform`),
  `EVENTS` (`evt.<module>.<type>`), `CONFIG` (`config.<tenant>.<env>`, carrying
  `bundle.published` notifications).
- **Outbox:** one table, `outbox.messages`, owned by the platform package
  `internal/platform/outbox`:
  `(tenant_id uuid NOT NULL, id uuid, stream text, subject text, payload jsonb, published_at timestamptz,
  xid xid8 NOT NULL DEFAULT pg_current_xact_id(), created_at)`, primary key
  `(tenant_id, id)`. Platform-level messages use the nil UUID as `tenant_id`.
  It is written in the same transaction as the change it describes.
- **Grants:** `portun_app` may only `INSERT` (row-level security checks the
  tenant). The relay and the audit writer run as `portun_platform` and may
  `SELECT`, `UPDATE` (`published_at` only) and `DELETE`.
- **Relay:** reads committed rows in order (`FOR UPDATE SKIP LOCKED`),
  publishes each with the JetStream message ID set to the row ID (broker-side
  deduplication) and waits for the acknowledgement. `EVENTS` and `CONFIG` rows
  are deleted after the acknowledgement. `AUDIT` rows are deleted by the audit
  writer only after the event is stored in `audit.events`; until then the relay
  skips rows it has already published (tracked by a `published_at` column) and
  republishes them after a timeout.
- **Consumers** are durable, acknowledge after processing, and are idempotent
  on the message ID.

## Consequences
- At-least-once delivery end to end; exactly-once effect through deduplication
  and idempotent consumers.
- If the bus is down, changes still commit and are delivered later.
- An audit record cannot be lost between the outbox and the audit store.
