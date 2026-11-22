package envelope_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/crypto"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/kms"
	"github.com/antonyrejith/portun/internal/platform/kms/envelope"
	"github.com/antonyrejith/portun/internal/platform/kms/local"
)

func setup(t *testing.T) (kms.KMS, *envelope.Envelope, kms.KeyRef) {
	t.Helper()
	k, err := local.NewMemory()
	if err != nil {
		t.Fatal(err)
	}
	kek, err := k.CreateKey(t.Context(), "tenant_kek", kms.PurposeEncrypt)
	if err != nil {
		t.Fatal(err)
	}
	return k, envelope.New(k), kek
}

const owner = "tenant:ten_01h455vb4pex5vsknk084sn02q/subject:usr_01h455vb4pex5vsknk084sn02q"

// TestRoundTrip encrypts data under a fresh DEK, persists only the wrapped
// DEK, unwraps it and decrypts.
//
// Covers: SEC-08
func TestRoundTrip(t *testing.T) {
	t.Parallel()
	_, env, kek := setup(t)
	dek, w, err := env.NewDEK(t.Context(), kek, owner)
	if err != nil {
		t.Fatal(err)
	}
	if w.KEK != kek || len(w.Ciphertext) == 0 {
		t.Fatalf("Wrapped = %+v", w)
	}
	aad := []byte("users.email/usr_1")
	ct, err := dek.Seal([]byte("alice@example.com"), aad)
	if err != nil {
		t.Fatal(err)
	}
	again, err := env.Unwrap(t.Context(), w, owner)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := again.Open(ct, aad)
	if err != nil || string(pt) != "alice@example.com" {
		t.Fatalf("Open: %q, %v", pt, err)
	}
	if _, err := again.Open(ct, []byte("users.email/usr_2")); !errors.Is(err, crypto.ErrInvalidCiphertext) {
		t.Fatalf("ciphertext moved to another record must fail: %v", err)
	}
}

// TestOwnerBinding checks a wrapped DEK only unwraps for its owner and fails
// when tampered with.
//
// Covers: SEC-08
func TestOwnerBinding(t *testing.T) {
	t.Parallel()
	_, env, kek := setup(t)
	_, w, err := env.NewDEK(t.Context(), kek, owner)
	if err != nil {
		t.Fatal(err)
	}
	tampered := w
	tampered.Ciphertext = bytes.Clone(w.Ciphertext)
	tampered.Ciphertext[len(tampered.Ciphertext)-1] ^= 1
	cases := []struct {
		name  string
		w     envelope.Wrapped
		owner string
		code  errs.Code
	}{
		{"other owner", w, "tenant:ten_other", errs.CodeInvalidRequest},
		{"owner prefix", w, owner[:10], errs.CodeInvalidRequest},
		{"tampered", tampered, owner, errs.CodeInvalidRequest},
		{"empty owner", w, "", errs.CodeInternal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := env.Unwrap(t.Context(), c.w, c.owner)
			if errs.CodeOf(err) != c.code {
				t.Fatalf("Unwrap: %v, want %s", err, c.code)
			}
		})
	}
	if _, _, err := env.NewDEK(t.Context(), kek, ""); errs.CodeOf(err) != errs.CodeInternal {
		t.Fatalf("NewDEK with empty owner: %v", err)
	}
}

// TestCryptoShredding checks that destroying the KEK makes data encrypted
// under its DEKs unrecoverable.
//
// Covers: SEC-08
func TestCryptoShredding(t *testing.T) {
	t.Parallel()
	k, env, kek := setup(t)
	_, w, err := env.NewDEK(t.Context(), kek, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.DestroyKey(t.Context(), kek); err != nil {
		t.Fatal(err)
	}
	_, err = env.Unwrap(t.Context(), w, owner)
	if !errors.Is(err, kms.ErrKeyDestroyed) || errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("Unwrap after KEK destruction: %v, want ErrKeyDestroyed", err)
	}
	if _, _, err := env.NewDEK(t.Context(), kek, owner); !errors.Is(err, kms.ErrKeyDestroyed) {
		t.Fatalf("NewDEK under a destroyed KEK: %v", err)
	}
}

// TestRewrapAfterRotation checks a DEK moves to the KEK's latest version and
// still decrypts existing data.
//
// Covers: SEC-08
func TestRewrapAfterRotation(t *testing.T) {
	t.Parallel()
	k, env, kek := setup(t)
	dek, w, err := env.NewDEK(t.Context(), kek, owner)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := dek.Seal([]byte("data"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.RotateKey(t.Context(), kek); err != nil {
		t.Fatal(err)
	}
	w2, err := env.Rewrap(t.Context(), w, owner)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := kms.CiphertextVersion(w2.Ciphertext); v != 2 {
		t.Fatalf("rewrapped version = %d, want 2", v)
	}
	dek2, err := env.Unwrap(t.Context(), w2, owner)
	if err != nil {
		t.Fatal(err)
	}
	if pt, err := dek2.Open(ct, nil); err != nil || string(pt) != "data" {
		t.Fatalf("Open with rewrapped DEK: %q, %v", pt, err)
	}
	if _, err := env.Rewrap(t.Context(), w, "tenant:other"); errs.CodeOf(err) != errs.CodeInvalidRequest {
		t.Fatalf("Rewrap for another owner: %v", err)
	}
}

// TestDEKRedacted checks no fmt verb prints a DEK.
//
// Covers: SEC-09
func TestDEKRedacted(t *testing.T) {
	t.Parallel()
	_, env, kek := setup(t)
	dek, _, err := env.NewDEK(t.Context(), kek, owner)
	if err != nil {
		t.Fatal(err)
	}
	holder := struct{ D envelope.DEK }{dek}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%d", "%x"} {
		if got := fmt.Sprintf(verb, dek); got != "envelope.DEK{[REDACTED]}" {
			t.Errorf("%s printed %q", verb, got)
		}
		if got := fmt.Sprintf(verb, holder); !bytes.Contains([]byte(got), []byte("REDACTED")) {
			t.Errorf("nested %s printed %q", verb, got)
		}
	}
}
