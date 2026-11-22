package verify

import (
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"strconv"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// PEM headers of a trusted key block ("PUBLIC KEY").
const (
	HeaderKeyRef     = "Key-Ref"
	HeaderKeyVersion = "Key-Version"
)

// ParseKeysPEM reads trusted keys: "PUBLIC KEY" blocks (PKIX) with the
// Key-Ref and Key-Version headers.
func ParseKeysPEM(data []byte) (Keys, error) {
	keys := Keys{}
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			break
		}
		if b.Type != "PUBLIC KEY" {
			continue
		}
		ref := b.Headers[HeaderKeyRef]
		version, err := strconv.Atoi(b.Headers[HeaderKeyVersion])
		if ref == "" || err != nil || version < 1 {
			return nil, errs.New(errs.CodeInvalidRequest, "trusted keys: each block needs "+HeaderKeyRef+" and "+HeaderKeyVersion+" headers")
		}
		pub, err := x509.ParsePKIXPublicKey(b.Bytes)
		if err != nil {
			return nil, errs.Wrap(errs.CodeInvalidRequest, err, "trusted keys: invalid public key")
		}
		if keys[ref] == nil {
			keys[ref] = map[int]crypto.PublicKey{}
		}
		keys[ref][version] = pub
	}
	if len(keys) == 0 {
		return nil, errs.New(errs.CodeInvalidRequest, "trusted keys: no PUBLIC KEY blocks found")
	}
	return keys, nil
}

// KeysPEM renders signing keys in the format ParseKeysPEM reads.
func KeysPEM(ks []store.SigningKey) []byte {
	var out []byte
	for _, k := range ks {
		out = append(out, pem.EncodeToMemory(&pem.Block{
			Type:    "PUBLIC KEY",
			Headers: map[string]string{HeaderKeyRef: k.KeyRef, HeaderKeyVersion: strconv.Itoa(k.Version)},
			Bytes:   k.PublicKey,
		})...)
	}
	return out
}
