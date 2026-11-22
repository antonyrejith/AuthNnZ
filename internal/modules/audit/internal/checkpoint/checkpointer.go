package checkpoint

import (
	"context"
	"crypto/x509"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/kms"
)

// DefaultInterval is how often checkpoints are taken (ADR-0008: at least
// every minute per chain with new events).
const DefaultInterval = time.Minute

// Config configures a Checkpointer.
type Config struct {
	DB  *db.DB
	KMS kms.KMS
	// Key is the KMS signing key (PurposeSign).
	Key kms.KeyRef
	// TSA timestamps each signature. Required: a checkpoint without an
	// external timestamp is not taken (the run fails and is retried).
	TSA      Stamper
	Interval time.Duration // default DefaultInterval
	// MaxRange bounds the events one checkpoint covers (default 100000), so
	// a backlog is checkpointed in bounded steps.
	MaxRange uint64
	Logger   *slog.Logger
	Now      func() time.Time
}

// Checkpointer takes signed checkpoints. Several instances may run: each
// range is stored once (unique range start), losers discard their work.
type Checkpointer struct {
	cfg       Config
	mu        sync.Mutex
	published map[int]bool // key versions already in audit.signing_keys
}

const defaultMaxRange = 100000

// New validates cfg.
func New(cfg Config) (*Checkpointer, error) {
	if cfg.DB == nil || cfg.KMS == nil || cfg.Key == "" || cfg.TSA == nil {
		return nil, errs.New(errs.CodeInvalidRequest, "checkpoint: DB, KMS, key and TSA are required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.MaxRange == 0 {
		cfg.MaxRange = defaultMaxRange
	}
	if cfg.Logger == nil {
		return nil, errs.New(errs.CodeInvalidRequest, "checkpoint: a logger is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Checkpointer{cfg: cfg, published: map[int]bool{}}, nil
}

// Run takes checkpoints every Interval until ctx ends. Failures are logged
// and retried at the next tick.
func (c *Checkpointer) Run(ctx context.Context) error {
	t := time.NewTicker(c.cfg.Interval)
	defer t.Stop()
	for {
		if _, err := c.RunOnce(ctx); err != nil && ctx.Err() == nil {
			c.cfg.Logger.ErrorContext(ctx, "audit checkpoint run failed", slog.String("error", err.Error()))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// RunOnce checkpoints every chain with events after its last checkpoint and
// returns the number of checkpoints stored.
func (c *Checkpointer) RunOnce(ctx context.Context) (int, error) {
	var chains []uuid.UUID
	err := c.cfg.DB.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		chains, err = store.Chains(ctx, tx)
		return err //nolint:wrapcheck // errs-coded by the callee
	}, db.ReadOnly())
	if err != nil {
		return 0, err //nolint:wrapcheck // errs-coded by store
	}
	n := 0
	var firstErr error
	for _, chain := range chains {
		for {
			stored, more, err := c.Chain(ctx, chain)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				break
			}
			if stored {
				n++
			}
			if !more {
				break
			}
		}
	}
	return n, firstErr
}

// Chain takes one checkpoint of chain if it has unanchored events. It
// reports whether one was stored and whether more events remain.
func (c *Checkpointer) Chain(ctx context.Context, chain uuid.UUID) (stored, more bool, err error) {
	var (
		last   *store.Checkpoint
		head   uint64
		hashes []string
		from   uint64
	)
	err = c.cfg.DB.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if head, _, err = store.Head(ctx, tx, chain); err != nil {
			return err //nolint:wrapcheck // errs-coded by the callee
		}
		if last, err = store.LastCheckpoint(ctx, tx, chain); err != nil {
			return err //nolint:wrapcheck // errs-coded by the callee
		}
		from = 1
		if last != nil {
			from = last.ToSeq + 1
		}
		if head < from {
			return nil
		}
		to := min(head, from+c.cfg.MaxRange-1)
		var seqs []uint64
		if hashes, seqs, err = store.EventHashes(ctx, tx, chain, from, to); err != nil {
			return err //nolint:wrapcheck // errs-coded by the callee
		}
		if uint64(len(hashes)) != to-from+1 || (len(seqs) > 0 && (seqs[0] != from || seqs[len(seqs)-1] != to)) {
			return errs.New(errs.CodeInternal, "checkpoint: the stored chain has a gap; refusing to sign it")
		}
		return nil
	}, db.RepeatableRead(), db.ReadOnly())
	if err != nil || len(hashes) == 0 {
		return false, false, err //nolint:wrapcheck // errs-coded
	}
	to := from + uint64(len(hashes)) - 1
	cp, err := c.sign(ctx, chain, from, to, hashes, last)
	if err != nil {
		return false, false, err
	}
	err = c.cfg.DB.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		stored, err = store.InsertCheckpoint(ctx, tx, cp)
		return err //nolint:wrapcheck // errs-coded by the callee
	})
	if err != nil {
		return false, false, err //nolint:wrapcheck // errs-coded
	}
	return stored, to < head, nil
}

func (c *Checkpointer) sign(ctx context.Context, chain uuid.UUID, from, to uint64, hashes []string, last *store.Checkpoint) (store.Checkpoint, error) {
	root, err := RootOf(hashes)
	if err != nil {
		return store.Checkpoint{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return store.Checkpoint{}, errs.Wrap(errs.CodeInternal, err, "checkpoint: new ID")
	}
	cp := store.Checkpoint{
		Chain: chain, ID: id, FromSeq: from, ToSeq: to, Root: root,
		Time: c.cfg.Now().UTC().Truncate(time.Microsecond), KeyRef: string(c.cfg.Key),
	}
	if last != nil {
		prev := last.ID
		cp.PrevID = &prev
	}
	head, err := HeadOf(cp).Canonical()
	if err != nil {
		return store.Checkpoint{}, err
	}
	sig, err := c.cfg.KMS.Sign(ctx, c.cfg.Key, head)
	if err != nil {
		return store.Checkpoint{}, errs.Wrap(errs.CodeOf(err), err, "checkpoint: sign the tree head")
	}
	if err := c.publishKey(ctx, sig.KeyVersion); err != nil {
		return store.Checkpoint{}, err
	}
	tok, err := c.cfg.TSA.Timestamp(ctx, sig.DER)
	if err != nil {
		return store.Checkpoint{}, errs.Wrap(errs.CodeUnavailable, err, "checkpoint: timestamp the signature")
	}
	cp.TreeHead, cp.KeyVersion, cp.Signature = head, sig.KeyVersion, sig.DER
	cp.TSAToken = tok.DER
	tsaTime := tok.Time
	cp.TSATime = &tsaTime
	return cp, nil
}

// publishKey records the public key of the signing key version, so that
// verifiers can check signatures without KMS access.
func (c *Checkpointer) publishKey(ctx context.Context, version int) error {
	c.mu.Lock()
	done := c.published[version]
	c.mu.Unlock()
	if done {
		return nil
	}
	if err := PublishKey(ctx, c.cfg.DB, c.cfg.KMS, c.cfg.Key, version); err != nil {
		return err
	}
	c.mu.Lock()
	c.published[version] = true
	c.mu.Unlock()
	return nil
}

// PublishKey stores the public key of ref's version in audit.signing_keys.
func PublishKey(ctx context.Context, d *db.DB, k kms.KMS, ref kms.KeyRef, version int) error {
	keys, err := k.PublicKeys(ctx, ref)
	if err != nil {
		return errs.Wrap(errs.CodeOf(err), err, "publish key: read the public keys")
	}
	pub, ok := keys[version]
	if !ok {
		return errs.New(errs.CodeInternal, "publish key: the KMS has no such key version")
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "publish key: encode the public key")
	}
	err = d.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return store.PublishSigningKey(ctx, tx, store.SigningKey{KeyRef: string(ref), Version: version, PublicKey: der})
	})
	return err //nolint:wrapcheck // errs-coded
}

// Proof is an inclusion proof of one event in a checkpoint.
type Proof struct {
	Checkpoint store.Checkpoint `json:"checkpoint"`
	Sequence   uint64           `json:"sequence"`
	Hash       string           `json:"hash"`
	Index      uint64           `json:"index"` // leaf index within the checkpoint
	Size       uint64           `json:"size"`  // leaves in the checkpoint
	Path       []string         `json:"path"`  // "sha256:<hex>" audit path
}

// Prove returns the inclusion proof of chain's event seq in its checkpoint.
func Prove(ctx context.Context, q store.Querier, chain uuid.UUID, seq uint64) (Proof, error) {
	cp, err := store.CheckpointCovering(ctx, q, chain, seq)
	if err != nil {
		return Proof{}, err //nolint:wrapcheck // errs-coded by the callee
	}
	hashes, _, err := store.EventHashes(ctx, q, chain, cp.FromSeq, cp.ToSeq)
	if err != nil {
		return Proof{}, err //nolint:wrapcheck // errs-coded by the callee
	}
	if uint64(len(hashes)) != cp.ToSeq-cp.FromSeq+1 {
		return Proof{}, errs.New(errs.CodeConflict, "the checkpoint's events are not all stored (removed by retention or missing)")
	}
	leaves, err := LeafData(hashes)
	if err != nil {
		return Proof{}, err
	}
	idx := seq - cp.FromSeq
	path, err := InclusionProof(leaves, int(idx)) //nolint:gosec // G115: bounded by MaxRange
	if err != nil {
		return Proof{}, err
	}
	p := Proof{Checkpoint: cp, Sequence: seq, Hash: hashes[idx], Index: idx, Size: uint64(len(leaves))}
	for _, h := range path {
		p.Path = append(p.Path, store.FormatHash(h))
	}
	return p, nil
}

// Verify checks the proof against its checkpoint's root.
func (p Proof) Verify() bool {
	leaf, err := store.ParseHash(p.Hash)
	if err != nil {
		return false
	}
	root, err := store.ParseHash(p.Checkpoint.Root)
	if err != nil {
		return false
	}
	path := make([][]byte, len(p.Path))
	for i, h := range p.Path {
		if path[i], err = store.ParseHash(h); err != nil {
			return false
		}
	}
	return VerifyInclusion(leaf, p.Index, p.Size, path, root)
}
