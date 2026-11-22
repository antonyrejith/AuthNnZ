package local_test

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/crypto"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/kms"
	"github.com/antonyrejith/portun/internal/platform/kms/kmstest"
	"github.com/antonyrejith/portun/internal/platform/kms/local"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

func masterKey(t *testing.T) secret.Bytes {
	t.Helper()
	b, err := crypto.RandomBytes(crypto.KeySize)
	if err != nil {
		t.Fatal(err)
	}
	return secret.NewBytes(b)
}

// TestContractMemory runs the KMS contract against the in-memory KMS.
//
// Covers: SEC-08
func TestContractMemory(t *testing.T) {
	t.Parallel()
	kmstest.Run(t, func(t *testing.T) kms.KMS {
		t.Helper()
		k, err := local.NewMemory()
		if err != nil {
			t.Fatal(err)
		}
		return k
	})
}

// TestContractFile runs the KMS contract against the file-backed KMS.
//
// Covers: SEC-08
func TestContractFile(t *testing.T) {
	t.Parallel()
	kmstest.Run(t, func(t *testing.T) kms.KMS {
		t.Helper()
		k, err := local.Open(filepath.Join(t.TempDir(), "keys", "kms.json"), masterKey(t))
		if err != nil {
			t.Fatal(err)
		}
		return k
	})
}

// TestFilePermissions checks the key file and its directory are private and
// that Open refuses files other users can read.
//
// Covers: SEC-08
func TestFilePermissions(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "kms")
	path := filepath.Join(dir, "keys.json")
	k, err := local.Open(path, masterKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.CreateKey(t.Context(), "kek", kms.PurposeEncrypt); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode %#o, want %#o", p, info.Mode().Perm(), want)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("directory must hold only the key file, got %d entries (%v)", len(entries), err)
	}

	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o666} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		_, err := local.Open(path, masterKey(t))
		if errs.CodeOf(err) != errs.CodeInvalidRequest || !strings.Contains(err.Error(), "group or others") {
			t.Fatalf("Open with mode %#o: %v, want refusal", mode, err)
		}
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Open(link, masterKey(t)); errs.CodeOf(err) != errs.CodeInvalidRequest {
		t.Fatalf("Open through a symlink: %v, want refusal", err)
	}
}

// TestReopen checks keys, versions and destruction survive a restart and that
// a wrong master key or a damaged file is refused.
//
// Covers: SEC-08
func TestReopen(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "keys.json")
	master := masterKey(t)
	k, err := local.Open(path, master)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := k.CreateKey(ctx, "kek", kms.PurposeEncrypt)
	if err != nil {
		t.Fatal(err)
	}
	gone, err := k.CreateKey(ctx, "gone", kms.PurposeSign)
	if err != nil {
		t.Fatal(err)
	}
	ct1, _ := k.Encrypt(ctx, enc, []byte("one"), nil)
	if err := k.RotateKey(ctx, enc); err != nil {
		t.Fatal(err)
	}
	ct2, _ := k.Encrypt(ctx, enc, []byte("two"), nil)
	if err := k.DestroyKey(ctx, gone); err != nil {
		t.Fatal(err)
	}

	k2, err := local.Open(path, master)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	for ct, want := range map[string]string{string(ct1): "one", string(ct2): "two"} {
		if pt, err := k2.Decrypt(ctx, enc, []byte(ct), nil); err != nil || string(pt) != want {
			t.Fatalf("after reopen: %q, %v", pt, err)
		}
	}
	if _, err := k2.Sign(ctx, gone, []byte("m")); !errors.Is(err, kms.ErrKeyDestroyed) {
		t.Fatalf("destroyed key after reopen: %v", err)
	}

	if _, err := local.Open(path, masterKey(t)); errs.CodeOf(err) != errs.CodeInvalidRequest {
		t.Fatalf("Open with the wrong master key: %v", err)
	}
	if _, err := local.Open(path, secret.NewBytes([]byte("short"))); errs.CodeOf(err) != errs.CodeInvalidRequest {
		t.Fatalf("Open with a short master key: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Open(path, master); errs.CodeOf(err) != errs.CodeInvalidRequest {
		t.Fatalf("Open of a damaged file: %v", err)
	}
}

func TestStaleTempFilesRemoved(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	stale := filepath.Join(dir, ".keys.json.123.tmp")
	if err := os.WriteFile(stale, []byte("leftover"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Open(path, masterKey(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale temporary file still present: %v", err)
	}
}

func TestParseMasterKey(t *testing.T) {
	t.Parallel()
	raw, err := crypto.RandomBytes(32)
	if err != nil {
		t.Fatal(err)
	}
	good := base64.StdEncoding.EncodeToString(raw)
	cases := []struct {
		name  string
		value string
		ok    bool
	}{
		{"valid", good, true},
		{"valid with newline", good + "\n", true},
		{"too short", base64.StdEncoding.EncodeToString(raw[:31]), false},
		{"too long", base64.StdEncoding.EncodeToString(append(raw, 1)), false},
		{"not base64", "!!" + good[2:], false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := local.ParseMasterKey(secret.NewString(c.value))
			if c.ok {
				if err != nil || string(got.Reveal()) != string(raw) {
					t.Fatalf("ParseMasterKey: %v", err)
				}
				return
			}
			if errs.CodeOf(err) != errs.CodeInvalidRequest {
				t.Fatalf("ParseMasterKey: %v, want invalid request", err)
			}
			if c.value != "" && strings.Contains(err.Error(), strings.TrimSpace(c.value)) {
				t.Fatal("error leaks the master key")
			}
		})
	}
}

func TestNewFromConfig(t *testing.T) {
	t.Parallel()
	raw, err := crypto.RandomBytes(32)
	if err != nil {
		t.Fatal(err)
	}
	cfg := local.Config{File: filepath.Join(t.TempDir(), "k.json"), MasterKey: secret.NewString(base64.StdEncoding.EncodeToString(raw))}
	k, err := local.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.CreateKey(t.Context(), "a", kms.PurposeEncrypt); err != nil {
		t.Fatal(err)
	}
	cfg.MasterKey = secret.NewString("bad")
	if _, err := local.New(cfg); errs.CodeOf(err) != errs.CodeInvalidRequest {
		t.Fatalf("New with a bad master key: %v", err)
	}
	if _, err := local.Open("", secret.NewBytes(raw)); errs.CodeOf(err) != errs.CodeInvalidRequest {
		t.Fatalf("Open with an empty path: %v", err)
	}
}

// TestFormatRedacts checks no fmt verb prints the master key.
//
// Covers: SEC-08
func TestFormatRedacts(t *testing.T) {
	t.Parallel()
	k, err := local.NewMemory()
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%d", "%x"} {
		if got := fmt.Sprintf(verb, k); got != "local.KMS{[REDACTED]}" {
			t.Errorf("%s printed %q", verb, got)
		}
	}
}
