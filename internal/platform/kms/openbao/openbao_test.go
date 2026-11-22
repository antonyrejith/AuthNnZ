package openbao_test

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/kms"
	"github.com/antonyrejith/portun/internal/platform/kms/openbao"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

const testToken = "s.unit-test-token-value"

func config(addr string) openbao.Config {
	return openbao.Config{Address: addr, Token: secret.NewString(testToken), Mount: "transit", Timeout: 2 * time.Second, AllowInsecureHTTP: true}
}

// TestConfigValidation checks insecure or malformed settings are refused.
//
// Covers: SEC-08, SEC-09
func TestConfigValidation(t *testing.T) {
	t.Parallel()
	good := config("http://127.0.0.1:8200")
	cases := []struct {
		name string
		edit func(*openbao.Config)
		ok   bool
	}{
		{"valid", func(*openbao.Config) {}, true},
		{"https", func(c *openbao.Config) { c.Address, c.AllowInsecureHTTP = "https://bao.example:8200", false }, true},
		{"nested mount", func(c *openbao.Config) { c.Mount = "portun/transit" }, true},
		{"http without opt-in", func(c *openbao.Config) { c.AllowInsecureHTTP = false }, false},
		{"other scheme", func(c *openbao.Config) { c.Address = "ftp://bao" }, false},
		{"relative", func(c *openbao.Config) { c.Address = "bao:8200" }, false},
		{"path", func(c *openbao.Config) { c.Address = "http://bao/v1" }, false},
		{"userinfo", func(c *openbao.Config) { c.Address = "http://u:p@bao" }, false},
		{"query", func(c *openbao.Config) { c.Address = "http://bao?x=1" }, false},
		{"empty token", func(c *openbao.Config) { c.Token = secret.String{} }, false},
		{"empty mount", func(c *openbao.Config) { c.Mount = "" }, false},
		{"mount traversal", func(c *openbao.Config) { c.Mount = "../sys" }, false},
		{"mount slash", func(c *openbao.Config) { c.Mount = "transit/" }, false},
		{"zero timeout", func(c *openbao.Config) { c.Timeout = 0 }, false},
		{"missing CA file", func(c *openbao.Config) { c.CACertFile = filepath.Join(t.TempDir(), "none.pem") }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cfg := good
			c.edit(&cfg)
			_, err := openbao.New(cfg)
			if c.ok != (err == nil) {
				t.Fatalf("New: %v, want ok=%v", err, c.ok)
			}
			if err != nil && strings.Contains(err.Error(), testToken) {
				t.Fatal("error contains the token")
			}
		})
	}
	bad := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(bad, []byte("not pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := good
	cfg.CACertFile = bad
	if _, err := openbao.New(cfg); errs.CodeOf(err) != errs.CodeInvalidRequest {
		t.Fatalf("New with a non-PEM CA file: %v", err)
	}
}

// TestStatusMapping checks HTTP failures map to errs codes and never echo the
// token.
//
// Covers: SEC-08
func TestStatusMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status int
		code   errs.Code
	}{
		{http.StatusUnauthorized, errs.CodePermissionDenied},
		{http.StatusForbidden, errs.CodePermissionDenied},
		{http.StatusTooManyRequests, errs.CodeUnavailable},
		{http.StatusInternalServerError, errs.CodeUnavailable},
		{http.StatusServiceUnavailable, errs.CodeUnavailable},
		{http.StatusTeapot, errs.CodeInternal},
		{http.StatusFound, errs.CodeInternal},
	}
	for _, c := range cases {
		t.Run(http.StatusText(c.status), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Vault-Token") != testToken {
					t.Errorf("token header missing")
				}
				if c.status == http.StatusFound {
					http.Redirect(w, r, "http://elsewhere.invalid/", c.status)
					return
				}
				w.WriteHeader(c.status)
				_, _ = fmt.Fprintf(w, `{"errors":["denied for %s"]}`, r.Header.Get("X-Vault-Token"))
			}))
			t.Cleanup(srv.Close)
			k, err := openbao.New(config(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			_, err = k.CreateKey(t.Context(), "k", kms.PurposeEncrypt)
			if errs.CodeOf(err) != c.code {
				t.Fatalf("code %s, want %s (%v)", errs.CodeOf(err), c.code, err)
			}
			if strings.Contains(err.Error(), testToken) {
				// The server echoes the token; it must not reach errors.
				t.Fatalf("error contains the token: %v", err)
			}
		})
	}
}

// TestNoRedirects checks the client does not follow redirects, so the token
// is never sent to another host.
//
// Covers: SEC-08
func TestNoRedirects(t *testing.T) {
	t.Parallel()
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "" {
			leaked.Store(true)
		}
	}))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	k, err := openbao.New(config(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Check(t.Context()); err == nil {
		t.Fatal("Check must fail on a redirect")
	}
	if leaked.Load() {
		t.Fatal("token was sent to the redirect target")
	}
}

func TestUnreachable(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	k, err := openbao.New(config(addr))
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Check(t.Context()); errs.CodeOf(err) != errs.CodeUnavailable {
		t.Fatalf("Check against a closed port: %v", err)
	}
}

// TestTLS checks HTTPS with a private CA works and that TLS 1.2 is refused.
//
// Covers: SEC-08, SEC-09
func TestTLS(t *testing.T) {
	t.Parallel()
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"disable_upsert":true}}`))
	})
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(ca, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config(srv.URL)
	cfg.AllowInsecureHTTP, cfg.CACertFile = false, ca
	k, err := openbao.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Check(t.Context()); err != nil {
		t.Fatalf("Check over TLS with the private CA: %v", err)
	}

	cfg.CACertFile = ""
	untrusted, err := openbao.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := untrusted.Check(t.Context()); errs.CodeOf(err) != errs.CodeUnavailable {
		t.Fatalf("Check with an untrusted certificate: %v", err)
	}

	old := httptest.NewUnstartedServer(handler)
	old.TLS = srv.TLS.Clone()
	old.TLS.MaxVersion = 0x0303 // TLS 1.2
	old.StartTLS()
	t.Cleanup(old.Close)
	cfg.Address, cfg.CACertFile = old.URL, filepath.Join(t.TempDir(), "old.pem")
	if err := os.WriteFile(cfg.CACertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: old.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	k12, err := openbao.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := k12.Check(t.Context()); errs.CodeOf(err) != errs.CodeUnavailable {
		t.Fatalf("Check against a TLS 1.2 server: %v, want refusal", err)
	}
}

// TestCheckRequiresDisableUpsert checks Check fails closed when the mount
// allows upsert.
//
// Covers: SEC-08
func TestCheckRequiresDisableUpsert(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"disable_upsert":false}}`))
	}))
	t.Cleanup(srv.Close)
	k, err := openbao.New(config(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Check(t.Context()); errs.CodeOf(err) != errs.CodeInternal {
		t.Fatalf("Check: %v, want refusal", err)
	}
}

// TestFormatRedacts checks no fmt verb prints the token.
//
// Covers: SEC-08
func TestFormatRedacts(t *testing.T) {
	t.Parallel()
	k, err := openbao.New(config("http://127.0.0.1:1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%d", "%x"} {
		if got := fmt.Sprintf(verb, k); strings.Contains(got, testToken) || !strings.Contains(got, "REDACTED") {
			t.Errorf("%s printed %q", verb, got)
		}
	}
}

// TestMalformedResponses checks unexpected OpenBao answers fail cleanly
// instead of being trusted.
func TestMalformedResponses(t *testing.T) {
	t.Parallel()
	const ecKey = `{"data":{"type":"ecdsa-p256","latest_version":1,"keys":{"1":{"public_key":%q}}}}`
	const aesKey = `{"data":{"type":"aes256-gcm96"}}`
	cases := []struct {
		name   string
		key    string // GET keys/k response
		op     string // response to every other request
		status int
		call   func(*openbao.KMS, kms.KeyRef) error
		code   errs.Code
	}{
		{"unsupported key type", `{"data":{"type":"rsa-2048"}}`, "", 200, encryptCall, errs.CodeInvalidRequest},
		{"ciphertext without prefix", aesKey, `{"data":{"ciphertext":"bogus"}}`, 200, encryptCall, errs.CodeInternal},
		{"ciphertext bad version", aesKey, `{"data":{"ciphertext":"vault:vX:AAAA"}}`, 200, encryptCall, errs.CodeInternal},
		{"ciphertext bad base64", aesKey, `{"data":{"ciphertext":"vault:v1:!!"}}`, 200, encryptCall, errs.CodeInternal},
		{"signature bad version", `{"data":{"type":"ecdsa-p256"}}`, `{"data":{"signature":"vault:v0:AAAA"}}`, 200, signCall, errs.CodeInternal},
		{"invalid json", aesKey, `{"data":`, 200, encryptCall, errs.CodeInternal},
		{"public key not pem", fmt.Sprintf(ecKey, "nope"), "", 200, publicKeysCall, errs.CodeInternal},
		{"public key bad version", `{"data":{"type":"ecdsa-p256","latest_version":1,"keys":{"x":{}}}}`, "", 200, publicKeysCall, errs.CodeInternal},
		{"public key garbage", fmt.Sprintf(ecKey, "-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n"), "", 200, publicKeysCall, errs.CodeInternal},
		{"rejected input", aesKey, `{"errors":["bad input"]}`, 400, encryptCall, errs.CodeInvalidRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v1/transit/config/keys":
					_, _ = w.Write([]byte(`{"data":{"disable_upsert":true}}`))
				case r.Method == http.MethodGet && r.URL.Path == "/v1/transit/keys/k":
					_, _ = w.Write([]byte(c.key))
				default:
					w.WriteHeader(c.status)
					_, _ = w.Write([]byte(c.op))
				}
			}))
			t.Cleanup(srv.Close)
			k, err := openbao.New(config(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			if err := c.call(k, "openbao:transit/k"); errs.CodeOf(err) != c.code {
				t.Fatalf("got %v, want %s", err, c.code)
			}
		})
	}
}

func encryptCall(k *openbao.KMS, ref kms.KeyRef) error {
	_, err := k.Encrypt(context.Background(), ref, []byte("x"), nil)
	return err
}

func signCall(k *openbao.KMS, ref kms.KeyRef) error {
	_, err := k.Sign(context.Background(), ref, []byte("x"))
	return err
}

func publicKeysCall(k *openbao.KMS, ref kms.KeyRef) error {
	_, err := k.PublicKeys(context.Background(), ref)
	return err
}

func TestResponseTooLarge(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, 2<<20))
	}))
	t.Cleanup(srv.Close)
	k, err := openbao.New(config(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Check(t.Context()); errs.CodeOf(err) != errs.CodeInternal {
		t.Fatalf("Check with a 2 MiB response: %v", err)
	}
}
