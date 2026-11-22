package openbao

import (
	"context"
	stdcrypto "crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/kms"
)

type keyResponse struct {
	Data keyData `json:"data"`
}

type keyData struct {
	Type          string                     `json:"type"`
	LatestVersion int                        `json:"latest_version"`
	Keys          map[string]json.RawMessage `json:"keys"`
}

// PublicKey implements kms.KMS.
func (k *KMS) PublicKey(ctx context.Context, ref kms.KeyRef) (stdcrypto.PublicKey, error) {
	keys, latest, err := k.publicKeys(ctx, ref)
	if err != nil {
		return nil, err
	}
	pub, ok := keys[latest]
	if !ok {
		return nil, errs.New(errs.CodeInternal, "OpenBao returned no public key for the latest version")
	}
	return pub, nil
}

// PublicKeys implements kms.KMS.
func (k *KMS) PublicKeys(ctx context.Context, ref kms.KeyRef) (map[int]stdcrypto.PublicKey, error) {
	keys, _, err := k.publicKeys(ctx, ref)
	return keys, err
}

func (k *KMS) publicKeys(ctx context.Context, ref kms.KeyRef) (map[int]stdcrypto.PublicKey, int, error) {
	name, err := k.use(ctx, ref, kms.PurposeSign)
	if err != nil {
		return nil, 0, err
	}
	data, err := k.readKey(ctx, ref, name)
	if err != nil {
		return nil, 0, err
	}
	if data.Type != typeSign {
		return nil, 0, wrap(kms.PurposeError(ref, purposeOf(data.Type), kms.PurposeSign))
	}
	out := make(map[int]stdcrypto.PublicKey, len(data.Keys))
	for vs, raw := range data.Keys {
		v, err := strconv.Atoi(vs)
		if err != nil || kms.CheckVersion(v) != nil {
			return nil, 0, errs.New(errs.CodeInternal, "OpenBao returned an invalid key version")
		}
		pub, err := parsePublicKey(raw)
		if err != nil {
			return nil, 0, err
		}
		out[v] = pub
	}
	return out, data.LatestVersion, nil
}

func parsePublicKey(raw json.RawMessage) (*ecdsa.PublicKey, error) {
	var entry struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "decode OpenBao public key")
	}
	block, _ := pem.Decode([]byte(entry.PublicKey))
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, errs.New(errs.CodeInternal, "OpenBao public key is not a PEM public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "parse OpenBao public key")
	}
	pub, ok := parsed.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, errs.New(errs.CodeInternal, "OpenBao public key is not ECDSA P-256")
	}
	return pub, nil
}

// DestroyKey implements kms.KMS.
func (k *KMS) DestroyKey(ctx context.Context, ref kms.KeyRef) error {
	name, err := k.parseRef(ctx, ref)
	if err != nil {
		return err
	}
	if _, err := k.readKey(ctx, ref, name); err != nil {
		if errors.Is(err, kms.ErrKeyDestroyed) {
			return nil
		}
		return err
	}
	// The tombstone comes first: if a later step fails, the key still exists
	// and DestroyKey can be retried; once the key is gone the tombstone is
	// already in place.
	tomb := map[string]any{"type": typeEncrypt, "exportable": false, "allow_plaintext_backup": false}
	if err := k.call(ctx, http.MethodPost, "keys/"+name+tombstoneSuffix, tomb, nil); err != nil {
		return statusError(err, "record key destruction")
	}
	steps := []struct {
		method, path string
		body         any
		op           string
	}{
		{http.MethodPost, "keys/" + name + "/config", map[string]any{"deletion_allowed": true}, "allow key deletion"},
		{http.MethodDelete, "keys/" + name, nil, "delete key"},
	}
	for _, s := range steps {
		if err := k.call(ctx, s.method, s.path, s.body, nil); err != nil {
			mapped := k.opError(ctx, ref, name, err, s.op)
			if errors.Is(mapped, kms.ErrKeyDestroyed) {
				break // destroyed concurrently
			}
			return mapped
		}
	}
	k.purposes.Delete(name)
	return nil
}

func (k *KMS) ref(name string) kms.KeyRef {
	return kms.KeyRef(RefPrefix + k.mount + "/" + name)
}

// parseRef checks the context and returns the key name of a reference to this
// KMS's mount.
func (k *KMS) parseRef(ctx context.Context, ref kms.KeyRef) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("openbao kms: %w", err)
	}
	rest, ok := strings.CutPrefix(string(ref), RefPrefix)
	if !ok {
		return "", wrap(kms.InvalidRefError(ref, "not an OpenBao reference"))
	}
	i := strings.LastIndexByte(rest, '/')
	if i < 0 || rest[:i] != k.mount {
		return "", wrap(kms.InvalidRefError(ref, "reference is not for mount "+k.mount))
	}
	name := rest[i+1:]
	if err := kms.ValidateName(name); err != nil {
		return "", wrap(kms.InvalidRefError(ref, "invalid key name"))
	}
	return name, nil
}

// use resolves ref to the name of an existing key with the wanted purpose.
func (k *KMS) use(ctx context.Context, ref kms.KeyRef, want kms.Purpose) (string, error) {
	name, _, err := k.useCached(ctx, ref, want)
	return name, err
}

// useCached is use that also reports whether the key's purpose came from the
// cache (false: it was read from OpenBao).
func (k *KMS) useCached(ctx context.Context, ref kms.KeyRef, want kms.Purpose) (string, bool, error) {
	name, err := k.parseRef(ctx, ref)
	if err != nil {
		return "", false, err
	}
	have, cached := k.purposes.Load(name)
	if !cached {
		data, err := k.readKey(ctx, ref, name)
		if err != nil {
			return "", false, err
		}
		p := purposeOf(data.Type)
		if !p.Valid() {
			return "", false, errs.New(errs.CodeInvalidRequest, "OpenBao key type "+data.Type+" is not supported")
		}
		k.purposes.Store(name, p)
		have = p
	}
	if have != want {
		p, _ := have.(kms.Purpose)
		return "", false, wrap(kms.PurposeError(ref, p, want))
	}
	return name, cached, nil
}

// readKey reads a key's metadata. A missing key is reported as destroyed if
// its tombstone exists and as not found otherwise.
func (k *KMS) readKey(ctx context.Context, ref kms.KeyRef, name string) (keyData, error) {
	var out keyResponse
	err := k.call(ctx, http.MethodGet, "keys/"+name, nil, &out)
	var api *apiError
	switch {
	case err == nil:
		return out.Data, nil
	case !errors.As(err, &api) || api.status != http.StatusNotFound:
		return keyData{}, statusError(err, "read key")
	}
	k.purposes.Delete(name)
	err = k.call(ctx, http.MethodGet, "keys/"+name+tombstoneSuffix, nil, nil)
	switch {
	case err == nil:
		return keyData{}, wrap(kms.DestroyedError(ref))
	case errors.As(err, &api) && api.status == http.StatusNotFound:
		return keyData{}, wrap(kms.NotFoundError(ref))
	default:
		return keyData{}, statusError(err, "read key")
	}
}

func purposeOf(transitType string) kms.Purpose {
	switch transitType {
	case typeEncrypt:
		return kms.PurposeEncrypt
	case typeSign:
		return kms.PurposeSign
	default:
		return 0
	}
}

// parseVersioned parses transit's "vault:v<N>:<base64>" ciphertexts and
// signatures.
func parseVersioned(s string) (int, []byte, error) {
	rest, ok := strings.CutPrefix(s, ciphertextPrefix)
	if !ok {
		return 0, nil, errs.New(errs.CodeInternal, "unexpected OpenBao ciphertext format")
	}
	vs, b64, ok := strings.Cut(rest, ":")
	v, err := strconv.Atoi(vs)
	if !ok || err != nil || kms.CheckVersion(v) != nil {
		return 0, nil, errs.New(errs.CodeInternal, "unexpected OpenBao ciphertext version")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) == 0 {
		return 0, nil, errs.New(errs.CodeInternal, "unexpected OpenBao ciphertext encoding")
	}
	return v, raw, nil
}

func formatVersioned(v int, raw []byte) string {
	return ciphertextPrefix + strconv.Itoa(v) + ":" + base64.StdEncoding.EncodeToString(raw)
}

// wrap adds this package's context; errs codes and sentinels stay reachable.
func wrap(err error) error {
	return fmt.Errorf("openbao kms: %w", err)
}
