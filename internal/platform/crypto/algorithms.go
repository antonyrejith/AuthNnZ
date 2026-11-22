package crypto

import (
	stdcrypto "crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"fmt"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// Approved signing algorithms, by their JOSE "alg" names (RFC 7518, RFC 8037).
// Names are case-sensitive.
const (
	// AlgES256 is ECDSA with P-256 and SHA-256: the default.
	AlgES256 = "ES256"
	// AlgRS256 is RSASSA-PKCS1-v1_5 with SHA-256.
	AlgRS256 = "RS256"
	// AlgPS256 is RSASSA-PSS with SHA-256 and MGF1 with SHA-256.
	AlgPS256 = "PS256"
	// AlgEdDSA is EdDSA with Ed25519.
	AlgEdDSA = "EdDSA"

	// DefaultSigningAlg is used unless a tenant or client chooses another
	// approved algorithm.
	DefaultSigningAlg = AlgES256
)

// EncA256GCM is the only approved content-encryption algorithm (JOSE "enc").
const EncA256GCM = "A256GCM"

// RSA key sizes for RS256 and PS256.
const (
	// MinRSAKeyBits is the smallest RSA modulus accepted.
	MinRSAKeyBits = 2048
	// RecommendedRSAKeyBits is the size Portun generates.
	RecommendedRSAKeyBits = 3072
)

// ApprovedSigningAlgs returns the approved signing algorithms, default first.
func ApprovedSigningAlgs() []string {
	return []string{AlgES256, AlgRS256, AlgPS256, AlgEdDSA}
}

// IsApprovedSigningAlg reports whether alg is an approved signing algorithm.
// "none", the HMAC algorithms (HS256, HS384, HS512: never accepted where an
// asymmetric signature is expected, which prevents algorithm confusion),
// unknown names and names in the wrong case are rejected.
func IsApprovedSigningAlg(alg string) bool {
	switch alg {
	case AlgES256, AlgRS256, AlgPS256, AlgEdDSA:
		return true
	default:
		return false
	}
}

// IsApprovedHash reports whether h is an approved hash function: SHA-256,
// SHA-384 or SHA-512.
func IsApprovedHash(h stdcrypto.Hash) bool {
	switch h { //nolint:exhaustive // allow-list: every other hash is rejected
	case stdcrypto.SHA256, stdcrypto.SHA384, stdcrypto.SHA512:
		return true
	default:
		return false
	}
}

// CheckSigningKey reports whether key is acceptable for alg: alg must be
// approved and key must be of the matching kind (P-256 for ES256, RSA of at
// least MinRSAKeyBits for RS256 and PS256, Ed25519 for EdDSA). key may be a
// public key or a private key (anything with a Public method).
func CheckSigningKey(alg string, key any) error {
	if !IsApprovedSigningAlg(alg) {
		return notApproved(fmt.Sprintf("signing algorithm %q is not approved", alg))
	}
	if priv, ok := key.(interface{ Public() stdcrypto.PublicKey }); ok {
		key = priv.Public()
	}
	switch alg {
	case AlgES256:
		k, ok := key.(*ecdsa.PublicKey)
		if !ok || k.Curve != elliptic.P256() {
			return notApproved("ES256 requires a P-256 key")
		}
	case AlgRS256, AlgPS256:
		k, ok := key.(*rsa.PublicKey)
		if !ok || k.N == nil {
			return notApproved(alg + " requires an RSA key")
		}
		if bits := k.N.BitLen(); bits < MinRSAKeyBits {
			return notApproved(fmt.Sprintf("%s requires an RSA key of at least %d bits, got %d", alg, MinRSAKeyBits, bits))
		}
	case AlgEdDSA:
		k, ok := key.(ed25519.PublicKey)
		if !ok || len(k) != ed25519.PublicKeySize {
			return notApproved("EdDSA requires an Ed25519 key")
		}
	}
	return nil
}

func notApproved(detail string) error {
	return errs.Wrap(errs.CodeInvalidRequest, fmt.Errorf("%w: %s", ErrNotApproved, detail), "algorithm or key not approved")
}
