package platform

import (
	"testing"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// TestETagRoundTrip: the ETag of a version is accepted as If-Match and
// returns that version.
func TestETagRoundTrip(t *testing.T) {
	t.Parallel()
	for _, v := range []int64{1, 2, 9, 10, 1234567890123} {
		got, err := parseIfMatch(etag(v))
		if err != nil || got != v {
			t.Errorf("parseIfMatch(%s) = %d, %v", etag(v), got, err)
		}
	}
	if etag(3) != `"3"` {
		t.Fatalf("etag(3) = %s", etag(3))
	}
}

// TestParseIfMatch: only one strong validator that this API issued is
// accepted; weak validators, lists, the wildcard and every other form are a
// failed precondition.
func TestParseIfMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want int64
		ok   bool
	}{
		{`"1"`, 1, true},
		{`"42"`, 42, true},
		{`"9223372036854775807"`, 9223372036854775807, true},
		{`"9223372036854775808"`, 0, false}, // overflows int64
		{`"0"`, 0, false},
		{`"-1"`, 0, false},
		{`"+1"`, 0, false},
		{`"1.5"`, 0, false},
		{`"abc"`, 0, false},
		{`""`, 0, false},
		{`"`, 0, false},
		{``, 0, false},
		{`1`, 0, false},
		{`3`, 0, false},
		{`W/"3"`, 0, false},
		{`w/"3"`, 0, false},
		{`*`, 0, false},
		{`"1", "2"`, 0, false},
		{`"1"x`, 0, false},
		{` "1"`, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := parseIfMatch(tt.in)
			if tt.ok {
				if err != nil || got != tt.want {
					t.Fatalf("parseIfMatch(%q) = %d, %v", tt.in, got, err)
				}
				return
			}
			if !errs.Is(err, errs.CodePreconditionFailed) {
				t.Fatalf("parseIfMatch(%q) = %d, %v; want a failed precondition", tt.in, got, err)
			}
		})
	}
}
