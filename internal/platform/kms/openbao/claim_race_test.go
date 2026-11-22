package openbao_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/kms"
	"github.com/antonyrejith/portun/internal/platform/kms/openbao"
)

// claimFake is a fake OpenBao transit server for CreateKey: the key "k" does
// not exist yet, and the rotation and read of its claim key "k.created"
// answer as configured.
type claimFake struct {
	rotate    func(n int32, w http.ResponseWriter) // answers the n-th rotation (1-based)
	claimGet  func(w http.ResponseWriter)          // answers a GET of the claim key
	rotations atomic.Int32
}

func (f *claimFake) start(t *testing.T) *openbao.KMS {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := strings.TrimPrefix(r.URL.Path, "/v1/transit/"); {
		case r.Method == http.MethodGet && p == "keys/k.created":
			f.claimGet(w)
		case r.Method == http.MethodGet:
			http.Error(w, `{"errors":[]}`, http.StatusNotFound) // key and tombstone
		case p == "keys/k.created/rotate":
			f.rotate(f.rotations.Add(1), w)
		default:
			_, _ = w.Write([]byte(`{"data":{"type":"aes256-gcm96"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	k, err := openbao.New(config(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func version(v int) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { _, _ = fmt.Fprintf(w, `{"data":{"latest_version":%d}}`, v) }
}

func rotateFails(w http.ResponseWriter) {
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(`{"errors":["` + strings.Repeat("x", 2000) + `"]}`))
}

// TestCreateKeyClaim forces each outcome of the claim rotation that
// concurrent creators race over, including the race-loser and retry paths.
//
// Covers: SEC-08
func TestCreateKeyClaim(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		fake      *claimFake
		code      errs.Code // zero means success
		rotations int32
	}{
		{"winner", &claimFake{rotate: func(_ int32, w http.ResponseWriter) { version(2)(w) }}, "", 1},
		{"loser sees newer version", &claimFake{rotate: func(_ int32, w http.ResponseWriter) { version(3)(w) }}, errs.CodeConflict, 1},
		{"no version reported", &claimFake{rotate: func(_ int32, w http.ResponseWriter) { version(0)(w) }}, errs.CodeInternal, 1},
		{"rotation raced and claim moved on", &claimFake{rotate: func(_ int32, w http.ResponseWriter) { rotateFails(w) }, claimGet: version(2)}, errs.CodeConflict, 1},
		{"rotation retried then wins", &claimFake{rotate: func(n int32, w http.ResponseWriter) {
			if n == 1 {
				rotateFails(w)
				return
			}
			version(2)(w)
		}, claimGet: version(1)}, "", 2},
		{"rotation keeps failing", &claimFake{rotate: func(_ int32, w http.ResponseWriter) { rotateFails(w) }, claimGet: version(1)}, errs.CodeUnavailable, 5},
		{"claim unreadable while rotation fails", &claimFake{rotate: func(_ int32, w http.ResponseWriter) { rotateFails(w) }, claimGet: func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) }}, errs.CodeUnavailable, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			k := c.fake.start(t)
			ref, err := k.CreateKey(t.Context(), "k", kms.PurposeEncrypt)
			if c.code == "" {
				if err != nil || ref != "openbao:transit/k" {
					t.Fatalf("CreateKey = %q, %v; want success", ref, err)
				}
			} else if errs.CodeOf(err) != c.code {
				t.Fatalf("CreateKey: %v, want code %s", err, c.code)
			}
			if got := c.fake.rotations.Load(); got != c.rotations {
				t.Fatalf("rotations = %d, want %d", got, c.rotations)
			}
			if err != nil && len(err.Error()) > 1024 {
				t.Fatalf("error carries an unbounded server message (%d bytes)", len(err.Error()))
			}
		})
	}
}

// TestCreateKeyClaimCanceled checks a caller cancelled while its claim
// rotation is being retried stops with the context error.
//
// Covers: SEC-08
func TestCreateKeyClaimCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f := &claimFake{
		rotate: func(_ int32, w http.ResponseWriter) {
			cancel()
			rotateFails(w)
		},
		claimGet: version(1),
	}
	k := f.start(t)
	_, err := k.CreateKey(ctx, "k", kms.PurposeEncrypt)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateKey: %v, want context.Canceled", err)
	}
}

// TestCreateKeyPurposes checks the transit key type requested for each
// purpose.
//
// Covers: SEC-08
func TestCreateKeyPurposes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		purpose kms.Purpose
		want    string
	}{
		{"encrypt", kms.PurposeEncrypt, "aes256-gcm96"},
		{"sign", kms.PurposeSign, "ecdsa-p256"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var created atomic.Value
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet:
					http.Error(w, `{"errors":[]}`, http.StatusNotFound)
				case r.URL.Path == "/v1/transit/keys/k":
					b, _ := io.ReadAll(r.Body)
					created.Store(string(b))
					_, _ = w.Write([]byte(`{"data":{}}`))
				default:
					_, _ = w.Write([]byte(`{"data":{"latest_version":2}}`))
				}
			}))
			t.Cleanup(srv.Close)
			k, err := openbao.New(config(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := k.CreateKey(t.Context(), "k", c.purpose); err != nil {
				t.Fatal(err)
			}
			if got, _ := created.Load().(string); !strings.Contains(got, `"type":"`+c.want+`"`) {
				t.Fatalf("create body %q, want type %s", got, c.want)
			}
		})
	}
}
