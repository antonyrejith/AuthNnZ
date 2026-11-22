// Package tsatest runs a local RFC 3161 timestamp authority for tests: an
// HTTP server with its own root CA and a timestamping certificate.
package tsatest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/digitorus/timestamp"
)

// TSA is a running test timestamp authority.
type TSA struct {
	URL   string
	Roots *x509.CertPool
	// Root is the self-signed trust anchor.
	Root *x509.Certificate
	// Fail makes the server answer HTTP 503 while set.
	Fail atomic.Bool
	// Requests counts answered requests.
	Requests atomic.Int64
}

var policyOID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1} //nolint:gochecknoglobals // test policy OID

// Start starts a TSA that is stopped when the test ends.
func Start(tb testing.TB) *TSA {
	tb.Helper()
	rootKey, root := mustCA(tb)
	leafKey, leaf := mustLeaf(tb, root, rootKey)
	t := &TSA{Root: root, Roots: x509.NewCertPool()}
	t.Roots.AddCert(root)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if t.Fail.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		req, err := timestamp.ParseRequest(body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		ts := timestamp.Timestamp{
			HashAlgorithm:     req.HashAlgorithm,
			HashedMessage:     req.HashedMessage,
			Time:              time.Now().UTC().Truncate(time.Second),
			Nonce:             req.Nonce,
			Policy:            policyOID,
			AddTSACertificate: true,
		}
		resp, err := ts.CreateResponseWithOpts(leaf, leafKey, crypto.SHA256)
		if err != nil {
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		t.Requests.Add(1)
		w.Header().Set("Content-Type", "application/timestamp-reply")
		_, _ = w.Write(resp)
	}))
	tb.Cleanup(srv.Close)
	t.URL = srv.URL
	return t
}

func mustCA(tb testing.TB) (*ecdsa.PrivateKey, *x509.Certificate) {
	tb.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Portun test TSA root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		tb.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		tb.Fatal(err)
	}
	return key, cert
}

func mustLeaf(tb testing.TB, root *x509.Certificate, rootKey *ecdsa.PrivateKey) (*ecdsa.PrivateKey, *x509.Certificate) {
	tb.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "Portun test TSA"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageTimeStamping},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, root, &key.PublicKey, rootKey)
	if err != nil {
		tb.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		tb.Fatal(err)
	}
	return key, cert
}
