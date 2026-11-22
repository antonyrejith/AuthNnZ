package crypto

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// MinMACKeySize is the smallest HMAC-SHA256 key accepted: 256 bits.
const MinMACKeySize = 32

// Equal reports whether a and b are equal, in time that depends only on their
// lengths. Use it for every comparison of secrets, tokens and MACs. The length
// itself is not secret.
func Equal(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

// HMACSHA256 returns HMAC-SHA256(key, message). key must be at least
// MinMACKeySize bytes.
func HMACSHA256(key, message []byte) ([]byte, error) {
	if len(key) < MinMACKeySize {
		return nil, errs.Wrap(errs.CodeInternal,
			fmt.Errorf("%w: HMAC key is %d bytes, want at least %d", ErrInvalidKey, len(key), MinMACKeySize),
			"invalid MAC key")
	}
	m := hmac.New(sha256.New, key)
	m.Write(message)
	return m.Sum(nil), nil
}

// VerifyHMACSHA256 reports whether mac is HMAC-SHA256(key, message), comparing
// in constant time. It returns false for keys shorter than MinMACKeySize.
func VerifyHMACSHA256(key, message, mac []byte) bool {
	want, err := HMACSHA256(key, message)
	if err != nil {
		return false
	}
	return hmac.Equal(want, mac)
}

// DeriveKey derives length bytes from secret with HKDF-SHA256 (RFC 5869).
// info names the purpose of the derived key (domain separation) and must not
// be empty; salt may be nil. length must be between 1 and 255*32 bytes.
func DeriveKey(secret, salt []byte, info string, length int) ([]byte, error) {
	switch {
	case len(secret) == 0:
		return nil, errs.Wrap(errs.CodeInternal, fmt.Errorf("%w: empty input key material", ErrInvalidKey), "invalid key derivation input")
	case info == "":
		return nil, errs.Wrap(errs.CodeInternal, fmt.Errorf("%w: empty info", ErrInvalidArgument), "invalid key derivation input")
	case length < 1 || length > 255*sha256.Size:
		return nil, errs.Wrap(errs.CodeInternal, fmt.Errorf("%w: length %d", ErrInvalidArgument, length), "invalid key derivation input")
	}
	out, err := hkdf.Key(sha256.New, secret, salt, info, length)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "derive key")
	}
	return out, nil
}
