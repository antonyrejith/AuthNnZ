package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// AEAD format (version 1):
//
//	version (1 byte, 0x01) ‖ nonce (12 bytes) ‖ AES-256-GCM ciphertext ‖ tag (16 bytes)
//
// The version byte is also authenticated: it is prepended to the associated
// data, so a ciphertext cannot be reinterpreted under another format.
const (
	// KeySize is the size of an AES-256-GCM key in bytes.
	KeySize = 32
	// Overhead is the number of bytes Seal adds to the plaintext.
	Overhead = 1 + nonceSize + tagSize

	formatV1  byte = 0x01
	nonceSize int  = 12
	tagSize   int  = 16
)

// Seal encrypts and authenticates plaintext and authenticates aad with
// AES-256-GCM under key (KeySize bytes), using a fresh random 96-bit nonce.
// The same aad must be passed to Open. Use aad to bind the ciphertext to its
// context (for example tenant, table, column and row), so it cannot be moved
// elsewhere.
func Seal(key, plaintext, aad []byte) ([]byte, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 1, Overhead+len(plaintext))
	out[0] = formatV1
	// NewGCMWithRandomNonce draws the nonce from crypto/rand and prepends it.
	return aead.Seal(out, nil, plaintext, versionedAAD(formatV1, aad)), nil //nolint:gosec // G407: nil nonce: NewGCMWithRandomNonce generates a random one
}

// Open authenticates and decrypts a ciphertext produced by Seal. Any failure
// (wrong key, wrong aad, tampering, truncation or an unknown format version)
// returns an error wrapping ErrInvalidCiphertext; a key of the wrong length
// returns an error wrapping ErrInvalidKey. Open never panics.
func Open(key, ciphertext, aad []byte) ([]byte, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) == 0 {
		return nil, invalidCiphertext("empty ciphertext")
	}
	if ciphertext[0] != formatV1 {
		return nil, invalidCiphertext(fmt.Sprintf("unknown format version %d", ciphertext[0]))
	}
	if len(ciphertext) < Overhead {
		return nil, invalidCiphertext("ciphertext is truncated")
	}
	plaintext, err := aead.Open(nil, nil, ciphertext[1:], versionedAAD(formatV1, aad))
	if err != nil {
		return nil, invalidCiphertext("message authentication failed")
	}
	if plaintext == nil {
		plaintext = []byte{}
	}
	return plaintext, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, errs.Wrap(errs.CodeInternal,
			fmt.Errorf("%w: AES-256-GCM key is %d bytes, want %d", ErrInvalidKey, len(key), KeySize),
			"invalid encryption key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "create cipher")
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "create cipher")
	}
	return aead, nil
}

func versionedAAD(version byte, aad []byte) []byte {
	out := make([]byte, 0, 1+len(aad))
	out = append(out, version)
	return append(out, aad...)
}

func invalidCiphertext(detail string) error {
	return errs.Wrap(errs.CodeInvalidRequest, fmt.Errorf("%w: %s", ErrInvalidCiphertext, detail), "decryption failed")
}
