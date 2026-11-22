package secret_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/secret"
)

// TestSecretsNeverPrint checks every formatting path redacts the value.
//
// Covers: SEC-09
func TestSecretsNeverPrint(t *testing.T) {
	t.Parallel()
	const value = "hunter2-very-secret"
	s := secret.NewString(value)
	b := secret.NewBytes([]byte(value))

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	logger.Info("loaded", slog.Any("password", s), slog.Any("key", b))

	jsonOut, err := json.Marshal(struct {
		S secret.String `json:"s"`
		B secret.Bytes  `json:"b"`
	}{s, b})
	if err != nil {
		t.Fatal(err)
	}
	outputs := map[string]string{
		"%v":   fmt.Sprintf("%v", s),
		"%s":   fmt.Sprintf("%s", s),
		"%+v":  fmt.Sprintf("%+v", s),
		"%#v":  fmt.Sprintf("%#v", s),
		"%q":   fmt.Sprintf("%q", s),
		"bv":   fmt.Sprintf("%v", b),
		"b#v":  fmt.Sprintf("%#v", b),
		"slog": logBuf.String(),
		"json": string(jsonOut),
		"err":  fmt.Errorf("connect with %v", s).Error(),
	}
	for name, out := range outputs {
		if strings.Contains(out, value) {
			t.Errorf("%s leaked the secret: %s", name, out)
		}
		if !strings.Contains(out, "REDACTED") {
			t.Errorf("%s does not show the redaction marker: %s", name, out)
		}
	}
	if s.Reveal() != value || string(b.Reveal()) != value {
		t.Fatal("Reveal must return the value")
	}
}

func TestBytesAreCopied(t *testing.T) {
	t.Parallel()
	src := []byte("abc")
	b := secret.NewBytes(src)
	src[0] = 'x'
	got := b.Reveal()
	got[1] = 'y'
	if string(b.Reveal()) != "abc" {
		t.Fatalf("Bytes shares memory with callers: %q", b.Reveal())
	}
}

func TestStringUnmarshalText(t *testing.T) {
	t.Parallel()
	var s secret.String
	if !s.IsEmpty() {
		t.Fatal("zero value must be empty")
	}
	if err := s.UnmarshalText([]byte("v")); err != nil || s.Reveal() != "v" || s.IsEmpty() {
		t.Fatalf("UnmarshalText: %v, %q", err, s.Reveal())
	}
}
