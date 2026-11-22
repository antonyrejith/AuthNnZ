// Package secret holds values that must never appear in logs, errors or JSON.
package secret

import "log/slog"

const redacted = "[REDACTED]"

// String is a secret string. Every formatting path prints "[REDACTED]"; only
// Reveal returns the value.
type String struct {
	v string
}

// NewString wraps a secret value.
func NewString(v string) String { return String{v: v} }

// Reveal returns the secret value. Call it only where the value is used.
func (s String) Reveal() string { return s.v }

// IsEmpty reports whether no value is set.
func (s String) IsEmpty() bool { return s.v == "" }

// String implements fmt.Stringer.
func (s String) String() string { return redacted }

// GoString implements fmt.GoStringer (%#v).
func (s String) GoString() string { return redacted }

// LogValue implements slog.LogValuer.
func (s String) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText implements encoding.TextMarshaler (JSON and text encoders).
func (s String) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// UnmarshalText implements encoding.TextUnmarshaler, so secrets can be loaded
// from configuration.
func (s *String) UnmarshalText(b []byte) error {
	s.v = string(b)
	return nil
}

// Bytes is a secret byte slice with the same guarantees as String.
type Bytes struct {
	v []byte
}

// NewBytes wraps a secret value. The slice is copied.
func NewBytes(v []byte) Bytes { return Bytes{v: append([]byte(nil), v...)} }

// Reveal returns a copy of the secret value.
func (b Bytes) Reveal() []byte { return append([]byte(nil), b.v...) }

// String implements fmt.Stringer.
func (b Bytes) String() string { return redacted }

// GoString implements fmt.GoStringer (%#v).
func (b Bytes) GoString() string { return redacted }

// LogValue implements slog.LogValuer.
func (b Bytes) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText implements encoding.TextMarshaler.
func (b Bytes) MarshalText() ([]byte, error) { return []byte(redacted), nil }
