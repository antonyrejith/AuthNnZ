// Package kms defines Portun's key management interface: named keys that
// never leave the key management service (KMS), used to encrypt small
// secrets (above all data-encryption keys, see package envelope) and to sign.
//
// Key hierarchy (plan 04 §8.1): a root key in the KMS or HSM protects one key-
// encryption key (KEK) per tenant and region; KEKs wrap data-encryption keys
// and signing keys. Destroying a KEK crypto-shreds everything under it.
//
// Implementations: package local (development and tests, keys in an encrypted
// file or in memory) and package openbao (OpenBao transit). Every
// implementation passes the shared contract suite in package kmstest.
//
// Semantics shared by every implementation:
//
//   - Keys have a purpose fixed at creation: PurposeEncrypt (AES-256-GCM) or
//     PurposeSign (ECDSA P-256 with SHA-256). Using a key for the other purpose
//     fails with errs.CodeInvalidRequest.
//   - Keys are versioned. RotateKey adds a version; Encrypt and Sign use the
//     latest version; Decrypt accepts every version. Ciphertexts and
//     signatures carry the version that produced them (CiphertextVersion,
//     Signature.KeyVersion), so data can be found and re-encrypted after a
//     rotation and old signatures can be verified with PublicKeys.
//   - DestroyKey irreversibly deletes every version (crypto-shredding).
//     Afterwards every operation on the reference fails with
//     errs.CodeNotFound wrapping ErrKeyDestroyed, DestroyKey itself succeeds
//     again (it is idempotent), and the name cannot be reused. A reference
//     that never existed fails with errs.CodeNotFound wrapping ErrKeyNotFound.
//   - Ciphertexts that are malformed, tampered with, or presented with other
//     associated data fail with errs.CodeInvalidRequest. Plaintext and
//     associated data are limited to MaxDataSize bytes
//     (errs.CodePayloadTooLarge).
//   - A cancelled or expired context fails the call with an error that wraps
//     the context's error.
//   - Implementations are safe for concurrent use.
package kms

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	pcrypto "github.com/antonyrejith/portun/internal/platform/crypto"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// KMS is a key management service.
type KMS interface {
	// CreateKey creates a key with the given name and purpose and returns its
	// reference. Names are 1 to MaxNameLength characters of ASCII letters,
	// digits, '_' and '-', starting and ending with a letter, digit or '_'.
	// A name that is in use, or was used by a destroyed key, fails with
	// errs.CodeConflict.
	CreateKey(ctx context.Context, name string, purpose Purpose) (KeyRef, error)
	// RotateKey adds a new version to the key. Later Encrypt and Sign calls
	// use it; earlier ciphertexts still decrypt and earlier signatures still
	// verify against PublicKeys.
	RotateKey(ctx context.Context, ref KeyRef) error
	// Encrypt encrypts plaintext with the latest version of an encryption
	// key, authenticating aad, which Decrypt must be given again.
	Encrypt(ctx context.Context, ref KeyRef, plaintext, aad []byte) ([]byte, error)
	// Decrypt decrypts a ciphertext produced by Encrypt with any version of
	// the key.
	Decrypt(ctx context.Context, ref KeyRef, ciphertext, aad []byte) ([]byte, error)
	// Sign signs SHA-256(message) with the latest version of a signing key.
	// The signature verifies with Verify against PublicKeys()[KeyVersion].
	Sign(ctx context.Context, ref KeyRef, message []byte) (Signature, error)
	// PublicKey returns the public key (*ecdsa.PublicKey, P-256) of the
	// latest version of a signing key.
	PublicKey(ctx context.Context, ref KeyRef) (crypto.PublicKey, error)
	// PublicKeys returns the public keys of every version of a signing key,
	// by version, for verifying signatures made before a rotation and for
	// publishing key sets.
	PublicKeys(ctx context.Context, ref KeyRef) (map[int]crypto.PublicKey, error)
	// DestroyKey irreversibly destroys every version of the key. It is
	// idempotent: destroying a destroyed key succeeds.
	DestroyKey(ctx context.Context, ref KeyRef) error
}

// KeyRef is an opaque, provider-prefixed reference to a key, as returned by
// CreateKey, for example "local:tenant_kek" or "openbao:transit/tenant_kek".
// It identifies the key, not a version, and is safe to store and log: it is
// not secret.
type KeyRef string

// Provider returns the provider prefix of the reference ("local",
// "openbao", ...), or "" if it has none.
func (r KeyRef) Provider() string {
	p, _, ok := strings.Cut(string(r), ":")
	if !ok {
		return ""
	}
	return p
}

// Purpose is what a key is for. It is fixed when the key is created.
type Purpose int

// Key purposes. The zero value is invalid.
const (
	// PurposeEncrypt keys encrypt and decrypt with AES-256-GCM.
	PurposeEncrypt Purpose = iota + 1
	// PurposeSign keys sign with ECDSA P-256 over SHA-256 (ES256).
	PurposeSign
)

// String returns "encrypt", "sign" or "invalid".
func (p Purpose) String() string {
	switch p {
	case PurposeEncrypt:
		return "encrypt"
	case PurposeSign:
		return "sign"
	default:
		return "invalid"
	}
}

// ParsePurpose parses the output of Purpose.String.
func ParsePurpose(s string) (Purpose, error) {
	switch s {
	case "encrypt":
		return PurposeEncrypt, nil
	case "sign":
		return PurposeSign, nil
	default:
		return 0, errs.New(errs.CodeInvalidRequest, fmt.Sprintf("unknown key purpose %q", s))
	}
}

// Valid reports whether p is a defined purpose.
func (p Purpose) Valid() bool {
	return p == PurposeEncrypt || p == PurposeSign
}

// Signature is an ASN.1 DER ECDSA signature over SHA-256(message) and the key
// version that produced it.
type Signature struct {
	KeyVersion int
	DER        []byte
}

// Sentinel errors, usable with errors.Is. Both come wrapped in an *errs.Error
// with errs.CodeNotFound.
var (
	// ErrKeyDestroyed reports that the key was destroyed (crypto-shredded).
	ErrKeyDestroyed = errors.New("kms: key destroyed")
	// ErrKeyNotFound reports that the key does not exist and never existed.
	ErrKeyNotFound = errors.New("kms: key not found")
	// ErrInvalidSignature reports a signature that does not verify.
	ErrInvalidSignature = errors.New("kms: invalid signature")
)

const (
	// MaxNameLength is the longest key name accepted.
	MaxNameLength = 128
	// MaxDataSize bounds plaintext and associated data. A KMS encrypts keys
	// and small secrets; bulk data uses envelope encryption.
	MaxDataSize = 64 << 10
)

// ValidateName checks a key name (see KMS.CreateKey).
func ValidateName(name string) error {
	if name == "" || len(name) > MaxNameLength {
		return errs.New(errs.CodeInvalidRequest, fmt.Sprintf("key name must be 1 to %d characters", MaxNameLength))
	}
	for i := range len(name) {
		c := name[i]
		word := c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if word || c == '-' && i > 0 && i < len(name)-1 {
			continue
		}
		return errs.New(errs.CodeInvalidRequest,
			"key name may contain only letters, digits, '_' and '-', and may not start or end with '-'")
	}
	return nil
}

// CheckPurpose returns an error unless p is a defined purpose.
func CheckPurpose(p Purpose) error {
	if !p.Valid() {
		return errs.New(errs.CodeInvalidRequest, fmt.Sprintf("invalid key purpose %d", int(p)))
	}
	return nil
}

// CheckDataSize returns errs.CodePayloadTooLarge if plaintext or aad exceeds
// MaxDataSize.
func CheckDataSize(plaintext, aad []byte) error {
	if len(plaintext) > MaxDataSize || len(aad) > MaxDataSize {
		return errs.New(errs.CodePayloadTooLarge, fmt.Sprintf("plaintext and associated data are limited to %d bytes", MaxDataSize))
	}
	return nil
}

// NotFoundError returns the error for a reference to a key that does not
// exist: errs.CodeNotFound wrapping ErrKeyNotFound.
func NotFoundError(ref KeyRef) error {
	return errs.Wrap(errs.CodeNotFound, fmt.Errorf("%w: %s", ErrKeyNotFound, ref), "key not found")
}

// DestroyedError returns the error for a reference to a destroyed key:
// errs.CodeNotFound wrapping ErrKeyDestroyed.
func DestroyedError(ref KeyRef) error {
	return errs.Wrap(errs.CodeNotFound, fmt.Errorf("%w: %s", ErrKeyDestroyed, ref), "key has been destroyed")
}

// PurposeError returns the error for using a key with purpose have where want
// is required: errs.CodeInvalidRequest.
func PurposeError(ref KeyRef, have, want Purpose) error {
	return errs.Wrap(errs.CodeInvalidRequest, fmt.Errorf("key %s has purpose %s, operation needs %s", ref, have, want),
		"key cannot be used for this operation")
}

// InvalidRefError returns the error for a malformed reference or one that
// belongs to another provider: errs.CodeInvalidRequest.
func InvalidRefError(ref KeyRef, detail string) error {
	return errs.Wrap(errs.CodeInvalidRequest, fmt.Errorf("key reference %q: %s", ref, detail), "invalid key reference")
}

// Ciphertext format shared by every implementation:
//
//	format (1 byte, 0x01) ‖ key version (4 bytes, big-endian, ≥ 1) ‖ provider payload
const (
	ciphertextFormatV1 byte = 0x01
	ciphertextHeader        = 5
	maxKeyVersion           = 1<<31 - 1
)

// EncodeCiphertext builds a ciphertext from the key version that produced it
// and the provider's payload. It is for KMS implementations.
func EncodeCiphertext(version int, payload []byte) []byte {
	out := make([]byte, ciphertextHeader, ciphertextHeader+len(payload))
	out[0] = ciphertextFormatV1
	binary.BigEndian.PutUint32(out[1:ciphertextHeader], uint32(version)) //nolint:gosec // G115: versions are 1..maxKeyVersion, checked by callers and DecodeCiphertext
	return append(out, payload...)
}

// DecodeCiphertext splits a ciphertext into its key version and provider
// payload. Malformed input fails with errs.CodeInvalidRequest wrapping
// crypto.ErrInvalidCiphertext. It is for KMS implementations.
func DecodeCiphertext(ciphertext []byte) (int, []byte, error) {
	if len(ciphertext) <= ciphertextHeader {
		return 0, nil, invalidCiphertext("ciphertext is truncated")
	}
	if ciphertext[0] != ciphertextFormatV1 {
		return 0, nil, invalidCiphertext(fmt.Sprintf("unknown ciphertext format %d", ciphertext[0]))
	}
	v := binary.BigEndian.Uint32(ciphertext[1:ciphertextHeader])
	if v < 1 || v > maxKeyVersion {
		return 0, nil, invalidCiphertext(fmt.Sprintf("invalid key version %d", v))
	}
	return int(v), ciphertext[ciphertextHeader:], nil
}

// CiphertextVersion returns the version of the key that produced ciphertext,
// so data encrypted under an old version can be found and re-encrypted after
// a rotation.
func CiphertextVersion(ciphertext []byte) (int, error) {
	v, _, err := DecodeCiphertext(ciphertext)
	return v, err
}

// CheckVersion returns an error unless v is a valid key version (1 or more).
// It is for KMS implementations.
func CheckVersion(v int) error {
	if v < 1 || v > maxKeyVersion {
		return errs.New(errs.CodeInternal, fmt.Sprintf("invalid key version %d", v))
	}
	return nil
}

func invalidCiphertext(detail string) error {
	return errs.Wrap(errs.CodeInvalidRequest, fmt.Errorf("%w: %s", pcrypto.ErrInvalidCiphertext, detail), "decryption failed")
}

// Digest returns SHA-256(message), the value a signing key signs.
func Digest(message []byte) []byte {
	d := sha256.Sum256(message)
	return d[:]
}

// Verify checks an ASN.1 DER ECDSA signature over SHA-256(message) against a
// P-256 public key, as returned by PublicKey and PublicKeys. A signature that
// does not verify fails with errs.CodeInvalidRequest wrapping
// ErrInvalidSignature.
func Verify(pub crypto.PublicKey, message, der []byte) error {
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok || ec.Curve != elliptic.P256() {
		return errs.Wrap(errs.CodeInvalidRequest, fmt.Errorf("%w: public key %T is not ECDSA P-256", ErrInvalidSignature, pub),
			"unsupported public key")
	}
	if !ecdsa.VerifyASN1(ec, Digest(message), der) {
		return errs.Wrap(errs.CodeInvalidRequest, ErrInvalidSignature, "signature verification failed")
	}
	return nil
}
