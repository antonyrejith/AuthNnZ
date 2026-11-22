// Package openbao is a KMS backed by the OpenBao transit secrets engine, over
// its HTTP API (net/http and encoding/json only).
//
// Keys: PurposeEncrypt keys are transit "aes256-gcm96" keys, PurposeSign keys
// are "ecdsa-p256" keys. Both are created non-exportable and without
// plaintext backup. Signing sends SHA-256(message) with prehashed=true,
// hash_algorithm=sha2-256 and marshaling_algorithm=asn1, so signatures verify
// with ecdsa.VerifyASN1 over SHA-256(message), exactly like package local.
// Associated data is passed as transit "associated_data".
//
// Mount requirements: the transit mount must set disable_upsert=true
// (POST <mount>/config/keys). Otherwise transit's encrypt endpoint silently
// creates a missing key, which would resurrect a destroyed key's name with new
// material. The adapter checks this before its first Encrypt (Check) and fails
// closed. Recommended token policy, with <mount> the transit mount:
//
//	path "<mount>/keys/*"      { capabilities = ["create", "read", "update", "delete"] }
//	path "<mount>/encrypt/*"   { capabilities = ["update"] }  # no "create": no upsert
//	path "<mount>/decrypt/*"   { capabilities = ["update"] }
//	path "<mount>/sign/*"      { capabilities = ["update"] }
//	path "<mount>/config/keys" { capabilities = ["read"] }
//
// Destruction: DestroyKey first creates a tombstone key "<name>.destroyed"
// (names of real keys cannot contain '.'), then sets deletion_allowed and
// deletes the key. The tombstone lets every process tell a destroyed key
// (kms.ErrKeyDestroyed) from one that never existed (kms.ErrKeyNotFound),
// and blocks reuse of the name. It holds no data.
//
// Transport: HTTPS with TLS 1.3 and an optional private CA; plain HTTP only
// with AllowInsecureHTTP (development and tests). Redirects are not followed,
// so the token is never sent to another host. The token is sent only in the
// X-Vault-Token header and never appears in errors. HTTP status codes map to
// errs codes: 400 to invalid request (after checking whether the key exists),
// 401 and 403 to permission denied, 404 to not found, 429 and 5xx and
// connection failures to unavailable.
package openbao

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/kms"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// RefPrefix starts every reference this KMS issues: "openbao:<mount>/<name>".
const RefPrefix = "openbao:"

const (
	typeEncrypt     = "aes256-gcm96"
	typeSign        = "ecdsa-p256"
	tombstoneSuffix = ".destroyed"
	// ciphertextPrefix is transit's ciphertext and signature prefix.
	ciphertextPrefix = "vault:v"
	maxResponseBytes = 1 << 20
	maxErrorDetail   = 512
)

// Config configures the OpenBao KMS. It can be loaded with package config.
type Config struct {
	// Address is the OpenBao base URL, for example "https://openbao:8200".
	Address string `env:"ADDR" required:"true"`
	// Token authenticates to OpenBao.
	Token secret.String `env:"TOKEN" required:"true"`
	// Mount is the transit mount path.
	Mount string `env:"MOUNT" default:"transit"`
	// CACertFile is a PEM file of CA certificates that verify the server.
	// Empty means the system roots.
	CACertFile string `env:"CA_CERT_FILE"`
	// Timeout bounds each HTTP request.
	Timeout time.Duration `env:"TIMEOUT" default:"10s"`
	// AllowInsecureHTTP permits an http:// address. Development and tests
	// only: the token travels in clear text.
	AllowInsecureHTTP bool `env:"ALLOW_INSECURE_HTTP"`
}

// KMS is the OpenBao transit KMS. It implements kms.KMS.
type KMS struct {
	base   string // <address>/v1/<mount>
	mount  string
	token  secret.String
	client *http.Client
	// purposes caches the purpose of each key name; a transit key's type
	// never changes, and destroyed names cannot be reused.
	purposes     sync.Map
	upsertSealed atomic.Bool
}

var _ kms.KMS = (*KMS)(nil)

// New validates cfg and returns a KMS. It does not contact OpenBao; call
// Check to verify connectivity and mount configuration at startup.
func New(cfg Config) (*KMS, error) {
	addr, err := parseAddress(cfg.Address, cfg.AllowInsecureHTTP)
	if err != nil {
		return nil, err
	}
	if err := validateMount(cfg.Mount); err != nil {
		return nil, err
	}
	if cfg.Token.IsEmpty() {
		return nil, errs.New(errs.CodeInvalidRequest, "OpenBao token is empty")
	}
	if cfg.Timeout <= 0 {
		return nil, errs.New(errs.CodeInvalidRequest, "OpenBao timeout must be positive")
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if cfg.CACertFile != "" {
		pem, err := os.ReadFile(filepath.Clean(cfg.CACertFile))
		if err != nil {
			return nil, errs.Wrap(errs.CodeInvalidRequest, err, "read OpenBao CA certificate file")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errs.New(errs.CodeInvalidRequest, "OpenBao CA certificate file contains no PEM certificates")
		}
		tlsCfg.RootCAs = pool
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errs.New(errs.CodeInternal, "unexpected default HTTP transport")
	}
	transport = transport.Clone()
	transport.TLSClientConfig = tlsCfg
	client := &http.Client{
		Transport: transport,
		Timeout:   cfg.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &KMS{
		base:   strings.TrimRight(addr.String(), "/") + "/v1/" + cfg.Mount,
		mount:  cfg.Mount,
		token:  cfg.Token,
		client: client,
	}, nil
}

// Format implements fmt.Formatter so no verb can print the token.
func (k *KMS) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, "openbao.KMS{"+k.mount+", [REDACTED]}")
}

func parseAddress(raw string, allowHTTP bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errs.New(errs.CodeInvalidRequest, "OpenBao address must be an absolute URL")
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && allowHTTP:
	case u.Scheme == "http":
		return nil, errs.New(errs.CodeInvalidRequest, "OpenBao address must use https (http needs AllowInsecureHTTP)")
	default:
		return nil, errs.New(errs.CodeInvalidRequest, "OpenBao address must use https")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errs.New(errs.CodeInvalidRequest, "OpenBao address must be scheme://host[:port] only")
	}
	return u, nil
}

func validateMount(mount string) error {
	if mount == "" || len(mount) > kms.MaxNameLength {
		return errs.New(errs.CodeInvalidRequest, "OpenBao transit mount must be 1 to 128 characters")
	}
	for seg := range strings.SplitSeq(mount, "/") {
		if kms.ValidateName(seg) != nil {
			return errs.New(errs.CodeInvalidRequest, "OpenBao transit mount must be '/'-separated names of letters, digits, '_' and '-'")
		}
	}
	return nil
}

// Check verifies that OpenBao is reachable, the token may read the transit
// mount's configuration, and the mount sets disable_upsert. Encrypt runs it
// once before first use and refuses to encrypt until it passes.
func (k *KMS) Check(ctx context.Context) error {
	var out struct {
		Data struct {
			DisableUpsert bool `json:"disable_upsert"`
		} `json:"data"`
	}
	if err := k.call(ctx, http.MethodGet, "config/keys", nil, &out); err != nil {
		return statusError(err, "read OpenBao transit mount configuration")
	}
	if !out.Data.DisableUpsert {
		return errs.New(errs.CodeInternal, "OpenBao transit mount must set disable_upsert=true")
	}
	k.upsertSealed.Store(true)
	return nil
}

// CreateKey implements kms.KMS.
func (k *KMS) CreateKey(ctx context.Context, name string, purpose kms.Purpose) (kms.KeyRef, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("openbao kms: %w", err)
	}
	if err := kms.ValidateName(name); err != nil {
		return "", wrap(err)
	}
	if err := kms.CheckPurpose(purpose); err != nil {
		return "", wrap(err)
	}
	ref := k.ref(name)
	_, err := k.readKey(ctx, ref, name)
	switch {
	case err == nil:
		return "", errs.New(errs.CodeConflict, "key already exists")
	case errors.Is(err, kms.ErrKeyDestroyed):
		return "", errs.New(errs.CodeConflict, "key name was used by a destroyed key")
	case !errors.Is(err, kms.ErrKeyNotFound):
		return "", err
	}
	want := transitType(purpose)
	var out keyResponse
	body := map[string]any{"type": want, "exportable": false, "allow_plaintext_backup": false}
	if err := k.call(ctx, http.MethodPost, "keys/"+name, body, &out); err != nil {
		return "", statusError(err, "create key")
	}
	// Transit's create is an upsert: if another process created the name
	// first, the response describes that key.
	if out.Data.Type != "" && out.Data.Type != want {
		return "", errs.New(errs.CodeConflict, "key already exists")
	}
	k.purposes.Store(name, purpose)
	return ref, nil
}

// RotateKey implements kms.KMS.
func (k *KMS) RotateKey(ctx context.Context, ref kms.KeyRef) error {
	name, err := k.parseRef(ctx, ref)
	if err != nil {
		return err
	}
	if err := k.call(ctx, http.MethodPost, "keys/"+name+"/rotate", nil, nil); err != nil {
		return k.opError(ctx, ref, name, err, "rotate key")
	}
	return nil
}

// Encrypt implements kms.KMS.
func (k *KMS) Encrypt(ctx context.Context, ref kms.KeyRef, plaintext, aad []byte) ([]byte, error) {
	if err := kms.CheckDataSize(plaintext, aad); err != nil {
		return nil, wrap(err)
	}
	name, err := k.use(ctx, ref, kms.PurposeEncrypt)
	if err != nil {
		return nil, err
	}
	if !k.upsertSealed.Load() {
		if err := k.Check(ctx); err != nil {
			return nil, err
		}
	}
	var out struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	body := map[string]any{"plaintext": plaintext, "associated_data": aad}
	if err := k.call(ctx, http.MethodPost, "encrypt/"+name, body, &out); err != nil {
		return nil, k.opError(ctx, ref, name, err, "encrypt")
	}
	v, raw, err := parseVersioned(out.Data.Ciphertext)
	if err != nil {
		return nil, err
	}
	return kms.EncodeCiphertext(v, raw), nil
}

// Decrypt implements kms.KMS.
func (k *KMS) Decrypt(ctx context.Context, ref kms.KeyRef, ciphertext, aad []byte) ([]byte, error) {
	if err := kms.CheckDataSize(nil, aad); err != nil {
		return nil, wrap(err)
	}
	name, err := k.use(ctx, ref, kms.PurposeEncrypt)
	if err != nil {
		return nil, err
	}
	v, raw, err := kms.DecodeCiphertext(ciphertext)
	if err != nil {
		return nil, wrap(err)
	}
	var out struct {
		Data struct {
			Plaintext []byte `json:"plaintext"`
		} `json:"data"`
	}
	body := map[string]any{"ciphertext": formatVersioned(v, raw), "associated_data": aad}
	if err := k.call(ctx, http.MethodPost, "decrypt/"+name, body, &out); err != nil {
		return nil, k.opError(ctx, ref, name, err, "decrypt")
	}
	if out.Data.Plaintext == nil {
		return []byte{}, nil
	}
	return out.Data.Plaintext, nil
}

// Sign implements kms.KMS.
func (k *KMS) Sign(ctx context.Context, ref kms.KeyRef, message []byte) (kms.Signature, error) {
	name, err := k.use(ctx, ref, kms.PurposeSign)
	if err != nil {
		return kms.Signature{}, err
	}
	var out struct {
		Data struct {
			Signature string `json:"signature"`
		} `json:"data"`
	}
	body := map[string]any{
		"input":                kms.Digest(message),
		"prehashed":            true,
		"hash_algorithm":       "sha2-256",
		"marshaling_algorithm": "asn1",
	}
	if err := k.call(ctx, http.MethodPost, "sign/"+name, body, &out); err != nil {
		return kms.Signature{}, k.opError(ctx, ref, name, err, "sign")
	}
	v, der, err := parseVersioned(out.Data.Signature)
	if err != nil {
		return kms.Signature{}, err
	}
	return kms.Signature{KeyVersion: v, DER: der}, nil
}

// apiError is a non-2xx response.
type apiError struct {
	status int
	detail string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("openbao: HTTP %d: %s", e.status, e.detail)
}

// statusError maps a failed call to an errs error.
func statusError(err error, op string) error {
	var api *apiError
	if !errors.As(err, &api) {
		return err // transport and context errors are already mapped
	}
	switch {
	case api.status == http.StatusBadRequest:
		return errs.Wrap(errs.CodeInvalidRequest, err, "key management service rejected the request: "+op)
	case api.status == http.StatusUnauthorized || api.status == http.StatusForbidden:
		return errs.Wrap(errs.CodePermissionDenied, err, "key management service denied the request: "+op)
	case api.status == http.StatusNotFound:
		return errs.Wrap(errs.CodeNotFound, err, "not found in key management service: "+op)
	case api.status == http.StatusTooManyRequests || api.status >= http.StatusInternalServerError:
		return errs.Wrap(errs.CodeUnavailable, err, "key management service unavailable: "+op)
	default:
		return errs.Wrap(errs.CodeInternal, err, "unexpected key management service response: "+op)
	}
}

// opError maps a failed key operation. Transit answers 400 both for bad input
// and for a missing key, so a 400 is followed by a key lookup.
func (k *KMS) opError(ctx context.Context, ref kms.KeyRef, name string, err error, op string) error {
	var api *apiError
	if errors.As(err, &api) && (api.status == http.StatusBadRequest || api.status == http.StatusNotFound) {
		if _, lookupErr := k.readKey(ctx, ref, name); lookupErr != nil {
			return lookupErr
		}
	}
	return statusError(err, op)
}

// call sends one request. body is encoded as JSON ([]byte as base64); out,
// if not nil, receives the decoded response.
func (k *KMS) call(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return errs.Wrap(errs.CodeInternal, err, "encode key management request")
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, k.base+"/"+path, reader)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "build key management request")
	}
	req.Header.Set("X-Vault-Token", k.token.Reveal())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := k.client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("openbao kms: %w", ctxErr)
		}
		return errs.Wrap(errs.CodeUnavailable, err, "key management service unreachable")
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("openbao kms: %w", ctxErr)
		}
		return errs.Wrap(errs.CodeUnavailable, err, "read key management response")
	}
	if len(data) > maxResponseBytes {
		return errs.New(errs.CodeInternal, "key management response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &apiError{status: resp.StatusCode, detail: errorDetail(data, k.token)}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "decode key management response")
	}
	return nil
}

// errorDetail extracts OpenBao's error messages for internal logs, removing
// the token in case a server or proxy echoes it.
func errorDetail(data []byte, token secret.String) string {
	var e struct {
		Errors []string `json:"errors"`
	}
	detail := "no detail"
	if json.Unmarshal(data, &e) == nil && len(e.Errors) > 0 {
		detail = strings.Join(e.Errors, "; ")
	}
	if t := token.Reveal(); t != "" {
		detail = strings.ReplaceAll(detail, t, "[REDACTED]")
	}
	if len(detail) > maxErrorDetail {
		detail = detail[:maxErrorDetail]
	}
	return detail
}

func transitType(p kms.Purpose) string {
	switch p {
	case kms.PurposeEncrypt:
		return typeEncrypt
	case kms.PurposeSign:
		return typeSign
	default:
		return ""
	}
}
