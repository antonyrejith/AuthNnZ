package local

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/crypto"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/kms"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

func encodings(b []byte) map[string][]byte {
	return map[string][]byte{
		"raw":       b,
		"base64":    []byte(base64.StdEncoding.EncodeToString(b)),
		"base64url": []byte(base64.RawURLEncoding.EncodeToString(b)),
		"hex":       []byte(hex.EncodeToString(b)),
	}
}

// TestMaterialEncryptedAtRest checks that neither the master key nor any key
// material appears in the key file in any common encoding, and that destroyed
// material leaves the file.
//
// Covers: SEC-08, SEC-09
func TestMaterialEncryptedAtRest(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	master, err := crypto.RandomBytes(crypto.KeySize)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "keys.json")
	k, err := Open(path, secret.NewBytes(master))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := k.CreateKey(ctx, "enc", kms.PurposeEncrypt)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.RotateKey(ctx, enc); err != nil {
		t.Fatal(err)
	}
	sig, err := k.CreateKey(ctx, "sig", kms.PurposeSign)
	if err != nil {
		t.Fatal(err)
	}
	var materials [][]byte
	var sigSealed [][]byte
	for name, kk := range k.keys {
		for v := 1; v <= len(kk.versions); v++ {
			m, err := k.unseal(name, kk, v)
			if err != nil {
				t.Fatal(err)
			}
			materials = append(materials, m)
		}
		if name == "sig" {
			sigSealed = append(sigSealed, bytes.Clone(kk.versions[0]))
		}
	}
	if len(materials) != 3 {
		t.Fatalf("want 3 versions of material, got %d", len(materials))
	}
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secretBytes := range append(materials, master) {
		for enc, b := range encodings(secretBytes) {
			if bytes.Contains(file, b) {
				t.Fatalf("key file contains secret material (%s)", enc)
			}
		}
	}

	if err := k.DestroyKey(ctx, sig); err != nil {
		t.Fatal(err)
	}
	file, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(file, []byte(base64.StdEncoding.EncodeToString(sigSealed[0]))) {
		t.Fatal("destroyed key's sealed material is still in the key file")
	}
	var st fileState
	if err := json.Unmarshal(file, &st); err != nil {
		t.Fatal(err)
	}
	if fk := st.Keys["sig"]; !fk.Destroyed || len(fk.Versions) != 0 {
		t.Fatalf("destroyed key record = %+v, want tombstone without material", fk)
	}
}

// TestSwappedMaterialDetected checks material moved between keys or versions
// in the file fails its integrity check.
//
// Covers: SEC-08
func TestSwappedMaterialDetected(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	master, err := crypto.RandomBytes(crypto.KeySize)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "keys.json")
	k, err := Open(path, secret.NewBytes(master))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := k.CreateKey(ctx, "a", kms.PurposeEncrypt)
	if _, err := k.CreateKey(ctx, "b", kms.PurposeEncrypt); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st fileState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	fa := st.Keys["a"]
	fa.Versions = st.Keys["b"].Versions
	st.Keys["a"] = fa
	data, err = json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	k2, err := Open(path, secret.NewBytes(master))
	if err != nil {
		t.Fatal(err)
	}
	_, err = k2.Encrypt(ctx, a, []byte("x"), nil)
	if errs.CodeOf(err) != errs.CodeInternal || !errors.Is(err, crypto.ErrInvalidCiphertext) {
		t.Fatalf("Encrypt with swapped material: %v, want integrity failure", err)
	}
}

// TestPersistFailureRollsBack checks a failed write leaves the in-memory
// state unchanged.
func TestPersistFailureRollsBack(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dir := t.TempDir()
	k, err := Open(filepath.Join(dir, "keys.json"), secret.NewBytes(make([]byte, crypto.KeySize)))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := k.CreateKey(ctx, "a", kms.PurposeEncrypt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if os.Geteuid() == 0 {
		// root ignores directory permissions; point the KMS at a missing
		// directory instead.
		k.path = filepath.Join(dir, "missing", "keys.json")
	}
	if _, err := k.CreateKey(ctx, "b", kms.PurposeEncrypt); err == nil {
		t.Fatal("CreateKey must fail when the file cannot be written")
	}
	if err := k.RotateKey(ctx, ref); err == nil {
		t.Fatal("RotateKey must fail when the file cannot be written")
	}
	if err := k.DestroyKey(ctx, ref); err == nil {
		t.Fatal("DestroyKey must fail when the file cannot be written")
	}
	if _, ok := k.keys["b"]; ok {
		t.Fatal("failed CreateKey left a key behind")
	}
	if kk := k.keys["a"]; kk.destroyed || len(kk.versions) != 1 {
		t.Fatalf("failed writes changed key a: destroyed %v, %d versions", kk.destroyed, len(kk.versions))
	}
	if _, err := k.Encrypt(ctx, ref, []byte("x"), nil); err != nil {
		t.Fatalf("key must still work: %v", err)
	}
}

// TestDamagedFileRefused checks Open rejects inconsistent key records.
func TestDamagedFileRefused(t *testing.T) {
	t.Parallel()
	master := secret.NewBytes(make([]byte, crypto.KeySize))
	cases := map[string]func(*fileState){
		"unknown format": func(st *fileState) { st.Format = 2 },
		"bad name":       func(st *fileState) { st.Keys["bad.name"] = fileKey{Purpose: "encrypt", Versions: [][]byte{{1}}} },
		"bad purpose":    func(st *fileState) { st.Keys["k"] = fileKey{Purpose: "wrap", Versions: [][]byte{{1}}} },
		"no versions":    func(st *fileState) { st.Keys["k"] = fileKey{Purpose: "encrypt"} },
		"destroyed with material": func(st *fileState) {
			st.Keys["k"] = fileKey{Purpose: "sign", Destroyed: true, Versions: [][]byte{{1}}}
		},
		"bad check": func(st *fileState) { st.Check[len(st.Check)-1] ^= 1 },
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "keys.json")
			if _, err := Open(path, master); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var st fileState
			if err := json.Unmarshal(data, &st); err != nil {
				t.Fatal(err)
			}
			if st.Keys == nil {
				st.Keys = map[string]fileKey{}
			}
			damage(&st)
			if data, err = json.Marshal(st); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path, master); errs.CodeOf(err) != errs.CodeInvalidRequest {
				t.Fatalf("Open: %v, want refusal", err)
			}
		})
	}
}
