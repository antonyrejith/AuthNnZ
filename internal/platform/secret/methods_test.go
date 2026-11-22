package secret_test

import (
	"testing"

	"github.com/antonyrejith/portun/internal/platform/secret"
)

// TestRedactingMethods calls the Stringer and GoStringer methods directly
// (fmt prefers Format, so the formatting tests never reach them) and checks
// the zero values.
//
// Covers: SEC-09
func TestRedactingMethods(t *testing.T) {
	t.Parallel()
	const value = "s3cret-value"
	tests := []struct {
		name string
		got  string
	}{
		{"String.String", secret.NewString(value).String()},
		{"String.GoString", secret.NewString(value).GoString()},
		{"Bytes.String", secret.NewBytes([]byte(value)).String()},
		{"Bytes.GoString", secret.NewBytes([]byte(value)).GoString()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.got != "[REDACTED]" {
				t.Fatalf("%s = %q, want the redaction marker", tt.name, tt.got)
			}
		})
	}
}

// TestZeroValues: the zero String and Bytes reveal nothing and are empty.
func TestZeroValues(t *testing.T) {
	t.Parallel()
	var s secret.String
	var b secret.Bytes
	if s.Reveal() != "" || !s.IsEmpty() {
		t.Fatalf("zero String reveals %q", s.Reveal())
	}
	if b.Reveal() != nil {
		t.Fatalf("zero Bytes reveals %v", b.Reveal())
	}
	if !b.Equal(secret.NewBytes(nil)) {
		t.Fatal("zero Bytes must equal an empty secret")
	}
}
