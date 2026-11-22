// Package crypto holds Portun's approved cryptographic primitives. Code
// outside this package does not use crypto/* building blocks directly for
// secrets, tokens or encryption; it calls these functions, so the choice of
// algorithms, parameters and formats is made once and reviewed once.
//
// Approved algorithms (the complete list; anything else is rejected):
//
//   - Randomness: crypto/rand only.
//   - Symmetric encryption: AES-256-GCM with a random 96-bit nonce (Seal,
//     Open). Use at most 2^32 messages per key.
//   - Key derivation: HKDF-SHA256 (DeriveKey).
//   - Message authentication: HMAC-SHA256 with keys of at least 256 bits.
//   - Hashing: SHA-256, SHA-384, SHA-512.
//   - Signing (JOSE "alg" names, for token and metadata signing): ES256
//     (default), RS256 and PS256 with RSA keys of at least 2048 bits (3072
//     recommended), and EdDSA with Ed25519.
//   - Content encryption (JOSE "enc"): A256GCM.
//
// Rejected: "none", HMAC algorithms (HS256, HS384, HS512) wherever an
// asymmetric signature is expected, RSA keys under 2048 bits, SHA-1, MD5 and
// every algorithm not listed above.
//
// Errors are platform errors (package errs). Invalid keys are programming or
// configuration errors (errs.CodeInternal); ciphertexts that are malformed,
// truncated, tampered with, of an unknown format version or encrypted under
// another key or associated data fail with errs.CodeInvalidRequest and wrap
// ErrInvalidCiphertext.
package crypto

import (
	"errors"
)

// Sentinel errors, usable with errors.Is. Every error returned by this package
// is an *errs.Error that wraps one of them.
var (
	// ErrInvalidKey reports a key of the wrong length or kind.
	ErrInvalidKey = errors.New("crypto: invalid key")
	// ErrInvalidCiphertext reports a ciphertext that cannot be decrypted:
	// malformed, truncated, tampered with, of an unknown format version, or
	// produced under another key or other associated data.
	ErrInvalidCiphertext = errors.New("crypto: invalid ciphertext")
	// ErrInvalidArgument reports an argument outside the allowed range.
	ErrInvalidArgument = errors.New("crypto: invalid argument")
	// ErrNotApproved reports an algorithm or key that is not on the approved
	// list.
	ErrNotApproved = errors.New("crypto: algorithm or key not approved")
)
