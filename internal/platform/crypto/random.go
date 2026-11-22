package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

const (
	// MinTokenBytes is the smallest token RandomToken produces: 128 bits.
	MinTokenBytes = 16
	// maxRandomBytes bounds a single request so a bad argument cannot exhaust
	// memory.
	maxRandomBytes = 1 << 20
)

// RandomBytes returns n bytes from crypto/rand. n must be between 1 and 1 MiB.
func RandomBytes(n int) ([]byte, error) {
	if n < 1 || n > maxRandomBytes {
		return nil, errs.Wrap(errs.CodeInternal, fmt.Errorf("%w: %d random bytes requested", ErrInvalidArgument, n),
			"invalid random length")
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "read random bytes")
	}
	return b, nil
}

// RandomToken returns nBytes random bytes encoded as unpadded base64url
// (RFC 4648 §5), suitable for URLs, headers and cookies. nBytes must be at
// least MinTokenBytes (128 bits).
func RandomToken(nBytes int) (string, error) {
	if nBytes < MinTokenBytes {
		return "", errs.Wrap(errs.CodeInternal,
			fmt.Errorf("%w: token of %d bytes is below the %d-byte minimum", ErrInvalidArgument, nBytes, MinTokenBytes),
			"invalid token length")
	}
	b, err := RandomBytes(nBytes)
	if err != nil {
		return "", err
	}
	defer clear(b)
	return base64.RawURLEncoding.EncodeToString(b), nil
}
