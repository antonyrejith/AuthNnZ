// Package local is a KMS for development and tests. Keys live in memory and,
// for the file-backed variant, in a single JSON file in which every piece of
// key material is encrypted with AES-256-GCM under a 32-byte master key
// supplied through configuration. The associated data of each encrypted key
// binds it to its key name, purpose and version, so material cannot be moved
// between keys or versions in the file without detection.
//
// The file is written atomically (temporary file, fsync, rename, directory
// fsync) with mode 0600, and Open refuses a file that group or others can
// access. After each write the previous file's blocks are overwritten with
// zeros on a best-effort basis; journaling, copy-on-write file systems and SSD
// wear levelling may keep old blocks, which is why the material is encrypted
// at rest. DestroyKey removes the key's material from memory and from the
// file and keeps a tombstone, so the name cannot be reused.
//
// One process at a time may use a key file. Production uses a real KMS or HSM
// (package openbao or a cloud KMS); this package is not for production.
package local

import (
	"context"
	stdcrypto "crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/antonyrejith/portun/internal/platform/crypto"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/kms"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// RefPrefix starts every reference this KMS issues: "local:<name>".
const RefPrefix = "local:"

// Config configures a file-backed local KMS from the environment.
type Config struct {
	// File is the path of the key file. It is created if missing.
	File string `env:"FILE" required:"true"`
	// MasterKey is 32 bytes, standard base64 (for example the output of
	// "openssl rand -base64 32").
	MasterKey secret.String `env:"MASTER_KEY" required:"true"`
}

// KMS is the local key management service. It implements kms.KMS.
type KMS struct {
	mu     sync.RWMutex
	master []byte
	path   string // "" for in-memory
	keys   map[string]*key
}

var _ kms.KMS = (*KMS)(nil)

type key struct {
	purpose   kms.Purpose
	destroyed bool
	versions  [][]byte // versions[i] is version i+1, sealed under the master key
}

// New opens the file-backed KMS described by cfg.
func New(cfg Config) (*KMS, error) {
	master, err := ParseMasterKey(cfg.MasterKey)
	if err != nil {
		return nil, err
	}
	return Open(cfg.File, master)
}

// NewMemory returns an in-memory KMS with a random master key, for tests.
func NewMemory() (*KMS, error) {
	master, err := crypto.RandomBytes(crypto.KeySize)
	if err != nil {
		return nil, wrap(err)
	}
	return &KMS{master: master, keys: map[string]*key{}}, nil
}

// ParseMasterKey decodes a standard base64 master key of exactly 32 bytes.
// Errors never contain the value.
func ParseMasterKey(s secret.String) (secret.Bytes, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s.Reveal()))
	if err != nil {
		return secret.Bytes{}, errs.New(errs.CodeInvalidRequest, "local KMS master key is not valid base64")
	}
	defer clear(raw)
	if len(raw) != crypto.KeySize {
		return secret.Bytes{}, errs.New(errs.CodeInvalidRequest,
			fmt.Sprintf("local KMS master key must be %d bytes, got %d", crypto.KeySize, len(raw)))
	}
	return secret.NewBytes(raw), nil
}

// Format implements fmt.Formatter so no verb can print the master key.
func (k *KMS) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, "local.KMS{[REDACTED]}")
}

// CreateKey implements kms.KMS.
func (k *KMS) CreateKey(ctx context.Context, name string, purpose kms.Purpose) (kms.KeyRef, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("local kms: %w", err)
	}
	if err := kms.ValidateName(name); err != nil {
		return "", wrap(err)
	}
	if err := kms.CheckPurpose(purpose); err != nil {
		return "", wrap(err)
	}
	sealed, err := k.newVersion(name, purpose, 1)
	if err != nil {
		return "", err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if existing, ok := k.keys[name]; ok {
		if existing.destroyed {
			return "", errs.New(errs.CodeConflict, "key name was used by a destroyed key")
		}
		return "", errs.New(errs.CodeConflict, "key already exists")
	}
	k.keys[name] = &key{purpose: purpose, versions: [][]byte{sealed}}
	if err := k.persist(); err != nil {
		delete(k.keys, name)
		return "", err
	}
	return kms.KeyRef(RefPrefix + name), nil
}

// RotateKey implements kms.KMS.
func (k *KMS) RotateKey(ctx context.Context, ref kms.KeyRef) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("local kms: %w", err)
	}
	name, err := parseRef(ref)
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	kk, err := k.live(ref, name)
	if err != nil {
		return err
	}
	sealed, err := k.newVersion(name, kk.purpose, len(kk.versions)+1)
	if err != nil {
		return err
	}
	kk.versions = append(kk.versions, sealed)
	if err := k.persist(); err != nil {
		kk.versions = kk.versions[:len(kk.versions)-1]
		return err
	}
	return nil
}

// Encrypt implements kms.KMS.
func (k *KMS) Encrypt(ctx context.Context, ref kms.KeyRef, plaintext, aad []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("local kms: %w", err)
	}
	if err := kms.CheckDataSize(plaintext, aad); err != nil {
		return nil, wrap(err)
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	name, kk, err := k.use(ref, kms.PurposeEncrypt)
	if err != nil {
		return nil, err
	}
	v := len(kk.versions)
	material, err := k.unseal(name, kk, v)
	if err != nil {
		return nil, err
	}
	defer clear(material)
	payload, err := crypto.Seal(material, plaintext, aad)
	if err != nil {
		return nil, wrap(err)
	}
	return kms.EncodeCiphertext(v, payload), nil
}

// Decrypt implements kms.KMS.
func (k *KMS) Decrypt(ctx context.Context, ref kms.KeyRef, ciphertext, aad []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("local kms: %w", err)
	}
	if err := kms.CheckDataSize(nil, aad); err != nil {
		return nil, wrap(err)
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	name, kk, err := k.use(ref, kms.PurposeEncrypt)
	if err != nil {
		return nil, err
	}
	v, payload, err := kms.DecodeCiphertext(ciphertext)
	if err != nil {
		return nil, wrap(err)
	}
	if v > len(kk.versions) {
		return nil, errs.New(errs.CodeInvalidRequest, "ciphertext refers to an unknown key version")
	}
	material, err := k.unseal(name, kk, v)
	if err != nil {
		return nil, err
	}
	defer clear(material)
	pt, err := crypto.Open(material, payload, aad)
	if err != nil {
		return nil, wrap(err)
	}
	return pt, nil
}

// Sign implements kms.KMS.
func (k *KMS) Sign(ctx context.Context, ref kms.KeyRef, message []byte) (kms.Signature, error) {
	if err := ctx.Err(); err != nil {
		return kms.Signature{}, fmt.Errorf("local kms: %w", err)
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	name, kk, err := k.use(ref, kms.PurposeSign)
	if err != nil {
		return kms.Signature{}, err
	}
	v := len(kk.versions)
	priv, err := k.signer(name, kk, v)
	if err != nil {
		return kms.Signature{}, err
	}
	der, err := ecdsa.SignASN1(rand.Reader, priv, kms.Digest(message))
	if err != nil {
		return kms.Signature{}, errs.Wrap(errs.CodeInternal, err, "sign")
	}
	return kms.Signature{KeyVersion: v, DER: der}, nil
}

// PublicKey implements kms.KMS.
func (k *KMS) PublicKey(ctx context.Context, ref kms.KeyRef) (stdcrypto.PublicKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("local kms: %w", err)
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	name, kk, err := k.use(ref, kms.PurposeSign)
	if err != nil {
		return nil, err
	}
	priv, err := k.signer(name, kk, len(kk.versions))
	if err != nil {
		return nil, err
	}
	return &priv.PublicKey, nil
}

// PublicKeys implements kms.KMS.
func (k *KMS) PublicKeys(ctx context.Context, ref kms.KeyRef) (map[int]stdcrypto.PublicKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("local kms: %w", err)
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	name, kk, err := k.use(ref, kms.PurposeSign)
	if err != nil {
		return nil, err
	}
	out := make(map[int]stdcrypto.PublicKey, len(kk.versions))
	for v := 1; v <= len(kk.versions); v++ {
		priv, err := k.signer(name, kk, v)
		if err != nil {
			return nil, err
		}
		out[v] = &priv.PublicKey
	}
	return out, nil
}

// DestroyKey implements kms.KMS.
func (k *KMS) DestroyKey(ctx context.Context, ref kms.KeyRef) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("local kms: %w", err)
	}
	name, err := parseRef(ref)
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	kk, ok := k.keys[name]
	if !ok {
		return wrap(kms.NotFoundError(ref))
	}
	if kk.destroyed {
		return nil
	}
	old := kk.versions
	kk.destroyed, kk.versions = true, nil
	if err := k.persist(); err != nil {
		kk.destroyed, kk.versions = false, old
		return err
	}
	for _, sealed := range old {
		clear(sealed)
	}
	return nil
}

func parseRef(ref kms.KeyRef) (string, error) {
	name, ok := strings.CutPrefix(string(ref), RefPrefix)
	if !ok {
		return "", wrap(kms.InvalidRefError(ref, "not a local KMS reference"))
	}
	if err := kms.ValidateName(name); err != nil {
		return "", wrap(kms.InvalidRefError(ref, "invalid key name"))
	}
	return name, nil
}

// live returns the key if it exists and is not destroyed. Callers hold k.mu.
func (k *KMS) live(ref kms.KeyRef, name string) (*key, error) {
	kk, ok := k.keys[name]
	switch {
	case !ok:
		return nil, wrap(kms.NotFoundError(ref))
	case kk.destroyed:
		return nil, wrap(kms.DestroyedError(ref))
	default:
		return kk, nil
	}
}

// use resolves ref to a live key with the wanted purpose. Callers hold k.mu.
func (k *KMS) use(ref kms.KeyRef, want kms.Purpose) (string, *key, error) {
	name, err := parseRef(ref)
	if err != nil {
		return "", nil, err
	}
	kk, err := k.live(ref, name)
	if err != nil {
		return "", nil, err
	}
	if kk.purpose != want {
		return "", nil, wrap(kms.PurposeError(ref, kk.purpose, want))
	}
	return name, kk, nil
}

// newVersion generates material for a version and seals it under the master
// key.
func (k *KMS) newVersion(name string, purpose kms.Purpose, version int) ([]byte, error) {
	var material []byte
	switch purpose {
	case kms.PurposeEncrypt:
		b, err := crypto.RandomBytes(crypto.KeySize)
		if err != nil {
			return nil, wrap(err)
		}
		material = b
	case kms.PurposeSign:
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "generate signing key")
		}
		der, err := x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "encode signing key")
		}
		material = der
	}
	defer clear(material)
	sealed, err := crypto.Seal(k.master, material, materialAAD(name, purpose, version))
	if err != nil {
		return nil, wrap(err)
	}
	return sealed, nil
}

// wrap adds this package's context; errs codes and sentinels stay reachable.
func wrap(err error) error {
	return fmt.Errorf("local kms: %w", err)
}

// unseal decrypts the material of one version. Callers hold k.mu.
func (k *KMS) unseal(name string, kk *key, version int) ([]byte, error) {
	material, err := crypto.Open(k.master, kk.versions[version-1], materialAAD(name, kk.purpose, version))
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "key material failed its integrity check")
	}
	return material, nil
}

func (k *KMS) signer(name string, kk *key, version int) (*ecdsa.PrivateKey, error) {
	der, err := k.unseal(name, kk, version)
	if err != nil {
		return nil, err
	}
	defer clear(der)
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "decode signing key")
	}
	priv, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || priv.Curve != elliptic.P256() {
		return nil, errs.New(errs.CodeInternal, "signing key is not ECDSA P-256")
	}
	return priv, nil
}

// materialAAD binds sealed material to its key name, purpose and version.
func materialAAD(name string, purpose kms.Purpose, version int) []byte {
	return []byte("portun.kms.local.v1\x00" + name + "\x00" + purpose.String() + "\x00" + strconv.Itoa(version))
}
