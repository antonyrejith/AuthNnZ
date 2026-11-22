// Package envelope implements envelope encryption on top of a KMS: data is
// encrypted locally with a random AES-256-GCM data-encryption key (DEK), and
// the DEK is encrypted ("wrapped") by a key-encryption key (KEK) inside the
// KMS. Only the wrapped DEK is stored; destroying the KEK makes every DEK under
// it, and so the data, unrecoverable (crypto-shredding).
//
// Each wrapped DEK is bound to its owner (for example "tenant:ten_…" or
// "tenant:ten_…/subject:usr_…") through the KMS's associated data, so a
// wrapped DEK copied to another owner does not unwrap. Callers should also
// pass associated data to DEK.Seal that names where the ciphertext belongs
// (table, column, row), so ciphertexts cannot be moved between records.
//
// Storage shape: a key store persists Wrapped (KEK reference and wrapped DEK)
// and may cache unwrapped DEKs; a DEK is immutable and safe for concurrent
// use. After a KEK rotation, Rewrap moves a DEK to the KEK's latest version;
// kms.CiphertextVersion(w.Ciphertext) tells which version wraps it. Use one
// DEK for at most 2^32 messages (random 96-bit nonces).
package envelope

import (
	"context"
	"fmt"
	"io"

	"github.com/antonyrejith/portun/internal/platform/crypto"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/kms"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// MaxOwnerLength bounds the owner string.
const MaxOwnerLength = 1024

const ownerAADPrefix = "portun.envelope.dek.v1\x00"

// DEK is an unwrapped data-encryption key. Every formatting path prints
// "[REDACTED]".
type DEK struct {
	key secret.Bytes
}

// Wrapped is a DEK encrypted by a KEK: what a key store persists.
type Wrapped struct {
	// KEK is the key-encryption key that wraps the DEK.
	KEK kms.KeyRef
	// Ciphertext is the DEK encrypted by the KEK (a kms ciphertext; it
	// records the KEK version).
	Ciphertext []byte
}

// Envelope creates and unwraps DEKs through a KMS.
type Envelope struct {
	kms kms.KMS
}

// New returns an Envelope that wraps DEKs with k.
func New(k kms.KMS) *Envelope {
	return &Envelope{kms: k}
}

// NewDEK generates a random DEK and wraps it with kek for owner.
func (e *Envelope) NewDEK(ctx context.Context, kek kms.KeyRef, owner string) (DEK, Wrapped, error) {
	if err := checkOwner(owner); err != nil {
		return DEK{}, Wrapped{}, err
	}
	raw, err := crypto.RandomBytes(crypto.KeySize)
	if err != nil {
		return DEK{}, Wrapped{}, wrap(err)
	}
	defer clear(raw)
	ct, err := e.kms.Encrypt(ctx, kek, raw, ownerAAD(owner))
	if err != nil {
		return DEK{}, Wrapped{}, fmt.Errorf("wrap data key: %w", err)
	}
	return DEK{key: secret.NewBytes(raw)}, Wrapped{KEK: kek, Ciphertext: ct}, nil
}

// Unwrap decrypts a wrapped DEK for owner. It fails with errs.CodeNotFound
// wrapping kms.ErrKeyDestroyed once the KEK is destroyed, and with
// errs.CodeInvalidRequest if w belongs to another owner or was tampered with.
func (e *Envelope) Unwrap(ctx context.Context, w Wrapped, owner string) (DEK, error) {
	if err := checkOwner(owner); err != nil {
		return DEK{}, err
	}
	raw, err := e.kms.Decrypt(ctx, w.KEK, w.Ciphertext, ownerAAD(owner))
	if err != nil {
		return DEK{}, fmt.Errorf("unwrap data key: %w", err)
	}
	defer clear(raw)
	if len(raw) != crypto.KeySize {
		return DEK{}, errs.New(errs.CodeInternal, fmt.Sprintf("unwrapped data key is %d bytes, want %d", len(raw), crypto.KeySize))
	}
	return DEK{key: secret.NewBytes(raw)}, nil
}

// Rewrap re-wraps a DEK under the latest version of its KEK, for use after
// the KEK is rotated. The DEK itself, and so the data, is unchanged.
func (e *Envelope) Rewrap(ctx context.Context, w Wrapped, owner string) (Wrapped, error) {
	dek, err := e.Unwrap(ctx, w, owner)
	if err != nil {
		return Wrapped{}, err
	}
	raw := dek.key.Reveal()
	defer clear(raw)
	ct, err := e.kms.Encrypt(ctx, w.KEK, raw, ownerAAD(owner))
	if err != nil {
		return Wrapped{}, fmt.Errorf("rewrap data key: %w", err)
	}
	return Wrapped{KEK: w.KEK, Ciphertext: ct}, nil
}

// Seal encrypts plaintext with the DEK (AES-256-GCM, see crypto.Seal).
func (d DEK) Seal(plaintext, aad []byte) ([]byte, error) {
	k := d.key.Reveal()
	defer clear(k)
	ct, err := crypto.Seal(k, plaintext, aad)
	if err != nil {
		return nil, wrap(err)
	}
	return ct, nil
}

// Open decrypts a ciphertext produced by Seal (see crypto.Open).
func (d DEK) Open(ciphertext, aad []byte) ([]byte, error) {
	k := d.key.Reveal()
	defer clear(k)
	pt, err := crypto.Open(k, ciphertext, aad)
	if err != nil {
		return nil, wrap(err)
	}
	return pt, nil
}

// wrap adds this package's context; errs codes and sentinels stay reachable.
func wrap(err error) error {
	return fmt.Errorf("envelope: %w", err)
}

// Format implements fmt.Formatter so no verb can print the key.
func (DEK) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, "envelope.DEK{[REDACTED]}")
}

func checkOwner(owner string) error {
	if owner == "" || len(owner) > MaxOwnerLength {
		return errs.New(errs.CodeInternal, fmt.Sprintf("data key owner must be 1 to %d bytes", MaxOwnerLength))
	}
	return nil
}

func ownerAAD(owner string) []byte {
	return []byte(ownerAADPrefix + owner)
}
