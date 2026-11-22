package ids_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"pgregory.net/rapid"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

// TestEncodingVectors checks the TypeID suffix encoding against vectors
// computed independently.
//
// Covers: TEN-01
func TestEncodingVectors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		uuid string
		want string
	}{
		{"00000000-0000-0000-0000-000000000001", "ten_00000000000000000000000001"},
		{"ffffffff-ffff-ffff-ffff-ffffffffffff", "ten_7zzzzzzzzzzzzzzzzzzzzzzzzz"},
		{"01890a5d-ac96-774b-bcce-b302099a8057", "ten_01h455vb4pex5vsknk084sn02q"},
		{"0193c6e2-7b1a-7c3e-9f00-1234567890ab", "ten_01jf3e4yrtfgz9y00j6hb7h45b"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			id := ids.FromUUID[ids.TenantKind](uuid.MustParse(tt.uuid))
			if got := id.String(); got != tt.want {
				t.Fatalf("String() = %q, want %q", got, tt.want)
			}
			parsed, err := ids.Parse[ids.TenantKind](tt.want)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if parsed.UUID().String() != tt.uuid {
				t.Fatalf("Parse(%q) = %s, want %s", tt.want, parsed.UUID(), tt.uuid)
			}
		})
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"no separator", "ten01h455vb4pex5vsknk084sn02q"},
		{"wrong prefix", "usr_01h455vb4pex5vsknk084sn02q"},
		{"short suffix", "ten_01h455vb4pex5vsknk084sn02"},
		{"long suffix", "ten_01h455vb4pex5vsknk084sn02qq"},
		{"uppercase", "ten_01H455VB4PEX5VSKNK084SN02Q"},
		{"excluded letter", "ten_01h455vb4pex5vsknk084sn0iq"},
		{"overflow", "ten_8zzzzzzzzzzzzzzzzzzzzzzzzz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ids.Parse[ids.TenantKind](tt.input)
			if !errs.Is(err, errs.CodeInvalidRequest) {
				t.Fatalf("Parse(%q) error = %v, want invalid_request", tt.input, err)
			}
		})
	}
}

func TestRoundTripProperty(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		var u uuid.UUID
		copy(u[:], rapid.SliceOfN(rapid.Byte(), 16, 16).Draw(t, "bytes"))
		id := ids.FromUUID[ids.UserKind](u)
		parsed, err := ids.Parse[ids.UserKind](id.String())
		if err != nil {
			t.Fatalf("Parse(%q): %v", id, err)
		}
		if parsed != id {
			t.Fatalf("round trip %s -> %s", u, parsed.UUID())
		}
	})
}

func TestNewIsTimeOrdered(t *testing.T) {
	t.Parallel()
	prev, err := ids.New[ids.EventKind]()
	if err != nil {
		t.Fatal(err)
	}
	for range 1000 {
		next, err := ids.New[ids.EventKind]()
		if err != nil {
			t.Fatal(err)
		}
		if next.UUID().Version() != 7 {
			t.Fatalf("version = %d, want 7", next.UUID().Version())
		}
		if next.String() <= prev.String() {
			t.Fatalf("not ordered: %s then %s", prev, next)
		}
		prev = next
	}
}

func TestJSON(t *testing.T) {
	t.Parallel()
	type doc struct {
		Tenant ids.TenantID `json:"tenant"`
		User   ids.UserID   `json:"user"`
	}
	in := doc{Tenant: ids.FromUUID[ids.TenantKind](uuid.MustParse("01890a5d-ac96-774b-bcce-b302099a8057"))}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"tenant":"ten_01h455vb4pex5vsknk084sn02q","user":""}` {
		t.Fatalf("json = %s", b)
	}
	var out doc
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
	if err := json.Unmarshal([]byte(`{"tenant":"usr_01h455vb4pex5vsknk084sn02q"}`), &out); err == nil {
		t.Fatal("decoding an ID of the wrong kind succeeded")
	}
}

func TestSQL(t *testing.T) {
	t.Parallel()
	u := uuid.MustParse("01890a5d-ac96-774b-bcce-b302099a8057")
	id := ids.FromUUID[ids.TenantKind](u)
	v, err := id.Value()
	if err != nil || v != u.String() {
		t.Fatalf("Value() = %v, %v", v, err)
	}
	if v, err := (ids.TenantID{}).Value(); err != nil || v != nil {
		t.Fatalf("zero Value() = %v, %v; want nil", v, err)
	}
	tests := []struct {
		name string
		src  any
		want ids.TenantID
		ok   bool
	}{
		{"string", u.String(), id, true},
		{"text bytes", []byte(u.String()), id, true},
		{"binary bytes", u[:], id, true},
		{"array", [16]byte(u), id, true},
		{"nil", nil, ids.TenantID{}, true},
		{"bad string", "nope", ids.TenantID{}, false},
		{"bad type", 42, ids.TenantID{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got ids.TenantID
			err := got.Scan(tt.src)
			if (err == nil) != tt.ok {
				t.Fatalf("Scan(%v) error = %v, want ok=%v", tt.src, err, tt.ok)
			}
			if tt.ok && got != tt.want {
				t.Fatalf("Scan(%v) = %v, want %v", tt.src, got, tt.want)
			}
		})
	}
}

func TestPrefixesAreUniqueAndValid(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, k := range ids.AllKinds() {
		p := k.Prefix()
		if seen[p] {
			t.Errorf("duplicate prefix %q", p)
		}
		seen[p] = true
		if len(p) < 2 || len(p) > 8 {
			t.Errorf("prefix %q must be 2-8 characters", p)
		}
		for _, r := range p {
			if r < 'a' || r > 'z' {
				t.Errorf("prefix %q must be lowercase letters", p)
			}
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add("ten_01h455vb4pex5vsknk084sn02q")
	f.Add("ten_7zzzzzzzzzzzzzzzzzzzzzzzzz")
	f.Add("ten_")
	f.Fuzz(func(t *testing.T, s string) {
		id, err := ids.Parse[ids.TenantKind](s)
		if err != nil {
			return
		}
		if id.String() != s {
			t.Fatalf("Parse(%q).String() = %q", s, id.String())
		}
	})
}
