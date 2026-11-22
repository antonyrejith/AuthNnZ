package kms_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/crypto"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/kms"
)

func TestValidateName(t *testing.T) {
	t.Parallel()
	valid := []string{"a", "_", "tenant_kek", "ten_01h455vb4pex5vsknk084sn02q", "a-b", "A9", strings.Repeat("x", kms.MaxNameLength)}
	invalid := []string{"", "-a", "a-", "a.b", "a/b", "a b", "a\x00", "é", strings.Repeat("x", kms.MaxNameLength+1)}
	for _, n := range valid {
		if err := kms.ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q): %v", n, err)
		}
	}
	for _, n := range invalid {
		if err := kms.ValidateName(n); errs.CodeOf(err) != errs.CodeInvalidRequest {
			t.Errorf("ValidateName(%q) = %v, want invalid request", n, err)
		}
	}
}

func TestPurpose(t *testing.T) {
	t.Parallel()
	for _, p := range []kms.Purpose{kms.PurposeEncrypt, kms.PurposeSign} {
		got, err := kms.ParsePurpose(p.String())
		if err != nil || got != p || !p.Valid() || kms.CheckPurpose(p) != nil {
			t.Errorf("purpose %v does not round-trip", p)
		}
	}
	for _, p := range []kms.Purpose{0, 3, -1} {
		if p.Valid() || p.String() != "invalid" || errs.CodeOf(kms.CheckPurpose(p)) != errs.CodeInvalidRequest {
			t.Errorf("purpose %d must be invalid", p)
		}
	}
	if _, err := kms.ParsePurpose("decrypt"); err == nil {
		t.Error("ParsePurpose accepted an unknown purpose")
	}
}

func TestKeyRefProvider(t *testing.T) {
	t.Parallel()
	cases := map[kms.KeyRef]string{"local:a": "local", "openbao:transit/a": "openbao", "nope": "", "": ""}
	for ref, want := range cases {
		if got := ref.Provider(); got != want {
			t.Errorf("%q.Provider() = %q, want %q", ref, got, want)
		}
	}
}

func TestCiphertextFormat(t *testing.T) {
	t.Parallel()
	payload := []byte("payload")
	for _, v := range []int{1, 2, 1 << 20, 1<<31 - 1} {
		ct := kms.EncodeCiphertext(v, payload)
		gotV, gotP, err := kms.DecodeCiphertext(ct)
		if err != nil || gotV != v || !bytes.Equal(gotP, payload) {
			t.Fatalf("round trip v%d: %d %q %v", v, gotV, gotP, err)
		}
		if cv, err := kms.CiphertextVersion(ct); err != nil || cv != v {
			t.Fatalf("CiphertextVersion: %d, %v", cv, err)
		}
	}
	bad := map[string][]byte{
		"empty":        nil,
		"header only":  kms.EncodeCiphertext(1, nil),
		"format 2":     append([]byte{2, 0, 0, 0, 1}, payload...),
		"version 0":    append([]byte{1, 0, 0, 0, 0}, payload...),
		"version 2^31": append([]byte{1, 0x80, 0, 0, 0}, payload...),
	}
	for name, ct := range bad {
		_, _, err := kms.DecodeCiphertext(ct)
		if errs.CodeOf(err) != errs.CodeInvalidRequest || !errors.Is(err, crypto.ErrInvalidCiphertext) {
			t.Errorf("%s: %v, want invalid ciphertext", name, err)
		}
	}
	if kms.CheckVersion(0) == nil || kms.CheckVersion(1) != nil {
		t.Error("CheckVersion")
	}
}

func TestDataSize(t *testing.T) {
	t.Parallel()
	big := make([]byte, kms.MaxDataSize+1)
	if kms.CheckDataSize(big[:kms.MaxDataSize], big[:kms.MaxDataSize]) != nil {
		t.Error("maximum size rejected")
	}
	if errs.CodeOf(kms.CheckDataSize(big, nil)) != errs.CodePayloadTooLarge || errs.CodeOf(kms.CheckDataSize(nil, big)) != errs.CodePayloadTooLarge {
		t.Error("oversized data accepted")
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()
	ref := kms.KeyRef("local:x")
	if err := kms.DestroyedError(ref); !errors.Is(err, kms.ErrKeyDestroyed) || errs.CodeOf(err) != errs.CodeNotFound {
		t.Errorf("DestroyedError = %v", err)
	}
	if err := kms.NotFoundError(ref); !errors.Is(err, kms.ErrKeyNotFound) || errors.Is(err, kms.ErrKeyDestroyed) || errs.CodeOf(err) != errs.CodeNotFound {
		t.Errorf("NotFoundError = %v", err)
	}
	if err := kms.PurposeError(ref, kms.PurposeEncrypt, kms.PurposeSign); errs.CodeOf(err) != errs.CodeInvalidRequest {
		t.Errorf("PurposeError = %v", err)
	}
	if err := kms.InvalidRefError(ref, "x"); errs.CodeOf(err) != errs.CodeInvalidRequest {
		t.Errorf("InvalidRefError = %v", err)
	}
	if code, m, _ := errs.Public(kms.DestroyedError(ref)); code != errs.CodeNotFound || strings.Contains(m, "local:x") {
		t.Errorf("client message %q must not reveal the key reference", m)
	}
}

// TestVerify checks signature verification rejects other keys, curves and
// tampered signatures.
//
// Covers: SEC-08
func TestVerify(t *testing.T) {
	t.Parallel()
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	edPub, _, _ := ed25519.GenerateKey(rand.Reader)
	msg := []byte("message")
	der, err := ecdsa.SignASN1(rand.Reader, priv, kms.Digest(msg))
	if err != nil {
		t.Fatal(err)
	}
	if err := kms.Verify(&priv.PublicKey, msg, der); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	bad := bytes.Clone(der)
	bad[len(bad)-1] ^= 1
	cases := map[string]error{
		"other message": kms.Verify(&priv.PublicKey, []byte("other"), der),
		"other key":     kms.Verify(&other.PublicKey, msg, der),
		"tampered":      kms.Verify(&priv.PublicKey, msg, bad),
		"P-384":         kms.Verify(&p384.PublicKey, msg, der),
		"Ed25519":       kms.Verify(edPub, msg, der),
		"nil":           kms.Verify(nil, msg, der),
		"empty sig":     kms.Verify(&priv.PublicKey, msg, nil),
	}
	for name, err := range cases {
		if !errors.Is(err, kms.ErrInvalidSignature) || errs.CodeOf(err) != errs.CodeInvalidRequest {
			t.Errorf("%s: %v, want ErrInvalidSignature", name, err)
		}
	}
}
