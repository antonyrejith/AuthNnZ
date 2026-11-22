package local

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/crypto"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/kms"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// TestCorruptMaterialFailsClosed replaces the sealed material of a key
// version with material that fails its integrity check, that is not a key,
// or that is the wrong kind of key; every operation must fail with an
// internal error instead of using it.
//
// Covers: SEC-08
func TestCorruptMaterialFailsClosed(t *testing.T) {
	t.Parallel()
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p384DER, err := x509.MarshalPKCS8PrivateKey(p384)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		purpose kms.Purpose
		// material returns the sealed bytes to install as version 1.
		material func(k *KMS, name string) []byte
	}{
		{"sign: integrity failure", kms.PurposeSign, func(*KMS, string) []byte { return []byte("garbage") }},
		{"sign: not a key", kms.PurposeSign, func(k *KMS, name string) []byte {
			return seal(t, k, []byte("not DER"), name, kms.PurposeSign)
		}},
		{"sign: wrong curve", kms.PurposeSign, func(k *KMS, name string) []byte {
			return seal(t, k, p384DER, name, kms.PurposeSign)
		}},
		{"encrypt: integrity failure", kms.PurposeEncrypt, func(*KMS, string) []byte { return []byte("garbage") }},
		{"encrypt: wrong key size", kms.PurposeEncrypt, func(k *KMS, name string) []byte {
			return seal(t, k, []byte("short"), name, kms.PurposeEncrypt)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			k, err := NewMemory()
			if err != nil {
				t.Fatal(err)
			}
			ref, err := k.CreateKey(ctx, "k", tt.purpose)
			if err != nil {
				t.Fatal(err)
			}
			var ciphertext []byte
			if tt.purpose == kms.PurposeEncrypt {
				if ciphertext, err = k.Encrypt(ctx, ref, []byte("x"), nil); err != nil {
					t.Fatal(err)
				}
			}
			k.keys["k"].versions[0] = tt.material(k, "k")
			var errsSeen []error
			if tt.purpose == kms.PurposeSign {
				_, e1 := k.Sign(ctx, ref, []byte("m"))
				_, e2 := k.PublicKey(ctx, ref)
				_, e3 := k.PublicKeys(ctx, ref)
				errsSeen = append(errsSeen, e1, e2, e3)
			} else {
				_, e1 := k.Encrypt(ctx, ref, []byte("x"), nil)
				_, e2 := k.Decrypt(ctx, ref, ciphertext, nil)
				errsSeen = append(errsSeen, e1, e2)
			}
			for i, e := range errsSeen {
				if errs.CodeOf(e) != errs.CodeInternal {
					t.Errorf("operation %d: err = %v, want internal", i, e)
				}
			}
		})
	}
}

func seal(t *testing.T, k *KMS, material []byte, name string, purpose kms.Purpose) []byte {
	t.Helper()
	sealed, err := crypto.Seal(k.master, material, materialAAD(name, purpose, 1))
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

// TestOpenRejectsBadPaths covers key file paths that cannot be used.
func TestOpenRejectsBadPaths(t *testing.T) {
	t.Parallel()
	master := secret.NewBytes(make([]byte, crypto.KeySize))
	dir := t.TempDir()
	notADir := filepath.Join(dir, "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	isADir := filepath.Join(dir, "dir")
	if err := os.Mkdir(isADir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(notADir, link); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path string
		want errs.Code
	}{
		{"parent is a file", filepath.Join(notADir, "keys.json"), errs.CodeInternal},
		{"path is a directory", isADir, errs.CodeInvalidRequest},
		{"path is a symlink", link, errs.CodeInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Open(tt.path, master); errs.CodeOf(err) != tt.want {
				t.Fatalf("Open(%s) = %v, want %s", tt.path, err, tt.want)
			}
		})
	}
}

// TestWriteAtomicRenameFailure: when the key file cannot be replaced, the
// temporary file is removed and the error is reported.
func TestWriteAtomicRenameFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "keys.json")
	if err := os.MkdirAll(filepath.Join(target, "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(target, []byte("{}")); errs.CodeOf(err) != errs.CodeInternal {
		t.Fatalf("writeAtomic onto a directory = %v, want internal", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, tempPattern(target)))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v", matches)
	}
}
