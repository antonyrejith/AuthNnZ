package crypto_test

import (
	"bytes"
	stdcrypto "crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math/big"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/crypto"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

func key(tb testing.TB) []byte {
	tb.Helper()
	k, err := crypto.RandomBytes(crypto.KeySize)
	if err != nil {
		tb.Fatal(err)
	}
	return k
}

func TestRandomBytes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		n  int
		ok bool
	}{{1, true}, {32, true}, {1 << 20, true}, {0, false}, {-1, false}, {1<<20 + 1, false}}
	for _, c := range cases {
		b, err := crypto.RandomBytes(c.n)
		if c.ok != (err == nil) {
			t.Fatalf("RandomBytes(%d): %v", c.n, err)
		}
		if c.ok && len(b) != c.n {
			t.Fatalf("RandomBytes(%d) returned %d bytes", c.n, len(b))
		}
		if !c.ok && (errs.CodeOf(err) != errs.CodeInternal || !errors.Is(err, crypto.ErrInvalidArgument)) {
			t.Fatalf("RandomBytes(%d): %v", c.n, err)
		}
	}
	a, _ := crypto.RandomBytes(32)
	b, _ := crypto.RandomBytes(32)
	if bytes.Equal(a, b) {
		t.Fatal("two random values are equal")
	}
}

// TestRandomToken checks tokens are base64url without padding and at least
// 128 bits.
//
// Covers: SEC-09
func TestRandomToken(t *testing.T) {
	t.Parallel()
	cases := []struct {
		n      int
		ok     bool
		length int
	}{{16, true, 22}, {32, true, 43}, {15, false, 0}, {0, false, 0}}
	for _, c := range cases {
		tok, err := crypto.RandomToken(c.n)
		if c.ok != (err == nil) {
			t.Fatalf("RandomToken(%d): %v", c.n, err)
		}
		if !c.ok {
			continue
		}
		if len(tok) != c.length {
			t.Fatalf("RandomToken(%d) length %d, want %d", c.n, len(tok), c.length)
		}
		raw, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil || len(raw) != c.n {
			t.Fatalf("token %q is not unpadded base64url of %d bytes: %v", tok, c.n, err)
		}
	}
}

// TestSealOpen covers the AEAD round trip and every failure mode.
//
// Covers: SEC-08
func TestSealOpen(t *testing.T) {
	t.Parallel()
	k := key(t)
	aad := []byte("tenant:ten_1/users/usr_1/email")
	pt := []byte("alice@example.com")
	ct, err := crypto.Seal(k, pt, aad)
	if err != nil {
		t.Fatal(err)
	}
	if len(ct) != len(pt)+crypto.Overhead || ct[0] != 0x01 {
		t.Fatalf("ciphertext length %d (version %d), want %d", len(ct), ct[0], len(pt)+crypto.Overhead)
	}
	if got, err := crypto.Open(k, ct, aad); err != nil || !bytes.Equal(got, pt) {
		t.Fatalf("Open: %q, %v", got, err)
	}
	ct2, _ := crypto.Seal(k, pt, aad)
	if bytes.Equal(ct, ct2) || bytes.Equal(ct[1:13], ct2[1:13]) {
		t.Fatal("nonces must be random")
	}
	empty, err := crypto.Seal(k, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := crypto.Open(k, empty, nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty round trip: %v, %v", got, err)
	}

	flip := func(i int) []byte {
		c := bytes.Clone(ct)
		c[i] ^= 0x80
		return c
	}
	withVersion := func(v byte) []byte {
		c := bytes.Clone(ct)
		c[0] = v
		return c
	}
	cases := []struct {
		name string
		key  []byte
		ct   []byte
		aad  []byte
	}{
		{"version byte", k, flip(0), aad},
		{"unknown version 2", k, withVersion(2), aad},
		{"version 0", k, withVersion(0), aad},
		{"nonce first byte", k, flip(1), aad},
		{"nonce last byte", k, flip(12), aad},
		{"ciphertext body", k, flip(13), aad},
		{"tag first byte", k, flip(len(ct) - 16), aad},
		{"tag last byte", k, flip(len(ct) - 1), aad},
		{"wrong aad", k, ct, []byte("tenant:ten_2/users/usr_1/email")},
		{"missing aad", k, ct, nil},
		{"wrong key", key(t), ct, aad},
		{"truncated by one", k, ct[:len(ct)-1], aad},
		{"shorter than overhead", k, ct[:crypto.Overhead-1], aad},
		{"version only", k, ct[:1], aad},
		{"empty", k, nil, aad},
		{"appended byte", k, append(bytes.Clone(ct), 0), aad},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := crypto.Open(c.key, c.ct, c.aad)
			if got != nil {
				t.Fatal("Open returned plaintext")
			}
			if errs.CodeOf(err) != errs.CodeInvalidRequest || !errors.Is(err, crypto.ErrInvalidCiphertext) {
				t.Fatalf("Open: %v, want invalid ciphertext", err)
			}
		})
	}
}

func TestKeyLength(t *testing.T) {
	t.Parallel()
	for _, n := range []int{0, 16, 24, 31, 33, 64} {
		k := make([]byte, n)
		_, err := crypto.Seal(k, []byte("x"), nil)
		if errs.CodeOf(err) != errs.CodeInternal || !errors.Is(err, crypto.ErrInvalidKey) {
			t.Fatalf("Seal with %d-byte key: %v", n, err)
		}
		_, err = crypto.Open(k, make([]byte, 64), nil)
		if !errors.Is(err, crypto.ErrInvalidKey) {
			t.Fatalf("Open with %d-byte key: %v", n, err)
		}
	}
}

// FuzzOpen checks Open never panics and never accepts forged input.
//
// Covers: SEC-08
func FuzzOpen(f *testing.F) {
	k := key(f)
	valid, err := crypto.Seal(k, []byte("seed"), []byte("aad"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid, []byte("aad"))
	f.Add([]byte{}, []byte{})
	f.Add([]byte{1}, []byte(nil))
	f.Add(bytes.Repeat([]byte{1}, crypto.Overhead), []byte("x"))
	f.Add([]byte{2, 0, 0}, []byte("aad"))
	f.Fuzz(func(t *testing.T, ct, aad []byte) {
		pt, err := crypto.Open(k, ct, aad)
		if err == nil && !bytes.Equal(ct, valid) {
			t.Fatalf("forged ciphertext accepted: %x", ct)
		}
		if err != nil && pt != nil {
			t.Fatal("plaintext returned with an error")
		}
	})
}

func TestEqual(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b []byte
		want bool
	}{
		{[]byte("abc"), []byte("abc"), true},
		{[]byte("abc"), []byte("abd"), false},
		{[]byte("abc"), []byte("ab"), false},
		{nil, []byte{}, true},
		{nil, []byte{0}, false},
	}
	for _, c := range cases {
		if got := crypto.Equal(c.a, c.b); got != c.want {
			t.Errorf("Equal(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestHMAC(t *testing.T) {
	t.Parallel()
	k := key(t)
	mac, err := crypto.HMACSHA256(k, []byte("msg"))
	if err != nil || len(mac) != 32 {
		t.Fatalf("HMACSHA256: %d bytes, %v", len(mac), err)
	}
	if !crypto.VerifyHMACSHA256(k, []byte("msg"), mac) {
		t.Fatal("valid MAC rejected")
	}
	bad := bytes.Clone(mac)
	bad[0] ^= 1
	for name, ok := range map[string]bool{
		"tampered mac":     crypto.VerifyHMACSHA256(k, []byte("msg"), bad),
		"other message":    crypto.VerifyHMACSHA256(k, []byte("msg2"), mac),
		"other key":        crypto.VerifyHMACSHA256(key(t), []byte("msg"), mac),
		"truncated":        crypto.VerifyHMACSHA256(k, []byte("msg"), mac[:16]),
		"short key verify": crypto.VerifyHMACSHA256(k[:16], []byte("msg"), mac),
	} {
		if ok {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := crypto.HMACSHA256(k[:31], []byte("msg")); !errors.Is(err, crypto.ErrInvalidKey) {
		t.Fatalf("short HMAC key: %v", err)
	}
	// RFC 4231 test case 6 (131-byte key).
	vector, err := crypto.HMACSHA256(bytes.Repeat([]byte{0xaa}, 131), []byte("Test Using Larger Than Block-Size Key - Hash Key First"))
	if err != nil || hex.EncodeToString(vector) != "60e431591ee0b67f0d8a26aacbf5b77f8e0bc6213728c5140546040f0ee37f54" {
		t.Fatalf("RFC 4231 vector: %x, %v", vector, err)
	}
}

func TestDeriveKey(t *testing.T) {
	t.Parallel()
	ikm := key(t)
	a, err := crypto.DeriveKey(ikm, []byte("salt"), "portun test a", 32)
	if err != nil || len(a) != 32 {
		t.Fatalf("DeriveKey: %v", err)
	}
	b, _ := crypto.DeriveKey(ikm, []byte("salt"), "portun test b", 32)
	a2, _ := crypto.DeriveKey(ikm, []byte("salt"), "portun test a", 32)
	if bytes.Equal(a, b) || !bytes.Equal(a, a2) {
		t.Fatal("derivation must be deterministic and separated by info")
	}
	bad := []struct {
		name   string
		ikm    []byte
		info   string
		length int
	}{
		{"empty secret", nil, "x", 32},
		{"empty info", ikm, "", 32},
		{"zero length", ikm, "x", 0},
		{"too long", ikm, "x", 255*32 + 1},
	}
	for _, c := range bad {
		if _, err := crypto.DeriveKey(c.ikm, nil, c.info, c.length); errs.CodeOf(err) != errs.CodeInternal {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// TestDeriveKeyVector checks HKDF-SHA256 against RFC 5869 test case 1.
func TestDeriveKeyVector(t *testing.T) {
	t.Parallel()
	ikm := bytes.Repeat([]byte{0x0b}, 22)
	salt := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	info := string([]byte{0xf0, 0xf1, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7, 0xf8, 0xf9})
	want := []byte{
		0x3c, 0xb2, 0x5f, 0x25, 0xfa, 0xac, 0xd5, 0x7a, 0x90, 0x43, 0x4f, 0x64, 0xd0, 0x36, 0x2f, 0x2a,
		0x2d, 0x2d, 0x0a, 0x90, 0xcf, 0x1a, 0x5a, 0x4c, 0x5d, 0xb0, 0x2d, 0x56, 0xec, 0xc4, 0xc5, 0xbf,
		0x34, 0x00, 0x72, 0x08, 0xd5, 0xb8, 0x87, 0x18, 0x58, 0x65,
	}
	got, err := crypto.DeriveKey(ikm, salt, info, 42)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("DeriveKey = %x, %v", got, err)
	}
}

// TestSigningAlgorithms checks the allow-list and rejects none, HMAC and
// unknown or miscased names.
//
// Covers: SEC-09
func TestSigningAlgorithms(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"ES256": true, "RS256": true, "PS256": true, "EdDSA": true,
		"none": false, "None": false, "NONE": false, "": false,
		"HS256": false, "HS384": false, "HS512": false,
		"es256": false, "ES384": false, "RS512": false, "RS1": false, "ES256K": false, "eddsa": false,
	}
	for alg, want := range cases {
		if got := crypto.IsApprovedSigningAlg(alg); got != want {
			t.Errorf("IsApprovedSigningAlg(%q) = %v, want %v", alg, got, want)
		}
	}
	approved := crypto.ApprovedSigningAlgs()
	if approved[0] != crypto.DefaultSigningAlg || crypto.DefaultSigningAlg != crypto.AlgES256 || len(approved) != 4 {
		t.Fatalf("ApprovedSigningAlgs = %v", approved)
	}
	for _, alg := range approved {
		if !crypto.IsApprovedSigningAlg(alg) {
			t.Errorf("%s listed but not approved", alg)
		}
	}
	hashes := map[stdcrypto.Hash]bool{
		stdcrypto.SHA256: true, stdcrypto.SHA384: true, stdcrypto.SHA512: true,
		stdcrypto.SHA1: false, stdcrypto.MD5: false, stdcrypto.SHA224: false, stdcrypto.SHA3_256: false,
	}
	for h, want := range hashes {
		if crypto.IsApprovedHash(h) != want {
			t.Errorf("IsApprovedHash(%v) != %v", h, want)
		}
	}
}

// TestCheckSigningKey checks key kinds and sizes per algorithm, including the
// RSA minimum.
//
// Covers: SEC-09
func TestCheckSigningKey(t *testing.T) {
	t.Parallel()
	p256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	rsa2048, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	edPub, edPriv, _ := ed25519.GenerateKey(rand.Reader)
	// A 1024-bit modulus built without generating a weak key (the standard
	// library refuses to generate one).
	weak := &rsa.PublicKey{N: new(big.Int).Rsh(rsa2048.N, 1024), E: 65537}

	cases := []struct {
		name string
		alg  string
		key  any
		ok   bool
	}{
		{"ES256 P-256 public", crypto.AlgES256, &p256.PublicKey, true},
		{"ES256 P-256 private", crypto.AlgES256, p256, true},
		{"ES256 P-384", crypto.AlgES256, &p384.PublicKey, false},
		{"ES256 RSA", crypto.AlgES256, &rsa2048.PublicKey, false},
		{"RS256 2048", crypto.AlgRS256, &rsa2048.PublicKey, true},
		{"PS256 2048 private", crypto.AlgPS256, rsa2048, true},
		{"RS256 1024", crypto.AlgRS256, weak, false},
		{"PS256 EC", crypto.AlgPS256, p256, false},
		{"EdDSA", crypto.AlgEdDSA, edPub, true},
		{"EdDSA private", crypto.AlgEdDSA, edPriv, true},
		{"EdDSA EC", crypto.AlgEdDSA, p256, false},
		{"none", "none", p256, false},
		{"HS256 with symmetric key", "HS256", []byte("secret"), false},
		{"HS256 with public key", "HS256", &rsa2048.PublicKey, false},
		{"nil key", crypto.AlgES256, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := crypto.CheckSigningKey(c.alg, c.key)
			if c.ok != (err == nil) {
				t.Fatalf("CheckSigningKey: %v, want ok=%v", err, c.ok)
			}
			if !c.ok && (!errors.Is(err, crypto.ErrNotApproved) || errs.CodeOf(err) != errs.CodeInvalidRequest) {
				t.Fatalf("CheckSigningKey: %v, want ErrNotApproved", err)
			}
		})
	}
}
