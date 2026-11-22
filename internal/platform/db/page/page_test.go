package page_test

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/db/page"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

const binding = "widgets|status=active|order=created_at,id"

func token(json string) string { return base64.RawURLEncoding.EncodeToString([]byte(json)) }

func fieldPointers(err error) []string {
	_, _, fields := errs.Public(err)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Pointer)
	}
	return out
}

func TestParse(t *testing.T) {
	t.Parallel()
	good := page.Encode(binding, []string{"2026-11-22T10:00:00Z", "0192f0c4-0000-7000-8000-000000000001"})
	tests := []struct {
		name       string
		req        page.Request
		keys       int
		wantSize   int
		wantAfter  []string
		wantFields []string // nil: success
	}{
		{"defaults", page.Request{}, 2, page.DefaultSize, nil, nil},
		{"minimum", page.Request{Size: 1}, 2, 1, nil, nil},
		{"maximum", page.Request{Size: 500}, 2, 500, nil, nil},
		{
			"with token",
			page.Request{Size: 10, Token: good},
			2,
			10,
			[]string{"2026-11-22T10:00:00Z", "0192f0c4-0000-7000-8000-000000000001"},
			nil,
		},
		{"negative size", page.Request{Size: -1}, 2, 0, nil, []string{"/page_size"}},
		{"too large", page.Request{Size: 501}, 2, 0, nil, []string{"/page_size"}},
		{"wrong key count", page.Request{Token: good}, 3, 0, nil, []string{"/page_token"}},
		{"other query", page.Request{Token: page.Encode("users", []string{"a", "b"})}, 2, 0, nil, []string{"/page_token"}},
		{"both invalid", page.Request{Size: 9999, Token: "!!"}, 2, 0, nil, []string{"/page_size", "/page_token"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, err := page.Parse(tt.req, binding, tt.keys)
			if tt.wantFields != nil {
				if !errs.Is(err, errs.CodeInvalidRequest) {
					t.Fatalf("err = %v, want CodeInvalidRequest", err)
				}
				if got := fieldPointers(err); !slices.Equal(got, tt.wantFields) {
					t.Fatalf("field pointers = %v, want %v", got, tt.wantFields)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.Size != tt.wantSize || !slices.Equal(p.After, tt.wantAfter) || p.First() != (tt.wantAfter == nil) {
				t.Fatalf("page = %+v, want size %d after %v", p, tt.wantSize, tt.wantAfter)
			}
			if p.Fetch() != p.Size+1 {
				t.Fatalf("Fetch() = %d", p.Fetch())
			}
		})
	}
}

func TestDecode(t *testing.T) {
	t.Parallel()
	hash := func(b string) string {
		// Take the query hash from a real token for b.
		raw, _ := base64.RawURLEncoding.DecodeString(page.Encode(b, []string{"x"}))
		s := string(raw)
		i := strings.Index(s, `"q":"`) + 5
		return s[i : i+strings.Index(s[i:], `"`)]
	}
	q := hash(binding)
	tests := []struct {
		name  string
		token string
		want  []string // nil: error
	}{
		{"valid", token(`{"v":1,"q":"` + q + `","k":["a","b"]}`), []string{"a", "b"}},
		{"trailing whitespace", token(`{"v":1,"q":"` + q + `","k":["a"]} `), []string{"a"}},
		{"empty", "", nil},
		{"not base64", "%%%", nil},
		{"padded base64", base64.URLEncoding.EncodeToString([]byte(`{"v":1,"q":"` + q + `","k":["a"]}`)), nil},
		{"not json", token(`hello`), nil},
		{"unknown field", token(`{"v":1,"q":"` + q + `","k":["a"],"x":1}`), nil},
		{"trailing data", token(`{"v":1,"q":"` + q + `","k":["a"]}{}`), nil},
		{"bad version", token(`{"v":2,"q":"` + q + `","k":["a"]}`), nil},
		{"other query", token(`{"v":1,"q":"` + hash("other") + `","k":["a"]}`), nil},
		{"no keys", token(`{"v":1,"q":"` + q + `","k":[]}`), nil},
		{"null keys", token(`{"v":1,"q":"` + q + `","k":null}`), nil},
		{"too many keys", token(`{"v":1,"q":"` + q + `","k":["1","2","3","4","5","6","7","8","9"]}`), nil},
		{"too long", strings.Repeat("A", 4096), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := page.Decode(tt.token, binding)
			if tt.want == nil {
				if !errs.Is(err, errs.CodeInvalidRequest) || !slices.Equal(fieldPointers(err), []string{"/page_token"}) {
					t.Fatalf("err = %v, want CodeInvalidRequest at /page_token", err)
				}
				return
			}
			if err != nil || !slices.Equal(got, tt.want) {
				t.Fatalf("Decode = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestTrim(t *testing.T) {
	t.Parallel()
	key := func(n int) []string { return []string{strings.Repeat("k", n)} }
	p, err := page.Parse(page.Request{Size: 2}, binding, 1)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		rows     []int
		wantRows []int
		wantNext bool
	}{
		{"empty", nil, nil, false},
		{"partial", []int{1}, []int{1}, false},
		{"exact", []int{1, 2}, []int{1, 2}, false},
		{"more", []int{1, 2, 3}, []int{1, 2}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rows, next := page.Trim(p, tt.rows, binding, key)
			if !slices.Equal(rows, tt.wantRows) || (next != "") != tt.wantNext {
				t.Fatalf("Trim = %v, %q", rows, next)
			}
			if next != "" {
				after, err := page.Decode(next, binding)
				if err != nil || !slices.Equal(after, key(2)) {
					t.Fatalf("next token decodes to %v, %v; want the last row's key", after, err)
				}
			}
		})
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(page.Encode(binding, []string{"a", "b"}))
	f.Add(page.Encode(binding, []string{""}))
	f.Add(token(`{"v":1,"q":"x","k":["a"]}`))
	f.Add("")
	f.Add("eyJ2IjoxfQ")
	f.Fuzz(func(t *testing.T, tok string) {
		keys, err := page.Decode(tok, binding)
		if err != nil {
			if !errs.Is(err, errs.CodeInvalidRequest) {
				t.Fatalf("error code %s, want invalid_request", errs.CodeOf(err))
			}
			return
		}
		if len(keys) == 0 {
			t.Fatal("decoded a token without keys")
		}
		// A decodable token round-trips.
		again, err := page.Decode(page.Encode(binding, keys), binding)
		if err != nil || !slices.Equal(again, keys) {
			t.Fatalf("round trip: %v, %v; want %v", again, err, keys)
		}
		if _, err := page.Decode(tok, binding+"x"); err == nil {
			t.Fatal("token accepted for another binding")
		}
	})
}
