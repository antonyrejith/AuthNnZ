package store_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/gowebpki/jcs"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

// TestJCSVectors checks the pinned JCS implementation against the examples
// and number vectors of RFC 8785.
//
// Covers: GOV-01, GOV-18
func TestJCSVectors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
	}{
		{
			name: "rfc8785 section 3.2.2",
			in: `{
  "numbers": [333333333.33333329, 1E30, 4.50, 2e-3, 0.000000000000000000000000001],
  "string": "\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/",
  "literals": [null, true, false]
}`,
			want: `{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],"string":"€$\u000f\nA'B\"\\\\\"/"}`,
		},
		{
			name: "rfc8785 section 3.2.3 sorting",
			in: `{
  "\u20ac": "Euro Sign",
  "\r": "Carriage Return",
  "\ufb33": "Hebrew Letter Dalet With Dagesh",
  "1": "One",
  "\ud83d\ude00": "Emoji: Grinning Face",
  "\u0080": "Control",
  "\u00f6": "Latin Small Letter O With Diaeresis"
}`,
			want: "{\"\\r\":\"Carriage Return\",\"1\":\"One\",\"\u0080\":\"Control\",\"ö\":\"Latin Small Letter O With Diaeresis\",\"€\":\"Euro Sign\",\"😀\":\"Emoji: Grinning Face\",\"\ufb33\":\"Hebrew Letter Dalet With Dagesh\"}",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := jcs.Transform([]byte(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

// TestJCSNumbers checks the IEEE 754 serialization vectors of RFC 8785
// Appendix B.
//
// Covers: GOV-18
func TestJCSNumbers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		bits uint64
		want string
	}{
		{0x0000000000000000, "0"},
		{0x8000000000000000, "0"},
		{0x0000000000000001, "5e-324"},
		{0x8000000000000001, "-5e-324"},
		{0x7fefffffffffffff, "1.7976931348623157e+308"},
		{0xffefffffffffffff, "-1.7976931348623157e+308"},
		{0x4340000000000000, "9007199254740992"},
		{0xc340000000000000, "-9007199254740992"},
		{0x4430000000000000, "295147905179352830000"},
		{0x44b52d02c7e14af5, "9.999999999999997e+22"},
		{0x44b52d02c7e14af6, "1e+23"},
		{0x3eb0c6f7a0b5ed8d, "0.000001"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			got, err := jcs.NumberToJSON(math.Float64frombits(tt.bits))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("bits %016x: got %s, want %s", tt.bits, got, tt.want)
			}
		})
	}
}

func sampleEvent(t *testing.T, tenant ids.TenantID) audit.Event {
	t.Helper()
	id, err := ids.New[ids.AuditEventKind]()
	if err != nil {
		t.Fatal(err)
	}
	return audit.Event{
		ID: id, Time: time.Date(2026, 11, 21, 10, 15, 2, 418e6, time.UTC), TenantID: tenant,
		Category: audit.CategoryConfiguration, Type: "mfa_policy.updated", Outcome: audit.OutcomeSuccess,
		Actor:   audit.Actor{Type: "admin", ID: "usr_1", AMR: []string{"hwk"}},
		Channel: audit.Channel{Type: "console", IP: "203.0.113.24"},
		Target:  &audit.Target{Type: "mfa_policy", ID: "pol_1", Name: "Default"},
		Change: &audit.ChangeSet{
			BeforeVersion: 41, AfterVersion: 42,
			Diff: []audit.DiffEntry{{Op: "replace", Path: "/grace_period_days", Before: 14, After: 7}},
			Rows: []audit.RowRef{{Table: "policy.mfa", Key: []string{"a", "b"}}},
		},
		Request: audit.Request{RequestID: "req_1", TraceID: "4bf9", DBXID: "887"},
		Details: map[string]any{"note": "<b>&"},
	}
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestHeaderMirrorsEvent keeps store.Header aligned with audit.Event.
//
// Covers: GOV-01
func TestHeaderMirrorsEvent(t *testing.T) {
	t.Parallel()
	ten, err := ids.New[ids.TenantKind]()
	if err != nil {
		t.Fatal(err)
	}
	ev := sampleEvent(t, ten)
	h, err := store.DecodeHeader(marshal(t, ev))
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case h.ID != ev.ID, !h.Time.Equal(ev.Time), h.TenantID != ev.TenantID, h.Category != string(ev.Category),
		h.Type != ev.Type, h.Outcome != string(ev.Outcome), h.Actor.ID != ev.Actor.ID, h.Actor.Type != ev.Actor.Type,
		h.Channel.Type != ev.Channel.Type, h.TargetType() != ev.Target.Type, h.TargetID() != ev.Target.ID,
		h.Request.RequestID != ev.Request.RequestID, h.Request.TraceID != ev.Request.TraceID, h.Request.DBXID != ev.Request.DBXID,
		len(h.Rows()) != 1 || h.Rows()[0].Table != "policy.mfa" || strings.Join(h.Rows()[0].Key, ",") != "a,b":
		t.Errorf("header %+v does not mirror event %+v", h, ev)
	}
}

// TestDecodeHeaderRejects checks record validation.
//
// Covers: GOV-01
func TestDecodeHeaderRejects(t *testing.T) {
	t.Parallel()
	valid := string(marshal(t, sampleEvent(t, ids.TenantID{})))
	tests := map[string]string{
		"not an object":  `[1]`,
		"empty":          ``,
		"duplicate keys": `{"id":"x","id":"y"}`,
		"no id":          strings.Replace(valid, `"id":"aud_`, `"xid":"aud_`, 1),
		"nul character":  strings.Replace(valid, `"console"`, `"con\u0000sole"`, 1),
		"bad id":         strings.Replace(valid, `"id":"aud_`, `"id":"usr_`, 1),
		"no type":        strings.Replace(valid, `"type":"mfa_policy.updated"`, `"type":""`, 1),
		"too large":      `{"x":"` + strings.Repeat("a", store.MaxRecordSize) + `"}`,
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := store.DecodeHeader([]byte(in)); !errs.Is(err, errs.CodeInvalidRequest) {
				t.Errorf("DecodeHeader = %v, want invalid_request", err)
			}
		})
	}
}

// TestSealAndRecompute checks the chain hash definition and that a sealed
// record recomputes to its hash, independent of key order and whitespace.
//
// Covers: GOV-01, GOV-18
func TestSealAndRecompute(t *testing.T) {
	t.Parallel()
	raw := marshal(t, sampleEvent(t, ids.TenantID{}))
	sealed, err := store.Seal(raw, 7, store.GenesisHash)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := store.Canonical(raw, 7)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(canonical), "integrity") || !strings.Contains(string(canonical), `"sequence":7`) {
		t.Fatalf("canonical form must carry the sequence and no integrity: %s", canonical)
	}
	sum := sha256.Sum256(append(make([]byte, 32), canonical...))
	if want := "sha256:" + hex.EncodeToString(sum[:]); sealed.Hash != want {
		t.Fatalf("hash = %s, want %s", sealed.Hash, want)
	}
	// Re-encode the stored record with another key order and spacing.
	var m map[string]any
	if err := json.Unmarshal(sealed.Stored, &m); err != nil {
		t.Fatal(err)
	}
	reordered, err := json.MarshalIndent(m, "", "   ")
	if err != nil {
		t.Fatal(err)
	}
	h, hash, err := store.Recompute(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if hash != sealed.Hash || h.Integrity.Hash != sealed.Hash || h.Sequence != 7 {
		t.Errorf("recompute = %s (seq %d), want %s", hash, h.Sequence, sealed.Hash)
	}
	// Any change of content changes the hash.
	tampered := strings.Replace(string(sealed.Stored), `"console"`, `"consolE"`, 1)
	if _, hash, _ := store.Recompute([]byte(tampered)); hash == sealed.Hash {
		t.Error("tampered record recomputes to the same hash")
	}
}

// TestHashParsing checks the hash format.
//
// Covers: GOV-18
func TestHashParsing(t *testing.T) {
	t.Parallel()
	tests := map[string]bool{
		store.GenesisHash:                   true,
		"sha256:" + strings.Repeat("A", 64): false,
		"sha256:" + strings.Repeat("a", 63): false,
		"md5:" + strings.Repeat("a", 64):    false,
		"sha256:" + strings.Repeat("g", 64): false,
	}
	for in, ok := range tests {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			if _, err := store.ParseHash(in); (err == nil) != ok {
				t.Errorf("ParseHash(%q) err = %v, want ok=%v", in, err, ok)
			}
		})
	}
	if _, err := store.Canonical([]byte(`{}`), store.MaxSequence+1); err == nil {
		t.Error("Canonical accepted a sequence above 2^53-1")
	}
}

// TestChainNames checks chain naming.
//
// Covers: GOV-01
func TestChainNames(t *testing.T) {
	t.Parallel()
	ten, err := ids.New[ids.TenantKind]()
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{"platform", ten.String(), ten.UUID().String()} {
		c, err := store.ParseChain(in)
		if err != nil {
			t.Fatalf("ParseChain(%q): %v", in, err)
		}
		if in != "platform" && c != ten.UUID() {
			t.Errorf("ParseChain(%q) = %s", in, c)
		}
		if name := store.ChainName(c); in != "platform" && name != ten.String() || in == "platform" && name != "platform" {
			t.Errorf("ChainName = %s", name)
		}
	}
	if _, err := store.ParseChain("nope"); err == nil {
		t.Error("ParseChain accepted garbage")
	}
}
