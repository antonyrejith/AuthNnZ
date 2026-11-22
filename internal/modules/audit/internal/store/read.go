package store

import (
	"context"
	"crypto/x509"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// Querier is what the store reads and writes through: a pgx.Tx, *pgx.Conn
// or *pgxpool.Pool.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// StoredEvent is one stored record.
type StoredEvent struct {
	Chain      uuid.UUID `json:"chain"`
	Sequence   uint64    `json:"sequence"`
	RecordedAt time.Time `json:"recorded_at"`
	Hash       string    `json:"hash"`
	PrevHash   string    `json:"prev_hash"`
	Event      []byte    `json:"-"` // the stored record (JSON)
}

// Checkpoint is a signed tree head over a range of a chain.
type Checkpoint struct {
	Chain      uuid.UUID  `json:"chain"`
	ID         uuid.UUID  `json:"id"`
	FromSeq    uint64     `json:"from_seq"`
	ToSeq      uint64     `json:"to_seq"`
	Root       string     `json:"root"`
	PrevID     *uuid.UUID `json:"prev_checkpoint_id,omitempty"`
	Time       time.Time  `json:"time"`
	TreeHead   []byte     `json:"tree_head"` // the signed bytes
	KeyRef     string     `json:"key_ref"`
	KeyVersion int        `json:"key_version"`
	Signature  []byte     `json:"signature"`
	TSAToken   []byte     `json:"tsa_token,omitempty"`
	TSATime    *time.Time `json:"tsa_time,omitempty"`
}

// RetentionMark records a removed range of a chain.
type RetentionMark struct {
	Chain      uuid.UUID `json:"chain"`
	ID         uuid.UUID `json:"id"`
	FromSeq    uint64    `json:"from_seq"`
	ThroughSeq uint64    `json:"through_seq"`
	LastHash   string    `json:"last_hash"`
	RemovedAt  time.Time `json:"removed_at"`
	Payload    []byte    `json:"payload"` // the signed bytes
	KeyRef     string    `json:"key_ref"`
	KeyVersion int       `json:"key_version"`
	Signature  []byte    `json:"signature"`
}

// SigningKey is the public half of a signing key version.
type SigningKey struct {
	KeyRef    string `json:"key_ref"`
	Version   int    `json:"version"`
	PublicKey []byte `json:"public_key"` // PKIX DER
}

// Parse returns the public key.
func (k SigningKey) Parse() (any, error) {
	pub, err := x509.ParsePKIXPublicKey(k.PublicKey)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInvalidRequest, err, "invalid public key "+k.KeyRef)
	}
	return pub, nil
}

// Head returns a chain's head (0 and GenesisHash for an empty chain).
func Head(ctx context.Context, q Querier, chain uuid.UUID) (uint64, string, error) {
	var seq int64
	var hash string
	err := q.QueryRow(ctx, `SELECT sequence, hash FROM audit.chain_heads WHERE chain_id = $1`, chain).Scan(&seq, &hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, GenesisHash, nil
		}
		return 0, "", wrapDB(err, "read the chain head")
	}
	return uint64(seq), hash, nil //nolint:gosec // G115: CHECK sequence >= 0
}

// Chains lists every chain with a head.
func Chains(ctx context.Context, q Querier) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `SELECT chain_id FROM audit.chain_heads ORDER BY chain_id`)
	if err != nil {
		return nil, wrapDB(err, "list chains")
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, wrapDB(err, "list chains")
	}
	return out, nil
}

// Events calls fn for each stored event of chain with from <= sequence <= to,
// in sequence order (to == 0: no upper bound).
func Events(ctx context.Context, q Querier, chain uuid.UUID, from, to uint64, fn func(StoredEvent) error) error {
	upper := int64(MaxSequence)
	if to > 0 {
		upper = int64(min(to, MaxSequence))
	}
	rows, err := q.Query(ctx, `
SELECT sequence, recorded_at, hash, prev_hash, event::text
FROM audit.events WHERE tenant_id = $1 AND sequence BETWEEN $2 AND $3 ORDER BY sequence`,
		chain, int64(min(from, MaxSequence)), upper)
	if err != nil {
		return wrapDB(err, "read events")
	}
	defer rows.Close()
	for rows.Next() {
		var e StoredEvent
		var seq int64
		var event string
		if err := rows.Scan(&seq, &e.RecordedAt, &e.Hash, &e.PrevHash, &event); err != nil {
			return wrapDB(err, "read events")
		}
		e.Chain = chain
		e.Sequence = uint64(seq) //nolint:gosec // G115: CHECK sequence > 0
		e.Event = []byte(event)
		if err := fn(e); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return wrapDB(err, "read events")
	}
	return nil
}

// EventHashes returns the hashes of chain's events from..to in order.
func EventHashes(ctx context.Context, q Querier, chain uuid.UUID, from, to uint64) ([]string, []uint64, error) {
	rows, err := q.Query(ctx, `SELECT sequence, hash FROM audit.events WHERE tenant_id = $1 AND sequence BETWEEN $2 AND $3 ORDER BY sequence`,
		chain, int64(from), int64(to)) //nolint:gosec // G115: sequences <= MaxSequence
	if err != nil {
		return nil, nil, wrapDB(err, "read event hashes")
	}
	defer rows.Close()
	var hashes []string
	var seqs []uint64
	for rows.Next() {
		var seq int64
		var h string
		if err := rows.Scan(&seq, &h); err != nil {
			return nil, nil, wrapDB(err, "read event hashes")
		}
		hashes = append(hashes, h)
		seqs = append(seqs, uint64(seq)) //nolint:gosec // G115: CHECK sequence > 0
	}
	if err := rows.Err(); err != nil {
		return nil, nil, wrapDB(err, "read event hashes")
	}
	return hashes, seqs, nil
}

const checkpointColumns = `tenant_id, id, from_seq, to_seq, root, prev_id, created_at, tree_head, key_ref, key_version, signature, tsa_token, tsa_time`

func scanCheckpoint(row pgx.Row) (Checkpoint, error) {
	var c Checkpoint
	var from, to int64
	err := row.Scan(&c.Chain, &c.ID, &from, &to, &c.Root, &c.PrevID, &c.Time, &c.TreeHead, &c.KeyRef, &c.KeyVersion,
		&c.Signature, &c.TSAToken, &c.TSATime)
	c.FromSeq, c.ToSeq = uint64(from), uint64(to) //nolint:gosec // G115: CHECK from_seq > 0, to_seq >= from_seq
	c.Time = c.Time.UTC()
	if c.TSATime != nil {
		t := c.TSATime.UTC()
		c.TSATime = &t
	}
	return c, err //nolint:wrapcheck // wrapped by callers
}

// Checkpoints returns chain's checkpoints ordered by from_seq.
func Checkpoints(ctx context.Context, q Querier, chain uuid.UUID) ([]Checkpoint, error) {
	rows, err := q.Query(ctx, `SELECT `+checkpointColumns+` FROM audit.checkpoints WHERE tenant_id = $1 ORDER BY from_seq`, chain)
	if err != nil {
		return nil, wrapDB(err, "read checkpoints")
	}
	defer rows.Close()
	var out []Checkpoint
	for rows.Next() {
		c, err := scanCheckpoint(rows)
		if err != nil {
			return nil, wrapDB(err, "read checkpoints")
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB(err, "read checkpoints")
	}
	return out, nil
}

// LastCheckpoint returns chain's newest checkpoint, or nil.
func LastCheckpoint(ctx context.Context, q Querier, chain uuid.UUID) (*Checkpoint, error) {
	c, err := scanCheckpoint(q.QueryRow(ctx, `SELECT `+checkpointColumns+` FROM audit.checkpoints WHERE tenant_id = $1 ORDER BY to_seq DESC LIMIT 1`, chain))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil //nolint:nilnil // no checkpoint yet
		}
		return nil, wrapDB(err, "read the last checkpoint")
	}
	return &c, nil
}

// CheckpointCovering returns the checkpoint whose range contains seq.
func CheckpointCovering(ctx context.Context, q Querier, chain uuid.UUID, seq uint64) (Checkpoint, error) {
	c, err := scanCheckpoint(q.QueryRow(ctx, `SELECT `+checkpointColumns+` FROM audit.checkpoints
WHERE tenant_id = $1 AND from_seq <= $2 AND to_seq >= $2`, chain, int64(seq))) //nolint:gosec // G115: seq <= MaxSequence
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c, errs.New(errs.CodeNotFound, "no checkpoint covers this event yet")
		}
		return c, wrapDB(err, "read the checkpoint")
	}
	return c, nil
}

// InsertCheckpoint stores c; it reports false when a checkpoint for the same
// range start already exists (another instance won).
func InsertCheckpoint(ctx context.Context, q Querier, c Checkpoint) (bool, error) {
	tag, err := q.Exec(ctx, `INSERT INTO audit.checkpoints (`+checkpointColumns+`)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) ON CONFLICT DO NOTHING`,
		c.Chain, c.ID, int64(c.FromSeq), int64(c.ToSeq), c.Root, c.PrevID, c.Time, c.TreeHead, c.KeyRef, c.KeyVersion, //nolint:gosec // G115: sequences <= MaxSequence
		c.Signature, c.TSAToken, c.TSATime)
	if err != nil {
		return false, wrapDB(err, "insert the checkpoint")
	}
	return tag.RowsAffected() == 1, nil
}

// RetentionMarks returns chain's retention marks ordered by from_seq.
func RetentionMarks(ctx context.Context, q Querier, chain uuid.UUID) ([]RetentionMark, error) {
	rows, err := q.Query(ctx, `SELECT tenant_id, id, from_seq, through_seq, last_hash, removed_at, payload, key_ref, key_version, signature
FROM audit.retention_marks WHERE tenant_id = $1 ORDER BY from_seq`, chain)
	if err != nil {
		return nil, wrapDB(err, "read retention marks")
	}
	defer rows.Close()
	var out []RetentionMark
	for rows.Next() {
		var m RetentionMark
		var from, through int64
		if err := rows.Scan(&m.Chain, &m.ID, &from, &through, &m.LastHash, &m.RemovedAt, &m.Payload, &m.KeyRef, &m.KeyVersion, &m.Signature); err != nil {
			return nil, wrapDB(err, "read retention marks")
		}
		m.FromSeq, m.ThroughSeq = uint64(from), uint64(through) //nolint:gosec // G115: CHECK constraints
		m.RemovedAt = m.RemovedAt.UTC()
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB(err, "read retention marks")
	}
	return out, nil
}

// SigningKeys returns every published signing key version.
func SigningKeys(ctx context.Context, q Querier) ([]SigningKey, error) {
	rows, err := q.Query(ctx, `SELECT key_ref, version, public_key FROM audit.signing_keys ORDER BY key_ref, version`)
	if err != nil {
		return nil, wrapDB(err, "read signing keys")
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (SigningKey, error) {
		var k SigningKey
		err := r.Scan(&k.KeyRef, &k.Version, &k.PublicKey)
		return k, err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return nil, wrapDB(err, "read signing keys")
	}
	return out, nil
}

// PublishSigningKey records a public key version (idempotent).
func PublishSigningKey(ctx context.Context, q Querier, k SigningKey) error {
	if _, err := q.Exec(ctx, `INSERT INTO audit.signing_keys (key_ref, version, public_key) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		k.KeyRef, k.Version, k.PublicKey); err != nil {
		return wrapDB(err, "publish the signing key")
	}
	return nil
}
