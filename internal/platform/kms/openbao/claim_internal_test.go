package openbao

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/kms"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// TestEncryptRechecksDisableUpsert proves that Encrypt re-checks the
// mount's disable_upsert on a key-cache miss and once the last check is older
// than UpsertRecheckInterval, and refuses to encrypt once upsert is enabled.
//
// Covers: SEC-08
func TestEncryptRechecksDisableUpsert(t *testing.T) {
	t.Parallel()
	var upsertDisabled atomic.Bool
	var checks atomic.Int32
	upsertDisabled.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/transit/config/keys":
			checks.Add(1)
			if upsertDisabled.Load() {
				_, _ = w.Write([]byte(`{"data":{"disable_upsert":true}}`))
			} else {
				_, _ = w.Write([]byte(`{"data":{"disable_upsert":false}}`))
			}
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":{"type":"aes256-gcm96"}}`))
		default:
			_, _ = w.Write([]byte(`{"data":{"ciphertext":"vault:v1:AAAA"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	k, err := New(Config{Address: srv.URL, Token: secret.NewString("tok-0123456789"), Mount: "transit", Timeout: 2 * time.Second, AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	encrypt := func(name string) error {
		_, err := k.Encrypt(t.Context(), kms.KeyRef("openbao:transit/"+name), []byte("x"), nil)
		return err
	}
	if err := encrypt("a"); err != nil {
		t.Fatal(err)
	}
	if err := encrypt("a"); err != nil || checks.Load() != 1 {
		t.Fatalf("cached key within the interval: err %v, checks %d; want no new check", err, checks.Load())
	}
	upsertDisabled.Store(false)
	if err := encrypt("b"); errs.CodeOf(err) != errs.CodeInternal {
		t.Fatalf("cache miss after upsert was enabled: %v, want refusal", err)
	}
	if err := encrypt("a"); errs.CodeOf(err) != errs.CodeInternal {
		t.Fatalf("after a failed check: %v, want refusal", err)
	}
	upsertDisabled.Store(true)
	if err := encrypt("a"); err != nil {
		t.Fatalf("after the mount was fixed: %v", err)
	}
	// A stale check is repeated even for a cached key.
	upsertDisabled.Store(false)
	k.upsertCheckedAt.Store(time.Now().Add(-UpsertRecheckInterval - time.Second).UnixNano())
	if err := encrypt("a"); errs.CodeOf(err) != errs.CodeInternal {
		t.Fatalf("stale check: %v, want refusal", err)
	}
}
