package checkpoint

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"

	"github.com/digitorus/pkcs7"
	"github.com/digitorus/timestamp"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// RFC 3161 timestamping. The ASN.1 and CMS encoding is done by
// github.com/digitorus/timestamp and github.com/digitorus/pkcs7; this file
// is the client: it builds a SHA-256 request with a nonce, sends it over
// HTTP (RFC 3161 §3.4), checks that the response answers it, and verifies
// tokens against configured trust anchors.

// Token is an RFC 3161 TimeStampToken and the time it asserts.
type Token struct {
	DER  []byte
	Time time.Time
}

// Stamper obtains timestamp tokens over data.
type Stamper interface {
	Timestamp(ctx context.Context, data []byte) (Token, error)
}

// HTTPStamper is an RFC 3161 client for a timestamp authority URL.
type HTTPStamper struct {
	URL    string
	Client *http.Client // nil: a client with a 10s timeout
	// Roots, when set, are the trust anchors every received token is
	// verified against before it is accepted.
	Roots *x509.CertPool
}

const (
	maxTSAResponse = 64 << 10
	defaultTimeout = 10 * time.Second
	nonceBits      = 64
)

// Timestamp implements Stamper.
func (s HTTPStamper) Timestamp(ctx context.Context, data []byte) (Token, error) {
	digest := sha256.Sum256(data)
	nonce, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), nonceBits))
	if err != nil {
		return Token{}, errs.Wrap(errs.CodeInternal, err, "tsa: generate a nonce")
	}
	req := timestamp.Request{HashAlgorithm: crypto.SHA256, HashedMessage: digest[:], Certificates: true, Nonce: nonce}
	body, err := req.Marshal()
	if err != nil {
		return Token{}, errs.Wrap(errs.CodeInternal, err, "tsa: encode the request")
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return Token{}, errs.Wrap(errs.CodeInvalidRequest, err, "tsa: invalid URL")
	}
	hreq.Header.Set("Content-Type", "application/timestamp-query")
	hreq.Header.Set("Accept", "application/timestamp-reply")
	resp, err := client.Do(hreq)
	if err != nil {
		return Token{}, errs.Wrap(errs.CodeUnavailable, err, "tsa: request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Token{}, errs.New(errs.CodeUnavailable, fmt.Sprintf("tsa: HTTP status %d", resp.StatusCode))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxTSAResponse+1))
	if err != nil {
		return Token{}, errs.Wrap(errs.CodeUnavailable, err, "tsa: read the response")
	}
	if len(raw) > maxTSAResponse {
		return Token{}, errs.New(errs.CodeUnavailable, "tsa: response too large")
	}
	ts, err := timestamp.ParseResponse(raw)
	if err != nil {
		return Token{}, errs.Wrap(errs.CodeUnavailable, err, "tsa: invalid or refused response")
	}
	if ts.Nonce == nil || ts.Nonce.Cmp(nonce) != 0 {
		return Token{}, errs.New(errs.CodeUnavailable, "tsa: the response does not answer this request (nonce)")
	}
	if ts.HashAlgorithm != crypto.SHA256 || !bytes.Equal(ts.HashedMessage, digest[:]) {
		return Token{}, errs.New(errs.CodeUnavailable, "tsa: the response timestamps other data")
	}
	if s.Roots != nil {
		if _, err := VerifyToken(ts.RawToken, data, s.Roots); err != nil {
			return Token{}, err
		}
	}
	return Token{DER: ts.RawToken, Time: ts.Time.UTC()}, nil
}

// VerifyToken checks that token is a valid RFC 3161 TimeStampToken over
// SHA-256(data), signed by a timestamping certificate that chains to roots
// at the asserted time, and returns that time.
func VerifyToken(token, data []byte, roots *x509.CertPool) (time.Time, error) {
	if roots == nil {
		return time.Time{}, errs.New(errs.CodeInvalidRequest, "tsa: no trust anchors configured")
	}
	ts, err := timestamp.Parse(token)
	if err != nil {
		return time.Time{}, errs.Wrap(errs.CodeInvalidRequest, err, "tsa: invalid token")
	}
	digest := sha256.Sum256(data)
	if ts.HashAlgorithm != crypto.SHA256 || !bytes.Equal(ts.HashedMessage, digest[:]) {
		return time.Time{}, errs.New(errs.CodeInvalidRequest, "tsa: the token timestamps other data")
	}
	p7, err := pkcs7.Parse(token)
	if err != nil {
		return time.Time{}, errs.Wrap(errs.CodeInvalidRequest, err, "tsa: invalid token")
	}
	if len(p7.Certificates) == 0 {
		return time.Time{}, errs.New(errs.CodeInvalidRequest, "tsa: the token carries no signing certificate")
	}
	intermediates := x509.NewCertPool()
	for _, c := range p7.Certificates {
		intermediates.AddCert(c)
	}
	err = p7.VerifyWithOpts(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   ts.Time,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageTimeStamping},
	})
	if err != nil {
		return time.Time{}, errs.Wrap(errs.CodeInvalidRequest, err, "tsa: token signature or certificate chain invalid")
	}
	return ts.Time.UTC(), nil
}
