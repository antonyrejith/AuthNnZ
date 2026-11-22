package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// Appended is the outcome of Append.
type Appended struct {
	Header    Header
	Chain     uuid.UUID
	Sequence  uint64
	Hash      string
	Duplicate bool // the event ID was already stored; nothing was written
}

// Appender appends records to their chains. It is safe for concurrent use;
// appends to one chain serialize on the chain head row.
type Appender struct {
	mu         sync.Mutex
	partitions map[string]bool // months whose partitions are known to exist
	now        func() time.Time
}

// NewAppender returns an Appender. now is the clock for recorded_at (nil:
// time.Now).
func NewAppender(now func() time.Time) *Appender {
	if now == nil {
		now = time.Now
	}
	return &Appender{partitions: map[string]bool{}, now: now}
}

// Append stores raw (a record, see DecodeHeader) as the next event of its
// chain, inside tx, which must be a platform transaction. A record whose ID
// is already stored is skipped (Duplicate). The chain head row is locked
// FOR UPDATE until tx ends, so concurrent appends to a chain are serialized
// and the sequence is gapless.
func (a *Appender) Append(ctx context.Context, tx pgx.Tx, raw []byte) (Appended, error) {
	h, err := DecodeHeader(raw)
	if err != nil {
		return Appended{}, err
	}
	chain := ChainOf(h.TenantID)
	out := Appended{Header: h, Chain: chain}
	var existing int64
	err = tx.QueryRow(ctx, `SELECT sequence FROM audit.event_ids WHERE tenant_id = $1 AND id = $2`,
		chain, h.ID.UUID()).Scan(&existing)
	switch {
	case err == nil:
		out.Duplicate = true
		out.Sequence = uint64(existing) //nolint:gosec // G115: sequences are positive (CHECK constraint)
		return out, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return Appended{}, wrapDB(err, "look up the event ID")
	}
	prevSeq, prevHash, err := lockHead(ctx, tx, chain)
	if err != nil {
		return Appended{}, err
	}
	seq := prevSeq + 1
	sealed, err := Seal(raw, seq, prevHash)
	if err != nil {
		return Appended{}, err
	}
	recordedAt := a.now().UTC().Truncate(time.Microsecond)
	if err := a.ensurePartitions(ctx, tx, recordedAt); err != nil {
		return Appended{}, err
	}
	_, err = tx.Exec(ctx, `
INSERT INTO audit.events (tenant_id, recorded_at, sequence, id, occurred_at, category, event_type, outcome,
    actor_type, actor_id, target_type, target_id, channel, request_id, trace_id, event, hash, prev_hash)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
		chain, recordedAt, int64(seq), h.ID.UUID(), h.Time.UTC(), h.Category, h.Type, h.Outcome, //nolint:gosec // G115: seq <= MaxSequence
		h.Actor.Type, h.Actor.ID, h.TargetType(), h.TargetID(), h.Channel.Type, h.Request.RequestID, h.Request.TraceID,
		string(sealed.Stored), sealed.Hash, sealed.PrevHash)
	if err != nil {
		return Appended{}, wrapDB(err, "insert the event")
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit.event_ids (tenant_id, id, sequence, recorded_at) VALUES ($1, $2, $3, $4)`,
		chain, h.ID.UUID(), int64(seq), recordedAt) //nolint:gosec // G115: seq <= MaxSequence
	if err != nil {
		return Appended{}, wrapDB(err, "insert the event ID")
	}
	_, err = tx.Exec(ctx, `UPDATE audit.chain_heads SET sequence = $2, hash = $3, updated_at = now() WHERE chain_id = $1`,
		chain, int64(seq), sealed.Hash) //nolint:gosec // G115: seq <= MaxSequence
	if err != nil {
		return Appended{}, wrapDB(err, "advance the chain head")
	}
	out.Sequence = seq
	out.Hash = sealed.Hash
	return out, nil
}

// lockHead creates the chain head if missing and locks it.
func lockHead(ctx context.Context, tx pgx.Tx, chain uuid.UUID) (uint64, string, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO audit.chain_heads (chain_id, sequence, hash) VALUES ($1, 0, $2) ON CONFLICT (chain_id) DO NOTHING`,
		chain, GenesisHash); err != nil {
		return 0, "", wrapDB(err, "create the chain head")
	}
	var seq int64
	var hash string
	if err := tx.QueryRow(ctx, `SELECT sequence, hash FROM audit.chain_heads WHERE chain_id = $1 FOR UPDATE`, chain).Scan(&seq, &hash); err != nil {
		return 0, "", wrapDB(err, "lock the chain head")
	}
	if seq < 0 || seq >= MaxSequence {
		return 0, "", errs.New(errs.CodeInternal, "chain head sequence out of range: "+strconv.FormatInt(seq, 10))
	}
	return uint64(seq), hash, nil
}

// ensurePartitions makes sure the partitions for t's month and the next exist.
func (a *Appender) ensurePartitions(ctx context.Context, tx pgx.Tx, t time.Time) error {
	month := t.Format("2006-01")
	a.mu.Lock()
	known := a.partitions[month]
	a.mu.Unlock()
	if known {
		return nil
	}
	if err := EnsurePartitions(ctx, tx, t); err != nil {
		return err
	}
	a.mu.Lock()
	a.partitions[month] = true
	a.mu.Unlock()
	return nil
}

// EnsurePartitions creates the event partitions for t's month and the next
// one if they are missing (maintenance; the writer calls it on demand).
func EnsurePartitions(ctx context.Context, q Querier, t time.Time) error {
	if _, err := q.Exec(ctx, `SELECT audit.ensure_partitions($1)`, t); err != nil {
		return wrapDB(err, "create the audit event partitions")
	}
	return nil
}

// wrapDB maps a database error to an errs code, keeping context.
func wrapDB(err error, what string) error {
	mapped := db.MapError(err)
	return errs.Wrap(errs.CodeOf(mapped), fmt.Errorf("%s: %w", what, err), "audit store: "+what)
}
