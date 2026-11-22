// Package classify protects audit record values by their declared
// classification: secret values are replaced by a redaction marker and a
// keyed fingerprint, personal values are encrypted under the data subject's
// key, plain values stay as they are.
//
// Fields are declared with JSON Pointer patterns, for example "/email" or
// "/secrets/*", where "*" matches exactly one reference token. A value is
// classified by every pattern that matches its pointer or one of its
// ancestors' pointers, and the strictest class wins (Secret over Personal over
// Plain): a value inside a secret object is always secret.
package classify

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/diff"
)

// Class is the classification of a field.
type Class int

// Classes, in increasing strictness.
const (
	Plain Class = iota
	Personal
	Secret
)

// String returns "plain", "personal" or "secret".
func (c Class) String() string {
	switch c {
	case Plain:
		return "plain"
	case Personal:
		return "personal"
	case Secret:
		return "secret"
	default:
		return "invalid"
	}
}

// Valid reports whether c is a defined class.
func (c Class) Valid() bool { return c >= Plain && c <= Secret }

// Redacted replaces secret values.
const Redacted = "[REDACTED]"

// PIIPrefix starts every encrypted personal value:
// "pii:v1:<data key ID>:<base64url ciphertext>".
const PIIPrefix = "pii:v1:"

// FingerprintPrefix starts a secret's fingerprint.
const FingerprintPrefix = "sha256:"

type rule struct {
	tokens []string // escaped tokens; "*" is a wildcard
	class  Class
}

// Rules is a compiled set of field patterns.
type Rules struct {
	rules []rule
}

// Compile validates and compiles patterns. A pattern is "/" followed by
// escaped tokens separated by "/"; "" (the root) is not allowed.
func Compile(fields map[string]Class) (Rules, error) {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := Rules{}
	for _, p := range keys {
		c := fields[p]
		if !c.Valid() {
			return Rules{}, fmt.Errorf("pattern %q: invalid class %d", p, int(c))
		}
		if !strings.HasPrefix(p, "/") || len(p) < 2 {
			return Rules{}, fmt.Errorf("pattern %q must start with / and name a field", p)
		}
		tokens := strings.Split(p[1:], "/")
		for _, t := range tokens {
			if t == "" {
				return Rules{}, fmt.Errorf("pattern %q has an empty token", p)
			}
		}
		out.rules = append(out.rules, rule{tokens: tokens, class: c})
	}
	return out, nil
}

// Empty reports whether no field is declared.
func (r Rules) Empty() bool { return len(r.rules) == 0 }

// ClassOf returns the class of the value at pointer (escaped tokens).
func (r Rules) ClassOf(pointer string) Class {
	var tokens []string
	if pointer != "" {
		tokens = strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	}
	best := Plain
	for _, ru := range r.rules {
		if len(ru.tokens) > len(tokens) || ru.class <= best {
			continue
		}
		match := true
		for i, t := range ru.tokens {
			if t != "*" && t != tokens[i] {
				match = false
				break
			}
		}
		if match {
			best = ru.class
		}
	}
	return best
}

// mayMatchBelow reports whether a non-plain rule could match a descendant of
// pointer, so plain containers are only walked when needed.
func (r Rules) mayMatchBelow(pointer string) bool {
	var tokens []string
	if pointer != "" {
		tokens = strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	}
	for _, ru := range r.rules {
		if ru.class == Plain || len(ru.tokens) <= len(tokens) {
			continue
		}
		match := true
		for i, t := range tokens {
			if ru.tokens[i] != "*" && ru.tokens[i] != t {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// Protector carries the operations Apply needs.
type Protector struct {
	// Fingerprint returns the keyed fingerprint of a secret's canonical
	// bytes, "sha256:<16 hex>".
	Fingerprint func(value []byte) (string, error)
	// Encrypt encrypts a personal value's JSON encoding, binding it to the
	// value's location (eventPointer) in the event, and returns the
	// "pii:v1:..." string.
	Encrypt func(plaintext []byte, eventPointer string) (string, error)
}

// SecretValue is how a secret appears in a record.
type SecretValue struct {
	Redacted    string `json:"redacted"`
	Fingerprint string `json:"fingerprint"`
}

// Apply returns v with its secret and personal parts protected. fieldPath
// is v's pointer in the classified document (matched against the rules),
// eventPointer its pointer in the event (bound into personal ciphertexts).
func (r Rules) Apply(v any, fieldPath, eventPointer string, p Protector) (any, error) {
	if v == nil {
		return nil, nil //nolint:nilnil // an absent value stays absent
	}
	switch r.ClassOf(fieldPath) {
	case Secret:
		return ProtectSecret(v, p)
	case Personal:
		return ProtectPersonal(v, eventPointer, p)
	case Plain:
	}
	if !r.mayMatchBelow(fieldPath) {
		return v, nil
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			tok := diff.Escape(k)
			pv, err := r.Apply(val, fieldPath+"/"+tok, eventPointer+"/"+tok, p)
			if err != nil {
				return nil, err
			}
			out[k] = pv
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			tok := strconv.Itoa(i)
			pv, err := r.Apply(val, fieldPath+"/"+tok, eventPointer+"/"+tok, p)
			if err != nil {
				return nil, err
			}
			out[i] = pv
		}
		return out, nil
	default:
		return v, nil
	}
}

// ProtectSecret replaces v by the redaction marker and its fingerprint. The
// fingerprinted bytes are the string itself for strings and the JSON
// encoding otherwise.
func ProtectSecret(v any, p Protector) (any, error) {
	if v == nil {
		return nil, nil //nolint:nilnil // an absent secret stays absent
	}
	var raw []byte
	if s, ok := v.(string); ok {
		raw = []byte(s)
	} else {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("classify: encode secret: %w", err)
		}
		raw = b
	}
	fp, err := p.Fingerprint(raw)
	clear(raw)
	if err != nil {
		return nil, err
	}
	return SecretValue{Redacted: Redacted, Fingerprint: fp}, nil
}

// ProtectPersonal encrypts v's JSON encoding.
func ProtectPersonal(v any, eventPointer string, p Protector) (any, error) {
	if v == nil {
		return nil, nil //nolint:nilnil // an absent value stays absent
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("classify: encode personal value: %w", err)
	}
	s, err := p.Encrypt(b, eventPointer)
	clear(b)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// FormatFingerprint formats an HMAC as "sha256:<first 16 hex digits>".
func FormatFingerprint(mac []byte) string {
	h := hex.EncodeToString(mac)
	if len(h) > 16 {
		h = h[:16]
	}
	return FingerprintPrefix + h
}

// FormatPII formats a personal ciphertext.
func FormatPII(keyID string, ciphertext []byte) string {
	return PIIPrefix + keyID + ":" + base64.RawURLEncoding.EncodeToString(ciphertext)
}

// ParsePII splits a "pii:v1:<key ID>:<ciphertext>" string.
func ParsePII(s string) (keyID string, ciphertext []byte, err error) {
	rest, ok := strings.CutPrefix(s, PIIPrefix)
	if !ok {
		return "", nil, fmt.Errorf("classify: not an encrypted personal value")
	}
	keyID, enc, ok := strings.Cut(rest, ":")
	if !ok || keyID == "" {
		return "", nil, fmt.Errorf("classify: malformed encrypted personal value")
	}
	ct, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "", nil, fmt.Errorf("classify: malformed encrypted personal value: %w", err)
	}
	return keyID, ct, nil
}

// PIIAAD is the associated data binding a personal ciphertext to its event
// and location.
func PIIAAD(eventID, eventPointer string) []byte {
	return []byte("portun.audit.pii.v1\x00" + eventID + "\x00" + eventPointer)
}
