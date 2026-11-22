// Package kmstest is the contract suite every kms.KMS implementation must
// pass. It tests behavior through the interface only.
//
// The suite assumes that a key reference ends with the key name, as every
// implementation in this repository does, to build a reference to a key that
// does not exist.
package kmstest

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	pcrypto "github.com/antonyrejith/portun/internal/platform/crypto"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/kms"
)

// Run runs the contract suite. newKMS returns the implementation under test;
// it is called once per subtest, and the instances may share a backend.
//
// Covers: SEC-08
func Run(t *testing.T, newKMS func(t *testing.T) kms.KMS) {
	t.Helper()
	cases := []struct {
		name string
		fn   func(*testing.T, kms.KMS)
	}{
		{"RoundTrip", testRoundTrip},
		{"Tamper", testTamper},
		{"RotateEncrypt", testRotateEncrypt},
		{"SignVerify", testSignVerify},
		{"WrongPurpose", testWrongPurpose},
		{"Destroy", testDestroy},
		{"UnknownRef", testUnknownRef},
		{"InvalidInput", testInvalidInput},
		{"Concurrent", testConcurrent},
		{"CanceledContext", testCanceledContext},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.fn(t, newKMS(t))
		})
	}
}

// UniqueName returns a random valid key name with the given prefix.
func UniqueName(t *testing.T, prefix string) string {
	t.Helper()
	b, err := pcrypto.RandomBytes(8)
	if err != nil {
		t.Fatal(err)
	}
	return prefix + "_" + hex.EncodeToString(b)
}

func create(t *testing.T, k kms.KMS, purpose kms.Purpose) (kms.KeyRef, string) {
	t.Helper()
	name := UniqueName(t, "kt")
	ref, err := k.CreateKey(t.Context(), name, purpose)
	if err != nil {
		t.Fatalf("CreateKey(%s, %s): %v", name, purpose, err)
	}
	if ref == "" || ref.Provider() == "" {
		t.Fatalf("CreateKey returned reference %q without a provider prefix", ref)
	}
	return ref, name
}

func wantCode(t *testing.T, op string, err error, code errs.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s succeeded, want %s", op, code)
	}
	if got := errs.CodeOf(err); got != code {
		t.Fatalf("%s: code %s, want %s (%v)", op, got, code, err)
	}
}

func testRoundTrip(t *testing.T, k kms.KMS) {
	t.Helper()
	ref, _ := create(t, k, kms.PurposeEncrypt)
	big := bytes.Repeat([]byte{0xA5}, kms.MaxDataSize)
	cases := []struct {
		name           string
		plaintext, aad []byte
	}{
		{"empty", []byte{}, nil},
		{"short", []byte("x"), []byte("tenant:ten_1")},
		{"binary", []byte{0, 1, 2, 255, 0}, []byte{0}},
		{"max size", big, big},
	}
	for _, c := range cases {
		ct, err := k.Encrypt(t.Context(), ref, c.plaintext, c.aad)
		if err != nil {
			t.Fatalf("%s: Encrypt: %v", c.name, err)
		}
		if len(c.plaintext) > 4 && bytes.Contains(ct, c.plaintext) {
			t.Fatalf("%s: ciphertext contains the plaintext", c.name)
		}
		again, err := k.Encrypt(t.Context(), ref, c.plaintext, c.aad)
		if err != nil || bytes.Equal(ct, again) {
			t.Fatalf("%s: encryption must be randomized (err %v)", c.name, err)
		}
		pt, err := k.Decrypt(t.Context(), ref, ct, c.aad)
		if err != nil {
			t.Fatalf("%s: Decrypt: %v", c.name, err)
		}
		if !bytes.Equal(pt, c.plaintext) {
			t.Fatalf("%s: round trip mismatch", c.name)
		}
		if v, err := kms.CiphertextVersion(ct); err != nil || v != 1 {
			t.Fatalf("%s: CiphertextVersion = %d, %v; want 1", c.name, v, err)
		}
	}
}

func testTamper(t *testing.T, k kms.KMS) {
	t.Helper()
	ref, _ := create(t, k, kms.PurposeEncrypt)
	other, _ := create(t, k, kms.PurposeEncrypt)
	aad := []byte("owner:usr_1")
	ct, err := k.Encrypt(t.Context(), ref, []byte("secret value"), aad)
	if err != nil {
		t.Fatal(err)
	}
	flip := func(i int) []byte {
		c := bytes.Clone(ct)
		c[i] ^= 0x01
		return c
	}
	cases := []struct {
		name string
		ref  kms.KeyRef
		ct   []byte
		aad  []byte
	}{
		{"wrong aad", ref, ct, []byte("owner:usr_2")},
		{"missing aad", ref, ct, nil},
		{"extra aad", ref, ct, append(bytes.Clone(aad), 'x')},
		{"other key", other, ct, aad},
		{"last byte", ref, flip(len(ct) - 1), aad},
		{"payload byte", ref, flip(len(ct) / 2), aad},
		{"format byte", ref, flip(0), aad},
		{"unknown version", ref, kms.EncodeCiphertext(99, ct[5:]), aad},
		{"truncated", ref, ct[:len(ct)-1], aad},
		{"header only", ref, ct[:5], aad},
		{"empty", ref, nil, aad},
	}
	for _, c := range cases {
		pt, err := k.Decrypt(t.Context(), c.ref, c.ct, c.aad)
		if pt != nil {
			t.Fatalf("%s: Decrypt returned plaintext", c.name)
		}
		wantCode(t, c.name, err, errs.CodeInvalidRequest)
	}
}

func testRotateEncrypt(t *testing.T, k kms.KMS) {
	t.Helper()
	ref, _ := create(t, k, kms.PurposeEncrypt)
	ctx := t.Context()
	ct1, err := k.Encrypt(ctx, ref, []byte("v1 data"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for want := 2; want <= 3; want++ {
		if err := k.RotateKey(ctx, ref); err != nil {
			t.Fatalf("RotateKey: %v", err)
		}
		ct, err := k.Encrypt(ctx, ref, []byte("new data"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if v, err := kms.CiphertextVersion(ct); err != nil || v != want {
			t.Fatalf("after rotation ciphertext version = %d, %v; want %d", v, err, want)
		}
		if pt, err := k.Decrypt(ctx, ref, ct, nil); err != nil || string(pt) != "new data" {
			t.Fatalf("Decrypt new: %q, %v", pt, err)
		}
	}
	if v, _ := kms.CiphertextVersion(ct1); v != 1 {
		t.Fatalf("old ciphertext version = %d, want 1", v)
	}
	if pt, err := k.Decrypt(ctx, ref, ct1, nil); err != nil || string(pt) != "v1 data" {
		t.Fatalf("old ciphertext after rotation: %q, %v", pt, err)
	}
}

func testSignVerify(t *testing.T, k kms.KMS) {
	t.Helper()
	ref, _ := create(t, k, kms.PurposeSign)
	ctx := t.Context()
	msg := []byte("merkle root")
	sig1, err := k.Sign(ctx, ref, msg)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	pub1, err := k.PublicKey(ctx, ref)
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}
	ec1, ok := pub1.(*ecdsa.PublicKey)
	if !ok || ec1.Curve != elliptic.P256() {
		t.Fatalf("PublicKey is %T, want P-256 *ecdsa.PublicKey", pub1)
	}
	if sig1.KeyVersion != 1 {
		t.Fatalf("KeyVersion = %d, want 1", sig1.KeyVersion)
	}
	if err := kms.Verify(pub1, msg, sig1.DER); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ecdsa.VerifyASN1(ec1, kms.Digest(msg), sig1.DER) {
		t.Fatal("ecdsa.VerifyASN1 over SHA-256(message) failed")
	}
	if err := kms.Verify(pub1, []byte("other"), sig1.DER); !errors.Is(err, kms.ErrInvalidSignature) {
		t.Fatalf("Verify(other message) = %v, want ErrInvalidSignature", err)
	}

	if err := k.RotateKey(ctx, ref); err != nil {
		t.Fatal(err)
	}
	sig2, err := k.Sign(ctx, ref, msg)
	if err != nil || sig2.KeyVersion != 2 {
		t.Fatalf("Sign after rotation: version %d, %v", sig2.KeyVersion, err)
	}
	keys, err := k.PublicKeys(ctx, ref)
	if err != nil || len(keys) != 2 {
		t.Fatalf("PublicKeys: %d keys, %v", len(keys), err)
	}
	latest, err := k.PublicKey(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	ecLatest, ok := latest.(*ecdsa.PublicKey)
	if !ok || !ecLatest.Equal(keys[2]) || !ec1.Equal(keys[1]) {
		t.Fatal("PublicKey must be the latest version and PublicKeys must keep version 1")
	}
	if err := kms.Verify(keys[sig1.KeyVersion], msg, sig1.DER); err != nil {
		t.Fatalf("old signature with version 1 key: %v", err)
	}
	if err := kms.Verify(keys[sig2.KeyVersion], msg, sig2.DER); err != nil {
		t.Fatalf("new signature with version 2 key: %v", err)
	}
	if err := kms.Verify(keys[2], msg, sig1.DER); err == nil {
		t.Fatal("version 1 signature must not verify with the version 2 key")
	}
}

func testWrongPurpose(t *testing.T, k kms.KMS) {
	t.Helper()
	ctx := t.Context()
	enc, _ := create(t, k, kms.PurposeEncrypt)
	sig, _ := create(t, k, kms.PurposeSign)
	ct, err := k.Encrypt(ctx, enc, []byte("x"), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = k.Sign(ctx, enc, []byte("m"))
	wantCode(t, "Sign with encrypt key", err, errs.CodeInvalidRequest)
	_, err = k.PublicKey(ctx, enc)
	wantCode(t, "PublicKey of encrypt key", err, errs.CodeInvalidRequest)
	_, err = k.PublicKeys(ctx, enc)
	wantCode(t, "PublicKeys of encrypt key", err, errs.CodeInvalidRequest)
	_, err = k.Encrypt(ctx, sig, []byte("x"), nil)
	wantCode(t, "Encrypt with sign key", err, errs.CodeInvalidRequest)
	_, err = k.Decrypt(ctx, sig, ct, nil)
	wantCode(t, "Decrypt with sign key", err, errs.CodeInvalidRequest)
}

// allOps runs every key operation on ref and returns their errors by name.
func allOps(ctx context.Context, k kms.KMS, ref kms.KeyRef, ct []byte) map[string]error {
	out := map[string]error{}
	_, out["Encrypt"] = k.Encrypt(ctx, ref, []byte("x"), nil)
	_, out["Decrypt"] = k.Decrypt(ctx, ref, ct, nil)
	_, out["Sign"] = k.Sign(ctx, ref, []byte("m"))
	_, out["PublicKey"] = k.PublicKey(ctx, ref)
	_, out["PublicKeys"] = k.PublicKeys(ctx, ref)
	out["RotateKey"] = k.RotateKey(ctx, ref)
	return out
}

func testDestroy(t *testing.T, k kms.KMS) {
	t.Helper()
	ctx := t.Context()
	keep, _ := create(t, k, kms.PurposeEncrypt)
	for _, purpose := range []kms.Purpose{kms.PurposeEncrypt, kms.PurposeSign} {
		ref, name := create(t, k, purpose)
		ct, err := k.Encrypt(ctx, keep, []byte("x"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if purpose == kms.PurposeEncrypt {
			if ct, err = k.Encrypt(ctx, ref, []byte("shred me"), nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := k.DestroyKey(ctx, ref); err != nil {
			t.Fatalf("DestroyKey: %v", err)
		}
		for op, err := range allOps(ctx, k, ref, ct) {
			wantCode(t, purpose.String()+" key: "+op+" after destroy", err, errs.CodeNotFound)
			if !errors.Is(err, kms.ErrKeyDestroyed) {
				t.Fatalf("%s after destroy: %v, want ErrKeyDestroyed", op, err)
			}
		}
		if err := k.DestroyKey(ctx, ref); err != nil {
			t.Fatalf("second DestroyKey must succeed: %v", err)
		}
		_, err = k.CreateKey(ctx, name, purpose)
		wantCode(t, "CreateKey with a destroyed key's name", err, errs.CodeConflict)
	}
	if _, err := k.Encrypt(ctx, keep, []byte("still works"), nil); err != nil {
		t.Fatalf("other keys must be unaffected: %v", err)
	}
}

func testUnknownRef(t *testing.T, k kms.KMS) {
	t.Helper()
	ctx := t.Context()
	ref, _ := create(t, k, kms.PurposeEncrypt)
	ct, err := k.Encrypt(ctx, ref, []byte("x"), nil)
	if err != nil {
		t.Fatal(err)
	}
	unknown := kms.KeyRef(string(ref) + "_missing")
	ops := allOps(ctx, k, unknown, ct)
	ops["DestroyKey"] = k.DestroyKey(ctx, unknown)
	for op, err := range ops {
		wantCode(t, op+" on unknown key", err, errs.CodeNotFound)
		if !errors.Is(err, kms.ErrKeyNotFound) || errors.Is(err, kms.ErrKeyDestroyed) {
			t.Fatalf("%s on unknown key: %v, want ErrKeyNotFound only", op, err)
		}
	}
}

func testInvalidInput(t *testing.T, k kms.KMS) {
	t.Helper()
	ctx := t.Context()
	for _, name := range []string{"", "-lead", "trail-", "has.dot", "../up", "a/b", "sp ace", "ü", strings.Repeat("a", kms.MaxNameLength+1)} {
		_, err := k.CreateKey(ctx, name, kms.PurposeEncrypt)
		wantCode(t, "CreateKey("+name+")", err, errs.CodeInvalidRequest)
	}
	for _, p := range []kms.Purpose{0, 3, -1} {
		_, err := k.CreateKey(ctx, UniqueName(t, "kt"), p)
		wantCode(t, "CreateKey with invalid purpose", err, errs.CodeInvalidRequest)
	}
	ref, name := create(t, k, kms.PurposeEncrypt)
	_, err := k.CreateKey(ctx, name, kms.PurposeSign)
	wantCode(t, "CreateKey with an existing name", err, errs.CodeConflict)
	for _, bad := range []kms.KeyRef{"", "nope", "other:" + kms.KeyRef(name), ref + "/../x", ref + ".x"} {
		_, err := k.Encrypt(ctx, bad, []byte("x"), nil)
		wantCode(t, "Encrypt with reference "+string(bad), err, errs.CodeInvalidRequest)
		wantCode(t, "DestroyKey with reference "+string(bad), k.DestroyKey(ctx, bad), errs.CodeInvalidRequest)
	}
	tooBig := make([]byte, kms.MaxDataSize+1)
	_, err = k.Encrypt(ctx, ref, tooBig, nil)
	wantCode(t, "Encrypt oversized plaintext", err, errs.CodePayloadTooLarge)
	_, err = k.Encrypt(ctx, ref, nil, tooBig)
	wantCode(t, "Encrypt oversized aad", err, errs.CodePayloadTooLarge)
}

func testConcurrent(t *testing.T, k kms.KMS) {
	t.Helper()
	ctx := t.Context()
	enc, _ := create(t, k, kms.PurposeEncrypt)
	sig, _ := create(t, k, kms.PurposeSign)
	const workers, iterations, rotations = 8, 8, 3
	var wg sync.WaitGroup
	errc := make(chan error, workers*iterations+2*rotations)
	for w := range workers {
		wg.Go(func() {
			for i := range iterations {
				errc <- encryptSignOnce(ctx, k, enc, sig, fmt.Appendf(nil, "worker %d, iteration %d", w, i))
			}
		})
	}
	wg.Go(func() {
		for range rotations {
			errc <- k.RotateKey(ctx, enc)
			errc <- k.RotateKey(ctx, sig)
		}
	})
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	keys, err := k.PublicKeys(ctx, sig)
	if err != nil || len(keys) != rotations+1 {
		t.Fatalf("PublicKeys after concurrent rotations: %d, %v", len(keys), err)
	}
}

func encryptSignOnce(ctx context.Context, k kms.KMS, enc, sig kms.KeyRef, data []byte) error {
	ct, err := k.Encrypt(ctx, enc, data, data)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	pt, err := k.Decrypt(ctx, enc, ct, data)
	if err != nil {
		return fmt.Errorf("decrypt: %w", err)
	}
	if !bytes.Equal(pt, data) {
		return errors.New("concurrent round trip mismatch")
	}
	s, err := k.Sign(ctx, sig, data)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	keys, err := k.PublicKeys(ctx, sig)
	if err != nil {
		return fmt.Errorf("public keys: %w", err)
	}
	if err := kms.Verify(keys[s.KeyVersion], data, s.DER); err != nil {
		return fmt.Errorf("verify version %d: %w", s.KeyVersion, err)
	}
	return nil
}

func testCanceledContext(t *testing.T, k kms.KMS) {
	t.Helper()
	ref, name := create(t, k, kms.PurposeEncrypt)
	ct, err := k.Encrypt(t.Context(), ref, []byte("x"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ops := allOps(ctx, k, ref, ct)
	_, ops["CreateKey"] = k.CreateKey(ctx, name+"_2", kms.PurposeEncrypt)
	ops["DestroyKey"] = k.DestroyKey(ctx, ref)
	for op, err := range ops {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%s with a cancelled context: %v, want context.Canceled", op, err)
		}
	}
	if _, err := k.Decrypt(t.Context(), ref, ct, nil); err != nil {
		t.Fatalf("key must be intact after cancelled calls: %v", err)
	}
}
