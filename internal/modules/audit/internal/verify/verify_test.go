package verify_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/checkpoint"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/checkpoint/tsatest"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/verify"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/kms"
	"github.com/antonyrejith/portun/internal/platform/kms/local"
)

// mem is an in-memory chain built exactly as the writer and checkpointer
// build it.
type mem struct {
	chain  uuid.UUID
	events []store.StoredEvent
	cps    []store.Checkpoint
	marks  []store.RetentionMark
	keys   []store.SigningKey
}

func (m *mem) ChainID() uuid.UUID { return m.chain }

func (m *mem) Events(_ context.Context, from, to uint64, fn func(store.StoredEvent) error) error {
	for _, e := range m.events {
		if e.Sequence >= from && (to == 0 || e.Sequence <= to) {
			if err := fn(e); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *mem) Checkpoints(context.Context) ([]store.Checkpoint, error) { return m.cps, nil }

func (m *mem) RetentionMarks(context.Context) ([]store.RetentionMark, error) { return m.marks, nil }

func (m *mem) SigningKeys(context.Context) ([]store.SigningKey, error) { return m.keys, nil }

func (m *mem) Bounds(context.Context, time.Time, time.Time) (uint64, uint64, uint64, error) {
	var head uint64
	for _, e := range m.events {
		head = max(head, e.Sequence)
	}
	return head, 0, head, nil
}

type fixture struct {
	k     kms.KMS
	ref   kms.KeyRef
	tsa   *tsatest.TSA
	start time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	k, err := local.NewMemory()
	if err != nil {
		t.Fatal(err)
	}
	ref, err := k.CreateKey(t.Context(), "audit-"+strings.ReplaceAll(uuid.NewString(), "-", ""), kms.PurposeSign)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{k: k, ref: ref, tsa: tsatest.Start(t), start: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
}

func (f *fixture) publish(t *testing.T, m *mem) {
	t.Helper()
	pubs, err := f.k.PublicKeys(t.Context(), f.ref)
	if err != nil {
		t.Fatal(err)
	}
	m.keys = nil
	for v, pub := range pubs {
		der, err := x509.MarshalPKIXPublicKey(pub)
		if err != nil {
			t.Fatal(err)
		}
		m.keys = append(m.keys, store.SigningKey{KeyRef: string(f.ref), Version: v, PublicKey: der})
	}
}

func (f *fixture) event(t *testing.T, m *mem, seq uint64) {
	t.Helper()
	id, err := ids.New[ids.AuditEventKind]()
	if err != nil {
		t.Fatal(err)
	}
	ev := audit.Event{
		ID: id, Time: f.start.Add(time.Duration(seq) * time.Second), TenantID: ids.FromUUID[ids.TenantKind](m.chain),
		Category: audit.CategoryData, Type: "user.updated", Outcome: audit.OutcomeSuccess,
		Actor: audit.Actor{Type: "admin", ID: "usr_x"}, Channel: audit.Channel{Type: "api"},
		Details: map[string]any{"n": seq},
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	prev := store.GenesisHash
	if n := len(m.events); n > 0 {
		prev = m.events[n-1].Hash
	}
	s, err := store.Seal(raw, seq, prev)
	if err != nil {
		t.Fatal(err)
	}
	m.events = append(m.events, store.StoredEvent{
		Chain: m.chain, Sequence: seq, RecordedAt: ev.Time, Hash: s.Hash, PrevHash: s.PrevHash, Event: s.Stored,
	})
}

func (f *fixture) checkpoint(t *testing.T, m *mem, from, to uint64) {
	t.Helper()
	var hashes []string
	for _, e := range m.events {
		if e.Sequence >= from && e.Sequence <= to {
			hashes = append(hashes, e.Hash)
		}
	}
	root, err := checkpoint.RootOf(hashes)
	if err != nil {
		t.Fatal(err)
	}
	cp := store.Checkpoint{
		Chain: m.chain, ID: uuid.Must(uuid.NewV7()), FromSeq: from, ToSeq: to, Root: root,
		Time: f.start.Add(time.Duration(to) * time.Minute), KeyRef: string(f.ref),
	}
	if n := len(m.cps); n > 0 {
		prev := m.cps[n-1].ID
		cp.PrevID = &prev
	}
	f.sign(t, &cp)
	m.cps = append(m.cps, cp)
}

func (f *fixture) sign(t *testing.T, cp *store.Checkpoint) {
	t.Helper()
	head, err := checkpoint.HeadOf(*cp).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := f.k.Sign(t.Context(), f.ref, head)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := checkpoint.HTTPStamper{URL: f.tsa.URL}.Timestamp(t.Context(), sig.DER)
	if err != nil {
		t.Fatal(err)
	}
	cp.TreeHead, cp.KeyVersion, cp.Signature, cp.TSAToken = head, sig.KeyVersion, sig.DER, tok.DER
	cp.TSATime = &tok.Time
}

// build returns a chain of n events with a checkpoint every `every` events
// (the tail after the last full block stays unanchored).
func (f *fixture) build(t *testing.T, n, every uint64) *mem {
	t.Helper()
	m := &mem{chain: uuid.Must(uuid.NewV7())}
	for seq := uint64(1); seq <= n; seq++ {
		f.event(t, m, seq)
		if seq%every == 0 {
			f.checkpoint(t, m, seq-every+1, seq)
		}
	}
	f.publish(t, m)
	return m
}

func (f *fixture) opts() verify.Options {
	return verify.Options{TSARoots: f.tsa.Roots, RequireTSA: true}
}

func exportLines(t *testing.T, m *mem) []string {
	t.Helper()
	var buf bytes.Buffer
	if _, _, err := verify.Export(t.Context(), m, 0, 0, time.Now(), &buf); err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(buf.String()), "\n")
}

func verifyLines(t *testing.T, lines []string, opts verify.Options) *verify.Report {
	t.Helper()
	src, err := verify.ReadExport(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := verify.Chain(t.Context(), src, opts)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// eventLine returns the index of the export line of event seq.
func eventLine(t *testing.T, lines []string, seq uint64) int {
	t.Helper()
	for i, l := range lines {
		var v struct {
			Kind     string `json:"kind"`
			Sequence uint64 `json:"sequence"`
		}
		if err := json.Unmarshal([]byte(l), &v); err != nil {
			t.Fatal(err)
		}
		if v.Kind == "event" && v.Sequence == seq {
			return i
		}
	}
	t.Fatalf("no line for event %d", seq)
	return -1
}

// TestVerifyIntact verifies an untouched chain from memory and from its export.
//
// Covers: GOV-18
func TestVerifyIntact(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.build(t, 23, 5)
	rep, err := verify.Chain(t.Context(), m, f.opts())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() || rep.Events != 23 || rep.Checkpoints != 4 || rep.TSAVerified != 4 || rep.Unanchored != 3 {
		t.Fatalf("report = %+v", rep)
	}
	rep = verifyLines(t, exportLines(t, m), f.opts())
	if !rep.OK() || rep.Events != 23 {
		t.Fatalf("export report = %+v", rep)
	}
	// A sub-range is widened to whole checkpoints.
	opts := f.opts()
	opts.FromSeq, opts.ToSeq = 7, 12
	rep, err = verify.Chain(t.Context(), m, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() || rep.FromSeq != 6 || rep.ToSeq != 15 || rep.Events != 10 {
		t.Fatalf("sub-range report = %+v", rep)
	}
}

// TestVerifyDetectsTampering covers each kind of tampering.
//
// Covers: GOV-18
func TestVerifyDetectsTampering(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tests := []struct {
		name   string
		mutate func(t *testing.T, m *mem, lines []string) []string
		kind   verify.Kind
		seq    uint64
	}{
		{
			name: "one byte of an event changed",
			mutate: func(t *testing.T, _ *mem, lines []string) []string {
				t.Helper()
				i := eventLine(t, lines, 8)
				lines[i] = strings.Replace(lines[i], `"user.updated"`, `"user.updatee"`, 1)
				return lines
			},
			kind: verify.KindHashMismatch, seq: 8,
		},
		{
			name: "one event deleted",
			mutate: func(t *testing.T, _ *mem, lines []string) []string {
				t.Helper()
				i := eventLine(t, lines, 12)
				return append(lines[:i], lines[i+1:]...)
			},
			kind: verify.KindSequenceGap, seq: 12,
		},
		{
			name: "last event deleted",
			mutate: func(t *testing.T, _ *mem, lines []string) []string {
				t.Helper()
				i := eventLine(t, lines, 20)
				return append(lines[:i], lines[i+1:]...)
			},
			kind: verify.KindSequenceGap, seq: 20,
		},
		{
			name: "two events reordered",
			mutate: func(t *testing.T, _ *mem, lines []string) []string {
				t.Helper()
				i, j := eventLine(t, lines, 3), eventLine(t, lines, 4)
				lines[i], lines[j] = lines[j], lines[i]
				return lines
			},
			kind: verify.KindSequenceGap, seq: 3,
		},
		{
			name: "event replaced with a re-hashed forgery",
			mutate: func(t *testing.T, m *mem, lines []string) []string {
				t.Helper()
				// The forger recomputes the event's own hash: the chain
				// link of the next event and the Merkle root still catch it.
				forged := &mem{chain: m.chain}
				forged.events = append(forged.events, m.events[:4]...)
				f.event(t, forged, 5)
				var buf bytes.Buffer
				if _, _, err := verify.Export(t.Context(), forged, 5, 5, time.Now(), &buf); err != nil {
					t.Fatal(err)
				}
				out := strings.Split(strings.TrimSpace(buf.String()), "\n")
				lines[eventLine(t, lines, 5)] = out[eventLine(t, out, 5)]
				return lines
			},
			kind: verify.KindChainBroken, seq: 6,
		},
		{
			name: "forged checkpoint",
			mutate: func(t *testing.T, m *mem, lines []string) []string {
				t.Helper()
				other := newFixture(t)
				cp := m.cps[1]
				cp.Root = m.cps[0].Root
				cp.KeyRef = string(other.ref)
				other.sign(t, &cp)
				return replaceCheckpoint(t, lines, cp)
			},
			kind: verify.KindCheckpointKey, seq: 6,
		},
		{
			name: "checkpoint root changed",
			mutate: func(t *testing.T, m *mem, lines []string) []string {
				t.Helper()
				cp := m.cps[2]
				cp.Root = m.cps[0].Root
				return replaceCheckpoint(t, lines, cp)
			},
			kind: verify.KindCheckpointStatement, seq: 11,
		},
		{
			name: "wrong key version",
			mutate: func(t *testing.T, m *mem, lines []string) []string {
				t.Helper()
				if err := f.k.RotateKey(t.Context(), f.ref); err != nil {
					t.Fatal(err)
				}
				pubs, err := f.k.PublicKeys(t.Context(), f.ref)
				if err != nil {
					t.Fatal(err)
				}
				der, err := x509.MarshalPKIXPublicKey(pubs[2])
				if err != nil {
					t.Fatal(err)
				}
				key, err := json.Marshal(map[string]any{"kind": "signing_key", "signing_key": store.SigningKey{KeyRef: string(f.ref), Version: 2, PublicKey: der}})
				if err != nil {
					t.Fatal(err)
				}
				cp := m.cps[0]
				cp.KeyVersion = 2
				return append(replaceCheckpoint(t, lines, cp), string(key))
			},
			kind: verify.KindCheckpointSignature, seq: 1,
		},
		{
			name: "timestamp token from another checkpoint",
			mutate: func(t *testing.T, m *mem, lines []string) []string {
				t.Helper()
				cp := m.cps[3]
				cp.TSAToken = m.cps[2].TSAToken
				return replaceCheckpoint(t, lines, cp)
			},
			kind: verify.KindTSAInvalid, seq: 16,
		},
		{
			name: "checkpoint removed",
			mutate: func(t *testing.T, m *mem, lines []string) []string {
				t.Helper()
				for i, l := range lines {
					if strings.Contains(l, m.cps[1].ID.String()) && strings.HasPrefix(l, `{"kind":"checkpoint"`) {
						return append(lines[:i], lines[i+1:]...)
					}
				}
				t.Fatal("checkpoint line not found")
				return nil
			},
			kind: verify.KindCheckpointLinkage, seq: 11,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ff := f
			m := ff.build(t, 20, 5)
			lines := tt.mutate(t, m, exportLines(t, m))
			rep := verifyLines(t, lines, ff.opts())
			if rep.OK() {
				t.Fatal("tampering not detected")
			}
			found := false
			for _, x := range rep.Failures {
				if x.Kind == tt.kind && x.Sequence == tt.seq {
					found = true
				}
			}
			if !found {
				t.Errorf("want %s at %d, got %+v", tt.kind, tt.seq, rep.Failures)
			}
		})
	}
}

func replaceCheckpoint(t *testing.T, lines []string, cp store.Checkpoint) []string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"kind": "checkpoint", "checkpoint": cp})
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range lines {
		if strings.HasPrefix(l, `{"kind":"checkpoint"`) && strings.Contains(l, cp.ID.String()) {
			lines[i] = string(b)
			return lines
		}
	}
	t.Fatal("checkpoint line not found")
	return nil
}

// TestVerifyAfterRetention verifies a chain whose oldest range was removed
// under a signed retention mark, and detects forged marks.
//
// Covers: GOV-01, GOV-18
func TestVerifyAfterRetention(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.build(t, 15, 5)
	mark := store.RetentionMark{
		Chain: m.chain, ID: uuid.Must(uuid.NewV7()), FromSeq: 1, ThroughSeq: 10, LastHash: m.events[9].Hash,
		RemovedAt: f.start.Add(time.Hour), KeyRef: string(f.ref),
	}
	payload, err := store.StatementOf(mark).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := f.k.Sign(t.Context(), f.ref, payload)
	if err != nil {
		t.Fatal(err)
	}
	mark.Payload, mark.KeyVersion, mark.Signature = payload, sig.KeyVersion, sig.DER
	m.marks = []store.RetentionMark{mark}
	m.events = m.events[10:]
	rep, err := verify.Chain(t.Context(), m, f.opts())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() || rep.FromSeq != 11 || rep.Events != 5 || rep.RetentionMarks != 1 {
		t.Fatalf("report = %+v", rep)
	}
	tests := map[string]func(*store.RetentionMark){
		"wrong last hash":       func(x *store.RetentionMark) { x.LastHash = m.events[0].Hash },
		"not at a checkpoint":   func(x *store.RetentionMark) { x.ThroughSeq = 9 },
		"signature over other":  func(x *store.RetentionMark) { x.Signature = m.cps[0].Signature },
		"does not start at one": func(x *store.RetentionMark) { x.FromSeq = 2 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bad := mark
			mutate(&bad)
			mm := *m
			mm.marks = []store.RetentionMark{bad}
			rep, err := verify.Chain(t.Context(), &mm, f.opts())
			if err != nil {
				t.Fatal(err)
			}
			if rep.OK() {
				t.Fatal("forged retention mark not detected")
			}
		})
	}
}

// TestTrustedKeys checks that pinned keys override keys from the source.
//
// Covers: GOV-18
func TestTrustedKeys(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.build(t, 10, 5)
	trusted, err := verify.ParseKeysPEM(verify.KeysPEM(m.keys))
	if err != nil {
		t.Fatal(err)
	}
	opts := f.opts()
	opts.TrustedKeys = trusted
	rep, err := verify.Chain(t.Context(), m, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() || rep.KeysFromSource {
		t.Fatalf("report = %+v", rep)
	}
	other := newFixture(t)
	om := other.build(t, 1, 1)
	opts.TrustedKeys, err = verify.ParseKeysPEM(verify.KeysPEM(om.keys))
	if err != nil {
		t.Fatal(err)
	}
	rep, err = verify.Chain(t.Context(), m, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() || rep.First().Kind != verify.KindCheckpointKey {
		t.Fatalf("untrusted signer not reported: %+v", rep)
	}
	if _, err := verify.ParseKeysPEM([]byte("nothing")); err == nil {
		t.Error("ParseKeysPEM accepted no keys")
	}
}
