package classify_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/classify"
)

func rules(t *testing.T, fields map[string]classify.Class) classify.Rules {
	t.Helper()
	r, err := classify.Compile(fields)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestClassOf checks pattern matching, wildcards, ancestors and strictness.
//
// Covers: GOV-01, SEC-08
func TestClassOf(t *testing.T) {
	t.Parallel()
	r := rules(t, map[string]classify.Class{
		"/email":           classify.Personal,
		"/secrets/*":       classify.Secret,
		"/profile/*/phone": classify.Personal,
		"/creds":           classify.Secret,
		"/creds/label":     classify.Plain,
		"/name":            classify.Plain,
	})
	cases := []struct {
		ptr  string
		want classify.Class
	}{
		{"/email", classify.Personal},
		{"/email/x", classify.Personal},
		{"/secrets", classify.Plain},
		{"/secrets/a", classify.Secret},
		{"/secrets/a/b", classify.Secret},
		{"/profile/home/phone", classify.Personal},
		{"/profile/home/street", classify.Plain},
		{"/creds/label", classify.Secret},
		{"/name", classify.Plain},
		{"", classify.Plain},
	}
	for _, c := range cases {
		t.Run(c.ptr, func(t *testing.T) {
			t.Parallel()
			if got := r.ClassOf(c.ptr); got != c.want {
				t.Errorf("ClassOf(%q) = %v, want %v", c.ptr, got, c.want)
			}
		})
	}
}

// TestCompileRejects checks invalid declarations.
//
// Covers: GOV-01
func TestCompileRejects(t *testing.T) {
	t.Parallel()
	for _, fields := range []map[string]classify.Class{
		{"email": classify.Plain},
		{"/": classify.Plain},
		{"/a//b": classify.Plain},
		{"/a": classify.Class(7)},
	} {
		if _, err := classify.Compile(fields); err == nil {
			t.Errorf("Compile(%v) succeeded", fields)
		}
	}
	if !rules(t, nil).Empty() {
		t.Error("no fields should compile to empty rules")
	}
	for c, s := range map[classify.Class]string{classify.Plain: "plain", classify.Personal: "personal", classify.Secret: "secret", 9: "invalid"} {
		if c.String() != s {
			t.Errorf("%d.String() = %q", int(c), c.String())
		}
	}
}

// TestApply checks that secrets are redacted with a fingerprint, personal
// values are encrypted with their event pointer, and plain values kept.
//
// Covers: GOV-01, SEC-08
func TestApply(t *testing.T) {
	t.Parallel()
	r := rules(t, map[string]classify.Class{"/secrets/*": classify.Secret, "/email": classify.Personal})
	var pointers []string
	p := classify.Protector{
		Fingerprint: func(v []byte) (string, error) { return "fp(" + string(v) + ")", nil },
		Encrypt: func(pt []byte, ptr string) (string, error) {
			pointers = append(pointers, ptr)
			return "enc(" + string(pt) + ")", nil
		},
	}
	in := map[string]any{
		"name":    "x",
		"email":   "a@example.com",
		"secrets": map[string]any{"k1": "s3cr3t", "k2": map[string]any{"n": json.Number("1")}},
		"list":    []any{"plain"},
	}
	got, err := r.Apply(in, "", "/change/diff/0/after", p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"name":  "x",
		"email": `enc("a@example.com")`,
		"secrets": map[string]any{
			"k1": classify.SecretValue{Redacted: "[REDACTED]", Fingerprint: "fp(s3cr3t)"},
			"k2": classify.SecretValue{Redacted: "[REDACTED]", Fingerprint: `fp({"n":1})`},
		},
		"list": []any{"plain"},
	}
	if d := cmp.Diff(want, got); d != "" {
		t.Errorf("Apply (-want +got):\n%s", d)
	}
	if d := cmp.Diff([]string{"/change/diff/0/after/email"}, pointers); d != "" {
		t.Errorf("pointers: %s", d)
	}
	if v, err := r.Apply(nil, "/email", "/x", p); err != nil || v != nil {
		t.Errorf("Apply(nil) = %v, %v", v, err)
	}
	bad := classify.Protector{
		Fingerprint: func([]byte) (string, error) { return "", errors.New("no key") },
		Encrypt:     func([]byte, string) (string, error) { return "", errors.New("no key") },
	}
	for _, path := range []string{"/email", "/secrets/k1"} {
		if _, err := r.Apply("v", path, path, bad); err == nil {
			t.Errorf("Apply at %s ignored a protector error", path)
		}
	}
	arr, err := rules(t, map[string]classify.Class{"/l/*/s": classify.Secret}).Apply([]any{map[string]any{"s": "x"}}, "/l", "/l", p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmtJSON(t, arr), "[REDACTED]") {
		t.Errorf("array element secret not redacted: %s", fmtJSON(t, arr))
	}
}

func fmtJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestPIIFormat round-trips the encrypted value format.
//
// Covers: SEC-08
func TestPIIFormat(t *testing.T) {
	t.Parallel()
	s := classify.FormatPII("key_x", []byte{0xff, 0x00, 0x10})
	if !strings.HasPrefix(s, "pii:v1:key_x:") {
		t.Fatalf("FormatPII = %q", s)
	}
	id, ct, err := classify.ParsePII(s)
	if err != nil || id != "key_x" || string(ct) != "\xff\x00\x10" {
		t.Errorf("ParsePII = %q, %x, %v", id, ct, err)
	}
	for _, bad := range []string{"x", "pii:v1:", "pii:v1::abc", "pii:v1:k:***"} {
		if _, _, err := classify.ParsePII(bad); err == nil {
			t.Errorf("ParsePII(%q) succeeded", bad)
		}
	}
	if got := classify.FormatFingerprint(make([]byte, 32)); got != "sha256:0000000000000000" {
		t.Errorf("FormatFingerprint = %q", got)
	}
	if string(classify.PIIAAD("e", "/p")) == string(classify.PIIAAD("e", "/q")) {
		t.Error("AAD does not bind the pointer")
	}
}
