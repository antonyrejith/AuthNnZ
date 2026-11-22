// Package verify checks the integrity of an audit chain (ADR-0008, GOV-18),
// from the database or from an exported file, without trusting the store:
// it recomputes every event hash (RFC 8785 JCS), the hash chain links,
// gapless sequences (continuing after retention marks), the Merkle root of
// every checkpoint, checkpoint and retention mark signatures (public keys by
// version) and RFC 3161 timestamp tokens, and reports each failure with the
// sequence and kind.
package verify

import (
	"bytes"
	"context"
	"crypto"
	"crypto/x509"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/checkpoint"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/kms"
)

// Source provides one chain's stored data.
type Source interface {
	// ChainID is the chain verified.
	ChainID() uuid.UUID
	// Events calls fn for the events with from <= sequence <= to (to == 0:
	// all), in the source's order (the database: by sequence; a file: as
	// written).
	Events(ctx context.Context, from, to uint64, fn func(store.StoredEvent) error) error
	Checkpoints(ctx context.Context) ([]store.Checkpoint, error)
	RetentionMarks(ctx context.Context) ([]store.RetentionMark, error)
	SigningKeys(ctx context.Context) ([]store.SigningKey, error)
	// Bounds returns the highest stored sequence (the chain head) and, for
	// times, the sequence range of events recorded between from and to
	// (zero times are open ends).
	Bounds(ctx context.Context, from, to time.Time) (head, lo, hi uint64, err error)
}

// Keys are trusted public keys by key reference and version.
type Keys map[string]map[int]crypto.PublicKey

// Options selects the range and the trust anchors.
type Options struct {
	// FromSeq and ToSeq bound the range (0: open). The range is widened
	// to whole checkpoints so that Merkle roots can be recomputed.
	FromSeq, ToSeq uint64
	// From and To bound the range by recording time (zero: open); combined
	// with FromSeq and ToSeq, the narrower bound wins.
	From, To time.Time
	// TrustedKeys verify signatures. When nil, the keys published in the
	// source are used and Report.KeysFromSource is set: that proves
	// consistency, not authenticity.
	TrustedKeys Keys
	// TSARoots verify timestamp tokens. When nil, tokens are not verified
	// (Report.TSAUnverified counts them).
	TSARoots *x509.CertPool
	// RequireTSA reports checkpoints without a timestamp token.
	RequireTSA bool
}

// Kind classifies a failure.
type Kind string

// Failure kinds.
const (
	KindEventMalformed      Kind = "event_malformed"
	KindHashMismatch        Kind = "hash_mismatch"
	KindChainBroken         Kind = "chain_broken"
	KindSequenceGap         Kind = "sequence_gap"
	KindSequenceOrder       Kind = "sequence_order"
	KindMerkleRootMismatch  Kind = "merkle_root_mismatch"
	KindCheckpointStatement Kind = "checkpoint_statement_mismatch"
	KindCheckpointKey       Kind = "checkpoint_key_unknown"
	KindCheckpointSignature Kind = "checkpoint_signature_invalid"
	KindCheckpointLinkage   Kind = "checkpoint_linkage"
	KindTSAInvalid          Kind = "tsa_invalid"
	KindTSAMissing          Kind = "tsa_missing"
	KindRetentionMark       Kind = "retention_mark_invalid"
)

// Failure is one finding.
type Failure struct {
	Kind         Kind   `json:"kind"`
	Sequence     uint64 `json:"sequence,omitempty"`
	CheckpointID string `json:"checkpoint_id,omitempty"`
	Detail       string `json:"detail"`
}

// MaxFailures bounds the failures reported.
const MaxFailures = 1000

// Report is the result of a verification.
type Report struct {
	Chain          string    `json:"chain"`
	FromSeq        uint64    `json:"from_seq"`
	ToSeq          uint64    `json:"to_seq"`
	Events         uint64    `json:"events"`
	Checkpoints    int       `json:"checkpoints"`
	RetentionMarks int       `json:"retention_marks"`
	Unanchored     uint64    `json:"unanchored"` // events after the last checkpoint
	TSAVerified    int       `json:"tsa_verified"`
	TSAUnverified  int       `json:"tsa_unverified"`
	KeysFromSource bool      `json:"keys_from_source"`
	Truncated      bool      `json:"truncated"` // more than MaxFailures failures
	Failures       []Failure `json:"failures"`
}

// OK reports whether no failure was found.
func (r *Report) OK() bool { return len(r.Failures) == 0 }

// First returns the failure with the lowest sequence (checkpoint and mark
// failures without a sequence first), or nil.
func (r *Report) First() *Failure {
	if len(r.Failures) == 0 {
		return nil
	}
	f := r.Failures[0]
	for _, x := range r.Failures[1:] {
		if x.Sequence < f.Sequence {
			f = x
		}
	}
	return &f
}

func (r *Report) fail(kind Kind, seq uint64, cp string, format string, args ...any) {
	if len(r.Failures) >= MaxFailures {
		r.Truncated = true
		return
	}
	r.Failures = append(r.Failures, Failure{Kind: kind, Sequence: seq, CheckpointID: cp, Detail: fmt.Sprintf(format, args...)})
}

// Chain verifies src. An error means the source could not be read; integrity
// findings are in the report.
func Chain(ctx context.Context, src Source, opts Options) (*Report, error) {
	v := &verifier{src: src, opts: opts, rep: &Report{Chain: store.ChainName(src.ChainID())}}
	if err := v.loadKeys(ctx); err != nil {
		return nil, err
	}
	marks, err := src.RetentionMarks(ctx)
	if err != nil {
		return nil, wrapSource(err, "retention marks")
	}
	cps, err := src.Checkpoints(ctx)
	if err != nil {
		return nil, wrapSource(err, "checkpoints")
	}
	sort.Slice(marks, func(i, j int) bool { return marks[i].FromSeq < marks[j].FromSeq })
	sort.Slice(cps, func(i, j int) bool { return cps[i].FromSeq < cps[j].FromSeq })
	v.rep.RetentionMarks, v.rep.Checkpoints = len(marks), len(cps)
	v.cps = cps
	v.checkMarks(marks)
	v.checkCheckpoints()
	if err := v.checkEvents(ctx); err != nil {
		return nil, err
	}
	return v.rep, nil
}

type verifier struct {
	src      Source
	opts     Options
	rep      *Report
	keys     Keys
	cps      []store.Checkpoint
	base     uint64 // last sequence removed by retention
	baseHash string
}

func wrapSource(err error, what string) error {
	return errs.Wrap(errs.CodeOf(err), err, "verify: read "+what)
}

func (v *verifier) loadKeys(ctx context.Context) error {
	if v.opts.TrustedKeys != nil {
		v.keys = v.opts.TrustedKeys
		return nil
	}
	v.rep.KeysFromSource = true
	ks, err := v.src.SigningKeys(ctx)
	if err != nil {
		return wrapSource(err, "signing keys")
	}
	v.keys = Keys{}
	for _, k := range ks {
		pub, err := k.Parse()
		if err != nil {
			return err //nolint:wrapcheck // errs-coded by the callee
		}
		if v.keys[k.KeyRef] == nil {
			v.keys[k.KeyRef] = map[int]crypto.PublicKey{}
		}
		v.keys[k.KeyRef][k.Version] = pub
	}
	return nil
}

// checkSignature verifies a KMS signature over msg; it returns the failure
// kind and detail, or "" when valid.
func (v *verifier) checkSignature(ref string, version int, msg, der []byte) (Kind, string) {
	pub, ok := v.keys[ref][version]
	if !ok {
		return KindCheckpointKey, fmt.Sprintf("no trusted public key for %s version %d", ref, version)
	}
	if err := kms.Verify(pub, msg, der); err != nil {
		return KindCheckpointSignature, fmt.Sprintf("signature does not verify with %s version %d", ref, version)
	}
	return "", ""
}

func (v *verifier) checkMarks(marks []store.RetentionMark) {
	v.baseHash = store.GenesisHash
	for i, m := range marks {
		id := m.ID.String()
		want := uint64(1)
		if i > 0 {
			want = marks[i-1].ThroughSeq + 1
		}
		if m.FromSeq != want || m.ThroughSeq < m.FromSeq {
			v.rep.fail(KindRetentionMark, m.FromSeq, id, "retention range %d..%d does not continue at %d", m.FromSeq, m.ThroughSeq, want)
		}
		stmt, err := store.StatementOf(m).Canonical()
		if err != nil || !bytes.Equal(stmt, m.Payload) {
			v.rep.fail(KindRetentionMark, m.FromSeq, id, "retention mark fields do not match the signed statement")
		}
		if kind, detail := v.checkSignature(m.KeyRef, m.KeyVersion, m.Payload, m.Signature); kind != "" {
			v.rep.fail(KindRetentionMark, m.FromSeq, id, "%s: %s", kind, detail)
		}
		if !v.isCheckpointEnd(m.ThroughSeq) {
			v.rep.fail(KindRetentionMark, m.ThroughSeq, id, "retention mark does not end at a checkpoint boundary")
		}
		v.base, v.baseHash = m.ThroughSeq, m.LastHash
	}
}

func (v *verifier) isCheckpointEnd(seq uint64) bool {
	for _, c := range v.cps {
		if c.ToSeq == seq {
			return true
		}
	}
	return false
}

func (v *verifier) checkCheckpoints() {
	for i, c := range v.cps {
		id := c.ID.String()
		head, err := checkpoint.HeadOf(c).Canonical()
		if err != nil || !bytes.Equal(head, c.TreeHead) {
			v.rep.fail(KindCheckpointStatement, c.FromSeq, id, "checkpoint fields do not match the signed tree head")
		}
		if kind, detail := v.checkSignature(c.KeyRef, c.KeyVersion, c.TreeHead, c.Signature); kind != "" {
			v.rep.fail(kind, c.FromSeq, id, "%s", detail)
		}
		v.checkTSA(c)
		switch {
		case i == 0 && (c.FromSeq != 1 || c.PrevID != nil):
			v.rep.fail(KindCheckpointLinkage, c.FromSeq, id, "the first checkpoint must start at 1 with no predecessor")
		case i > 0 && (c.FromSeq != v.cps[i-1].ToSeq+1 || c.PrevID == nil || *c.PrevID != v.cps[i-1].ID):
			v.rep.fail(KindCheckpointLinkage, c.FromSeq, id, "checkpoint does not continue checkpoint %s", v.cps[i-1].ID)
		}
	}
}

func (v *verifier) checkTSA(c store.Checkpoint) {
	id := c.ID.String()
	if len(c.TSAToken) == 0 {
		if v.opts.RequireTSA {
			v.rep.fail(KindTSAMissing, c.FromSeq, id, "checkpoint has no timestamp token")
		}
		return
	}
	if v.opts.TSARoots == nil {
		v.rep.TSAUnverified++
		return
	}
	t, err := checkpoint.VerifyToken(c.TSAToken, c.Signature, v.opts.TSARoots)
	if err != nil {
		v.rep.fail(KindTSAInvalid, c.FromSeq, id, "%s", errs.CodeOf(err))
		return
	}
	if c.TSATime != nil && !c.TSATime.Equal(t) {
		v.rep.fail(KindTSAInvalid, c.FromSeq, id, "stored timestamp time differs from the token")
		return
	}
	v.rep.TSAVerified++
}

// span returns the event range to check, widened to whole checkpoints.
func (v *verifier) span(ctx context.Context) (lo, hi, head uint64, err error) {
	head, tlo, thi, err := v.src.Bounds(ctx, v.opts.From, v.opts.To)
	if err != nil {
		return 0, 0, 0, wrapSource(err, "bounds")
	}
	lo, hi = max(v.base+1, v.opts.FromSeq, tlo), head
	if v.opts.ToSeq > 0 {
		hi = min(hi, v.opts.ToSeq)
	}
	if !v.opts.To.IsZero() {
		hi = min(hi, thi)
	}
	for _, c := range v.cps {
		if c.FromSeq <= lo && lo <= c.ToSeq && c.FromSeq > v.base {
			lo = c.FromSeq
		}
		if c.FromSeq <= hi && hi <= c.ToSeq {
			hi = c.ToSeq
		}
	}
	return lo, hi, head, nil
}

func (v *verifier) checkEvents(ctx context.Context) error {
	lo, hi, head, err := v.span(ctx)
	if err != nil {
		return err
	}
	v.rep.FromSeq, v.rep.ToSeq = lo, hi
	if hi < lo {
		return nil
	}
	w := &walk{v: v, expected: lo, prevKnown: lo == v.base+1, prev: v.baseHash}
	if err := v.src.Events(ctx, lo, hi, w.event); err != nil {
		return wrapSource(err, "events")
	}
	if w.expected <= hi {
		v.rep.fail(KindSequenceGap, w.expected, "", "events %d..%d are missing", w.expected, hi)
	}
	w.flushPartial()
	var lastAnchored uint64
	if n := len(v.cps); n > 0 {
		lastAnchored = v.cps[n-1].ToSeq
	}
	if head > lastAnchored && hi > lastAnchored {
		v.rep.Unanchored = hi - max(lastAnchored, lo-1)
	}
	return nil
}

// walk is the state of the event pass.
type walk struct {
	v         *verifier
	expected  uint64
	prevKnown bool
	prev      string
	cpIdx     int
	leaves    [][]byte
	leafSeqs  []uint64
}

func (w *walk) event(e store.StoredEvent) error {
	r := w.v.rep
	switch {
	case e.Sequence < w.expected:
		r.fail(KindSequenceOrder, e.Sequence, "", "event %d appears after event %d (duplicate or reordered)", e.Sequence, w.expected-1)
		return nil
	case e.Sequence > w.expected:
		r.fail(KindSequenceGap, w.expected, "", "events %d..%d are missing", w.expected, e.Sequence-1)
		w.prevKnown = false
	}
	r.Events++
	h, hash, err := store.Recompute(e.Event)
	switch {
	case err != nil:
		r.fail(KindEventMalformed, e.Sequence, "", "%s", errs.CodeOf(err))
	case h.Sequence != e.Sequence:
		r.fail(KindHashMismatch, e.Sequence, "", "record claims sequence %d", h.Sequence)
	case hash != e.Hash || h.Integrity.Hash != e.Hash || h.Integrity.PrevHash != e.PrevHash:
		r.fail(KindHashMismatch, e.Sequence, "", "recomputed hash %s does not match stored %s", hash, e.Hash)
	}
	if w.prevKnown && e.PrevHash != w.prev {
		r.fail(KindChainBroken, e.Sequence, "", "prev_hash %s does not match the hash of event %d", e.PrevHash, e.Sequence-1)
	}
	w.prev, w.prevKnown, w.expected = e.Hash, true, e.Sequence+1
	w.merkle(e)
	return nil
}

// merkle collects leaves for the checkpoint covering e and checks its root
// when its last event has been seen.
func (w *walk) merkle(e store.StoredEvent) {
	cps := w.v.cps
	for w.cpIdx < len(cps) && cps[w.cpIdx].ToSeq < e.Sequence {
		w.flushPartial()
		w.cpIdx++
	}
	if w.cpIdx >= len(cps) || cps[w.cpIdx].FromSeq > e.Sequence {
		return
	}
	leaf, err := store.ParseHash(e.Hash)
	if err != nil {
		leaf = []byte(e.Hash)
	}
	w.leaves = append(w.leaves, leaf)
	w.leafSeqs = append(w.leafSeqs, e.Sequence)
	c := cps[w.cpIdx]
	if e.Sequence == c.ToSeq {
		w.checkRoot(c)
		w.leaves, w.leafSeqs = nil, nil
		w.cpIdx++
	}
}

func (w *walk) checkRoot(c store.Checkpoint) {
	size := c.ToSeq - c.FromSeq + 1
	if uint64(len(w.leaves)) != size || w.leafSeqs[0] != c.FromSeq {
		w.v.rep.fail(KindMerkleRootMismatch, c.FromSeq, c.ID.String(), "checkpoint covers %d events, %d present", size, len(w.leaves))
		return
	}
	if root := store.FormatHash(checkpoint.RootHash(w.leaves)); root != c.Root {
		w.v.rep.fail(KindMerkleRootMismatch, c.FromSeq, c.ID.String(), "recomputed root %s does not match %s", root, c.Root)
	}
}

// flushPartial reports a checkpoint whose last event was never seen.
func (w *walk) flushPartial() {
	if len(w.leaves) > 0 && w.cpIdx < len(w.v.cps) {
		c := w.v.cps[w.cpIdx]
		w.v.rep.fail(KindMerkleRootMismatch, c.FromSeq, c.ID.String(), "checkpoint covers %d events, %d present", c.ToSeq-c.FromSeq+1, len(w.leaves))
	}
	w.leaves, w.leafSeqs = nil, nil
}
