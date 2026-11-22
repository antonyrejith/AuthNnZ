// Package secret holds values that must never appear in logs, errors or JSON.
//
// The value sits behind a pointer, so even when fmt reaches a secret through
// an unexported field (where it cannot call String or Format) it prints only
// an address, never the value. Both types implement fmt.Formatter, so every
// verb prints "[REDACTED]". Neither type is comparable with ==; use Equal,
// which runs in constant time. Marshaling is one-way: the marker
// "[REDACTED]" is rejected on input so a round trip cannot silently replace a
// secret.
package secret

import (
	"crypto/subtle"
	"fmt"
	"log/slog"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

const redacted = "[REDACTED]"

// String is a secret string. Only Reveal returns the value.
type String struct {
	_ [0]func() // not comparable: use Equal
	p *string
}

// NewString wraps a secret value.
func NewString(v string) String { return String{p: &v} }

// Reveal returns the secret value. Call it only where the value is used.
func (s String) Reveal() string {
	if s.p == nil {
		return ""
	}
	return *s.p
}

// IsEmpty reports whether no value is set.
func (s String) IsEmpty() bool { return s.Reveal() == "" }

// Equal compares two secrets in constant time.
func (s String) Equal(o String) bool {
	return subtle.ConstantTimeCompare([]byte(s.Reveal()), []byte(o.Reveal())) == 1
}

// String implements fmt.Stringer.
func (String) String() string { return redacted }

// GoString implements fmt.GoStringer (%#v).
func (String) GoString() string { return redacted }

// Format implements fmt.Formatter: every verb prints the redaction marker.
func (String) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

// LogValue implements slog.LogValuer.
func (String) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText implements encoding.TextMarshaler (JSON and text encoders).
func (String) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// UnmarshalText implements encoding.TextUnmarshaler, so secrets can be loaded
// from configuration. The redaction marker itself is rejected.
func (s *String) UnmarshalText(b []byte) error {
	if string(b) == redacted {
		return errs.New(errs.CodeInvalidRequest, "secret value is the redaction marker; the real value was lost in an earlier export")
	}
	v := string(b)
	s.p = &v
	return nil
}

// Bytes is a secret byte slice with the same guarantees as String.
type Bytes struct {
	_ [0]func()
	p *[]byte
}

// NewBytes wraps a secret value. The slice is copied.
func NewBytes(v []byte) Bytes {
	c := append([]byte(nil), v...)
	return Bytes{p: &c}
}

// Reveal returns a copy of the secret value.
func (b Bytes) Reveal() []byte {
	if b.p == nil {
		return nil
	}
	return append([]byte(nil), *b.p...)
}

// Equal compares two secrets in constant time.
func (b Bytes) Equal(o Bytes) bool {
	return subtle.ConstantTimeCompare(b.Reveal(), o.Reveal()) == 1
}

// String implements fmt.Stringer.
func (Bytes) String() string { return redacted }

// GoString implements fmt.GoStringer (%#v).
func (Bytes) GoString() string { return redacted }

// Format implements fmt.Formatter: every verb prints the redaction marker.
func (Bytes) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

// LogValue implements slog.LogValuer.
func (Bytes) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText implements encoding.TextMarshaler.
func (Bytes) MarshalText() ([]byte, error) { return []byte(redacted), nil }
