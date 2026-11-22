package verify

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// DBSource reads a chain from the database through q (a platform
// transaction, ideally repeatable read, or a connection as portun_platform).
type DBSource struct {
	Q     store.Querier
	Chain uuid.UUID
}

// ChainID implements Source.
func (s DBSource) ChainID() uuid.UUID { return s.Chain }

// Events implements Source.
func (s DBSource) Events(ctx context.Context, from, to uint64, fn func(store.StoredEvent) error) error {
	return store.Events(ctx, s.Q, s.Chain, from, to, fn) //nolint:wrapcheck // errs-coded
}

// Checkpoints implements Source.
func (s DBSource) Checkpoints(ctx context.Context) ([]store.Checkpoint, error) {
	return store.Checkpoints(ctx, s.Q, s.Chain) //nolint:wrapcheck // errs-coded
}

// RetentionMarks implements Source.
func (s DBSource) RetentionMarks(ctx context.Context) ([]store.RetentionMark, error) {
	return store.RetentionMarks(ctx, s.Q, s.Chain) //nolint:wrapcheck // errs-coded
}

// SigningKeys implements Source.
func (s DBSource) SigningKeys(ctx context.Context) ([]store.SigningKey, error) {
	return store.SigningKeys(ctx, s.Q) //nolint:wrapcheck // errs-coded
}

// Bounds implements Source.
func (s DBSource) Bounds(ctx context.Context, from, to time.Time) (uint64, uint64, uint64, error) {
	head, _, err := store.Head(ctx, s.Q, s.Chain)
	if err != nil {
		return 0, 0, 0, err //nolint:wrapcheck // errs-coded
	}
	var lo, hi *int64
	if !from.IsZero() || !to.IsZero() {
		lower, upper := time.Unix(0, 0).UTC(), time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
		if !from.IsZero() {
			lower = from
		}
		if !to.IsZero() {
			upper = to
		}
		err := s.Q.QueryRow(ctx, `SELECT min(sequence), max(sequence) FROM audit.events
WHERE tenant_id = $1 AND recorded_at >= $2 AND recorded_at <= $3`, s.Chain, lower, upper).Scan(&lo, &hi)
		if err != nil {
			return 0, 0, 0, errs.Wrap(errs.CodeUnavailable, err, "verify: resolve the time range")
		}
		if lo == nil {
			// No events in the time range: an empty span.
			return head, head + 1, head, nil
		}
		return head, uint64(*lo), uint64(*hi), nil //nolint:gosec // G115: CHECK sequence > 0
	}
	return head, 0, head, nil
}

// Export format (JSON Lines), one object per line:
//
//	{"kind":"header","format":"portun.audit.export.v1","chain":"<uuid>","exported_at":"…"}
//	{"kind":"signing_key","signing_key":{…}}
//	{"kind":"retention_mark","retention_mark":{…}}
//	{"kind":"checkpoint","checkpoint":{…}}
//	{"kind":"event","sequence":n,"recorded_at":"…","hash":"…","prev_hash":"…","event":{…}}

// ExportFormat identifies the export format.
const ExportFormat = "portun.audit.export.v1"

type line struct {
	Kind          string               `json:"kind"`
	Format        string               `json:"format,omitempty"`
	Chain         *uuid.UUID           `json:"chain,omitempty"`
	ExportedAt    *time.Time           `json:"exported_at,omitempty"`
	SigningKey    *store.SigningKey    `json:"signing_key,omitempty"`
	RetentionMark *store.RetentionMark `json:"retention_mark,omitempty"`
	Checkpoint    *store.Checkpoint    `json:"checkpoint,omitempty"`
	Sequence      uint64               `json:"sequence,omitempty"`
	RecordedAt    *time.Time           `json:"recorded_at,omitempty"`
	Hash          string               `json:"hash,omitempty"`
	PrevHash      string               `json:"prev_hash,omitempty"`
	Event         json.RawMessage      `json:"event,omitempty"`
}

// Export writes src's events in from..to (0: open), widened to whole
// checkpoints, with every checkpoint, retention mark and signing key, as
// JSON Lines that FileSource reads back. It returns the exported range.
func Export(ctx context.Context, src Source, from, to uint64, now time.Time, w io.Writer) (uint64, uint64, error) {
	cps, err := src.Checkpoints(ctx)
	if err != nil {
		return 0, 0, wrapSource(err, "checkpoints")
	}
	marks, err := src.RetentionMarks(ctx)
	if err != nil {
		return 0, 0, wrapSource(err, "retention marks")
	}
	keys, err := src.SigningKeys(ctx)
	if err != nil {
		return 0, 0, wrapSource(err, "signing keys")
	}
	head, _, _, err := src.Bounds(ctx, time.Time{}, time.Time{})
	if err != nil {
		return 0, 0, wrapSource(err, "bounds")
	}
	lo, hi := exportRange(cps, marks, head, from, to)
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	chain := src.ChainID()
	put := func(l line) error {
		if err := enc.Encode(l); err != nil {
			return errs.Wrap(errs.CodeUnavailable, err, "export: write")
		}
		return nil
	}
	if err := put(line{Kind: "header", Format: ExportFormat, Chain: &chain, ExportedAt: &now}); err != nil {
		return 0, 0, err
	}
	if err := putMeta(put, keys, marks, cps); err != nil {
		return 0, 0, err
	}
	if hi >= lo {
		err = src.Events(ctx, lo, hi, func(e store.StoredEvent) error {
			ra := e.RecordedAt.UTC()
			return put(line{Kind: "event", Sequence: e.Sequence, RecordedAt: &ra, Hash: e.Hash, PrevHash: e.PrevHash, Event: e.Event})
		})
		if err != nil {
			return 0, 0, err //nolint:wrapcheck // errs-coded by the callee
		}
	}
	if err := bw.Flush(); err != nil {
		return 0, 0, errs.Wrap(errs.CodeUnavailable, err, "export: write")
	}
	return lo, hi, nil
}

func putMeta(put func(line) error, keys []store.SigningKey, marks []store.RetentionMark, cps []store.Checkpoint) error {
	for i := range keys {
		if err := put(line{Kind: "signing_key", SigningKey: &keys[i]}); err != nil {
			return err
		}
	}
	for i := range marks {
		if err := put(line{Kind: "retention_mark", RetentionMark: &marks[i]}); err != nil {
			return err
		}
	}
	for i := range cps {
		if err := put(line{Kind: "checkpoint", Checkpoint: &cps[i]}); err != nil {
			return err
		}
	}
	return nil
}

// exportRange widens from..to to whole checkpoints after the last
// retention mark.
func exportRange(cps []store.Checkpoint, marks []store.RetentionMark, head, from, to uint64) (uint64, uint64) {
	base := uint64(0)
	for _, m := range marks {
		base = max(base, m.ThroughSeq)
	}
	lo, hi := max(from, base+1), head
	if to > 0 {
		hi = min(hi, to)
	}
	for _, c := range cps {
		if c.FromSeq <= lo && lo <= c.ToSeq && c.FromSeq > base {
			lo = c.FromSeq
		}
		if c.FromSeq <= hi && hi <= c.ToSeq {
			hi = c.ToSeq
		}
	}
	return lo, hi
}

// FileSource is an export read into memory.
type FileSource struct {
	chain  uuid.UUID
	events []store.StoredEvent
	cps    []store.Checkpoint
	marks  []store.RetentionMark
	keys   []store.SigningKey
}

// MaxLine bounds one line of an export.
const MaxLine = 4 << 20

// ReadExport parses an export written by Export.
func ReadExport(r io.Reader) (*FileSource, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), MaxLine)
	fs := &FileSource{}
	n := 0
	for sc.Scan() {
		n++
		if len(sc.Bytes()) == 0 {
			continue
		}
		var l line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			return nil, errs.Wrap(errs.CodeInvalidRequest, err, "export: malformed line")
		}
		if err := fs.add(n, l); err != nil {
			return nil, err
		}
	}
	if err := sc.Err(); err != nil {
		return nil, errs.Wrap(errs.CodeInvalidRequest, err, "export: read")
	}
	if n == 0 {
		return nil, errs.New(errs.CodeInvalidRequest, "export: empty file")
	}
	return fs, nil
}

func (fs *FileSource) add(n int, l line) error {
	switch {
	case n == 1 && (l.Kind != "header" || l.Format != ExportFormat || l.Chain == nil):
		return errs.New(errs.CodeInvalidRequest, "export: the first line must be a "+ExportFormat+" header")
	case l.Kind == "header":
		if n != 1 {
			return errs.New(errs.CodeInvalidRequest, "export: more than one header")
		}
		fs.chain = *l.Chain
	case l.Kind == "signing_key" && l.SigningKey != nil:
		fs.keys = append(fs.keys, *l.SigningKey)
	case l.Kind == "retention_mark" && l.RetentionMark != nil:
		fs.marks = append(fs.marks, *l.RetentionMark)
	case l.Kind == "checkpoint" && l.Checkpoint != nil:
		fs.cps = append(fs.cps, *l.Checkpoint)
	case l.Kind == "event" && l.RecordedAt != nil && len(l.Event) > 0:
		fs.events = append(fs.events, store.StoredEvent{
			Chain: fs.chain, Sequence: l.Sequence, RecordedAt: *l.RecordedAt, Hash: l.Hash, PrevHash: l.PrevHash, Event: l.Event,
		})
	default:
		return errs.New(errs.CodeInvalidRequest, "export: unknown or incomplete line")
	}
	return nil
}

// ChainID implements Source.
func (fs *FileSource) ChainID() uuid.UUID { return fs.chain }

// Events implements Source: the events in file order.
func (fs *FileSource) Events(_ context.Context, from, to uint64, fn func(store.StoredEvent) error) error {
	for _, e := range fs.events {
		if e.Sequence < from || (to > 0 && e.Sequence > to) {
			continue
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

// Checkpoints implements Source.
func (fs *FileSource) Checkpoints(context.Context) ([]store.Checkpoint, error) {
	return append([]store.Checkpoint(nil), fs.cps...), nil
}

// RetentionMarks implements Source.
func (fs *FileSource) RetentionMarks(context.Context) ([]store.RetentionMark, error) {
	return append([]store.RetentionMark(nil), fs.marks...), nil
}

// SigningKeys implements Source.
func (fs *FileSource) SigningKeys(context.Context) ([]store.SigningKey, error) {
	return append([]store.SigningKey(nil), fs.keys...), nil
}

// Bounds implements Source: the head is the highest sequence in the file or
// covered by a checkpoint, so that missing trailing events are detected.
func (fs *FileSource) Bounds(_ context.Context, from, to time.Time) (uint64, uint64, uint64, error) {
	var head uint64
	for _, e := range fs.events {
		head = max(head, e.Sequence)
	}
	for _, c := range fs.cps {
		head = max(head, c.ToSeq)
	}
	lo, hi := uint64(0), head
	if from.IsZero() && to.IsZero() {
		return head, lo, hi, nil
	}
	found := false
	for _, e := range fs.events {
		if (!from.IsZero() && e.RecordedAt.Before(from)) || (!to.IsZero() && e.RecordedAt.After(to)) {
			continue
		}
		if !found || e.Sequence < lo {
			lo = e.Sequence
		}
		if !found || e.Sequence > hi {
			hi = e.Sequence
		}
		found = true
	}
	if !found {
		return head, head + 1, head, nil
	}
	return head, lo, hi, nil
}
