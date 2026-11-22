# ADR-0008: Audit architecture

- Status: accepted
- Date: 2026-11-22
- Deciders: Principal Architect, under the approved plan (D-21, [13](../plan/13-audit-and-change-tracking.md))

## Context
100% audit with no opt-outs, provable completeness and tamper evidence.

## Decision
- **Registry:** every event type is registered in code with its category and
  field classification; emitting an unregistered type is an error.
- **Writes:** `audit.Change(ctx, db, event, fn)` opens a tenant transaction,
  runs `fn` with an `audit.Tx`, computes the field-level diff from the before
  and after values returned, and inserts the audit record into the outbox in
  the same transaction. If any step fails, the whole change rolls back.
- **Events outside a transaction** (decisions, failed logins, stateless token
  issuance) go through `audit.Spool`: an append-only file journal, flushed with
  `fsync` before the call returns, shipped to the bus in the background. Strict
  mode is always on: if the journal cannot be written, the call fails.
- **Writer:** consumes the `AUDIT` stream; per tenant it takes the chain head
  row lock, assigns the next gapless sequence number, computes
  `hash = SHA-256(prev_hash ‖ JCS(event without integrity fields))` (RFC 8785),
  and appends to `audit.events` (partitioned by month). Duplicate event IDs are
  ignored. Platform-level events use a reserved platform chain.
- **Checkpoints:** at least every minute, a Merkle root over new hashes is signed
  through the KMS and stored in `audit.checkpoints`.
- **Verification** (API and CLI): recomputes hashes and Merkle roots, checks
  signatures and that sequences have no gaps.
- **Reconciliation:** a logical-replication reader (`pglogrepl`, `pgoutput`)
  follows every change to module schemas; each transaction that changed
  business rows must have at least one audit outbox record with the same
  `xid`. Unmatched transactions raise an unaudited-change alert in production
  and fail integration test suites.
- **Personal data** in records is encrypted with per-subject data keys.
- **Audit access** (search, view, export) is itself audited.

## Consequences
- No change can commit without its audit record; tampering, loss and gaps are
  detectable.
- Audit write cost is part of every write's latency budget (≤ 2 ms p99).
