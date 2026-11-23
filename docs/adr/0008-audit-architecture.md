# ADR-0008: Audit architecture

- Status: accepted (revised after review of T0.1–T0.3: R4, R7, R8)
- Date: 2026-11-22
- Deciders: Principal Architect, under the approved plan (D-21, [13](../plan/13-audit-and-change-tracking.md))

## Context
100% audit with no opt-outs, provable completeness and tamper evidence
([13 §2](../plan/13-audit-and-change-tracking.md#2-seven-completeness-guarantees)).
Every later module writes through this design, so it must cover tenant and
platform writes, several events per transaction and per-row completeness.

## Options considered
1. Application-level audit calls after each write: simple; a forgotten call or
   a crash between write and audit loses records. Rejected.
2. Database triggers writing audit rows: complete for rows, but no actor,
   reason or field semantics, and business logic in SQL. Rejected as the
   primary mechanism.
3. Same-transaction outbox written through a mandatory `audit.Tx`, plus
   independent WAL reconciliation. Chosen.

## Decision

**Record:** the envelope in [13 §3](../plan/13-audit-and-change-tracking.md#3-the-audit-record)
(who, what, when, where, how, why, result, integrity). The Go type is
`audit.Event` in `internal/modules/audit`; fields are never removed or renamed.
Each event lists the database rows it accounts for (`change.rows`: table and
primary key) and the transaction ID (`request.db_xid`).

**Registry:** every event type is registered in code with its category, OCSF
class and the classification of each field (plain, personal, secret).
Emitting an unregistered type is an error.

**Writes (`audit.Change`):**
- `Change(ctx, fn)` opens a tenant transaction (`db.InTenantTx`);
  `PlatformChange(ctx, fn)` opens a platform transaction (`db.InPlatformTx`)
  for platform-level records (partners, tenants). Both give `fn` an `audit.Tx`,
  the only type repository write methods accept.
- Inside `fn`, each logical change is recorded with `tx.Record(...)`: type,
  target, before and after values, versions, and the rows written. One
  transaction may record several events (bulk operations, provisioning).
- The field-level diff (RFC 6902 paths with before and after values) is
  computed from the before and after values. Secret fields become
  `"[REDACTED]"` plus a fingerprint. Personal fields are encrypted with the
  subject's data key before hashing, so erasure never breaks the chain.
- All records are inserted into the outbox in the same transaction, with
  `pg_current_xact_id()`. A transaction that records nothing is rolled back
  (fail closed). Any failure rolls back the whole change.
- When `fn` fails, a failure event (type, outcome `failure`, error code, actor,
  request) is written through the spool, so rejected attempts are recorded too.

**Events outside a transaction** (decisions, failed logins, stateless token
issuance, audit-log reads) go through `audit.Spool`: an append-only file
journal with per-record checksums, flushed with `fsync` before the call
returns, shipped to the bus in the background and truncated only after the
publish is acknowledged. It is sized for at least 24 hours of outage at the
expected rate. Strict mode is always on: if the journal cannot be written or
is full, the call fails.

**Chains:** one chain per tenant; platform-level events use the platform
chain (tenant ID = the nil UUID, subject `audit.platform`). Tenant lifecycle
events are written to the platform chain; events about a tenant's own
configuration and data to the tenant chain.

**Writer:** consumes the `AUDIT` stream ordered per chain (one active writer
per chain, held with a PostgreSQL advisory lock). Per chain it locks the chain
head row, assigns the next gapless sequence number, computes
`hash = SHA-256(prev_hash ‖ JCS(event without integrity fields))` (RFC 8785;
the sequence number is part of the hashed content), and appends to
`audit.events` (partitioned by month). Duplicate event IDs are ignored. After
the event is stored, the writer deletes its outbox row.

**Append-only storage:** no application role has `UPDATE` or `DELETE` on
`audit.events`, `audit.checkpoints` or chain history; a trigger rejects updates
and deletes as a second line of defence. Only the retention job (below) removes
records.

**Checkpoints:** at least every minute per chain with new events, an RFC 6962
Merkle root over the new hashes is signed through the KMS (ECDSA P-256; the key
version is stored) and timestamped by an RFC 3161 timestamp authority
(configurable URL; tests use a local timestamp authority). Stored in
`audit.checkpoints`.

**Verification** (library, API and `portunctl audit verify`): recomputes event
hashes, the chain, gapless sequences, Merkle roots, checkpoint signatures and
timestamp tokens, and works offline on exported records.

**Reconciliation (per row):** a logical-replication reader (`pglogrepl`,
`pgoutput`) follows every change to module schemas. For each committed
transaction, every inserted, updated or deleted row (table and primary key)
must be listed in `change.rows` of an audit record inserted into the outbox in
the same transaction. Excluded: a reviewed allowlist of ephemeral tables, the
audit schema itself, `outbox.messages` (delivery records written in the same
transaction as the audited change they describe), the API idempotency cache (`api.idempotency_keys`),
and migration version tables (migrations
are recorded in the platform trail by the release pipeline). Unmatched rows
raise an unaudited-change alert (metric, log, platform audit event) in
production and fail integration test suites.

**Retention:** per tenant, from its plan (GOV-01). A retention job removes the
oldest contiguous range of a chain's records and writes a signed retention
event (range, last removed hash), so verification continues from that point.
Legal hold blocks removal.

**Audit access** (search, view, export) is itself audited through the spool
(GOV-16).

**Storage location:** in B0, PostgreSQL is the store of record for audit
events, with the guarantees above. The plan's ClickHouse search store and
write-once object-storage archive
([13 §4](../plan/13-audit-and-change-tracking.md#4-architecture)) are added with
the audit log service and SIEM streaming in increment B7. This sequencing is
recorded for the owner's confirmation in the B0 report.

## Consequences
- No change can commit without its audit record; a row written without being
  declared is caught by reconciliation in CI.
- Tampering, loss and gaps are detectable by anyone with the verification tool.
- Audit write cost is part of every write's latency budget (≤ 2 ms p99).
