package apimw_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/antonyrejith/portun/internal/api/apimw"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

const testToken = "operator-token-for-tests-0123456789"

func testDigest() string {
	sum := sha256.Sum256([]byte(testToken))
	return hex.EncodeToString(sum[:])
}

// TestAuthenticator: only the one configured bearer token is accepted; a
// missing, malformed, wrong or repeated credential is unauthenticated, an
// unconfigured server rejects everything, and the response says the same in
// every case.
//
// Covers: SEC-09
func TestAuthenticator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		digest  string
		headers []string // Authorization header values
		want    bool
	}{
		{"valid", testDigest(), []string{"Bearer " + testToken}, true},
		{"scheme is case insensitive", testDigest(), []string{"bearer " + testToken}, true},
		{"digest in upper case", strings.ToUpper(testDigest()), []string{"Bearer " + testToken}, true},
		{"missing header", testDigest(), nil, false},
		{"wrong token", testDigest(), []string{"Bearer " + testToken + "x"}, false},
		{"prefix of the token", testDigest(), []string{"Bearer " + testToken[:10]}, false},
		{"basic scheme", testDigest(), []string{"Basic " + testToken}, false},
		{"no scheme", testDigest(), []string{testToken}, false},
		{"empty token", testDigest(), []string{"Bearer "}, false},
		{"token with a space", testDigest(), []string{"Bearer " + testToken + " extra"}, false},
		{"two headers", testDigest(), []string{"Bearer " + testToken, "Bearer " + testToken}, false},
		{"huge token", testDigest(), []string{"Bearer " + strings.Repeat("a", 5000)}, false},
		{"not configured", "", []string{"Bearer " + testToken}, false},
		{"not configured, empty token", "", []string{"Bearer "}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := apimw.NewAuthenticator(apimw.AuthConfig{OperatorTokenSHA256: tt.digest})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil)
			for _, h := range tt.headers {
				r.Header.Add("Authorization", h)
			}
			actor, err := a.Authenticate(r)
			if tt.want {
				if err != nil || actor.Type != scope.ActorPlatformOperator || actor.ID != apimw.OperatorActorID {
					t.Fatalf("Authenticate() = %+v, %v", actor, err)
				}
				return
			}
			if !errs.Is(err, errs.CodeUnauthenticated) || strings.Contains(err.Error(), testToken) {
				t.Fatalf("Authenticate() = %+v, %v; want unauthenticated without the token", actor, err)
			}
		})
	}
}

// TestNewAuthenticatorRejectsBadDigest: a digest that is not a SHA-256 in hex
// is a configuration error, not a server that rejects everything.
func TestNewAuthenticatorRejectsBadDigest(t *testing.T) {
	t.Parallel()
	for _, d := range []string{"abc", strings.Repeat("z", 64), testDigest() + "00"} {
		if _, err := apimw.NewAuthenticator(apimw.AuthConfig{OperatorTokenSHA256: d}); !errs.Is(err, errs.CodeInvalidRequest) {
			t.Errorf("digest %q: error = %v", d, err)
		}
	}
}

// TestAuthenticatorMiddleware: a valid token reaches the handler with the
// operator actor; every failure is a 401 problem with WWW-Authenticate and
// the handler never runs.
//
// Covers: SEC-09
func TestAuthenticatorMiddleware(t *testing.T) {
	t.Parallel()
	a, err := apimw.NewAuthenticator(apimw.AuthConfig{OperatorTokenSHA256: testDigest()})
	if err != nil {
		t.Fatal(err)
	}
	var seen scope.Actor
	called := false
	h := a.Middleware()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		seen, _ = scope.ActorFrom(r.Context())
	}))

	rec := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil)
	r.Header.Set("Authorization", "Bearer wrong")
	h.ServeHTTP(rec, r)
	if called || rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "Bearer" ||
		rec.Header().Get("Content-Type") != "application/problem+json" || strings.Contains(rec.Body.String(), "wrong") {
		t.Fatalf("rejected request: called %v, %d, headers %v, body %s", called, rec.Code, rec.Header(), rec.Body)
	}

	rec = httptest.NewRecorder()
	r = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	h.ServeHTTP(rec, r)
	if !called || rec.Code != http.StatusOK || seen.Type != scope.ActorPlatformOperator {
		t.Fatalf("accepted request: called %v, %d, actor %+v", called, rec.Code, seen)
	}
}

// TestOperatorAuthorizer: the B0 authorizer grants every permission to the
// platform operator and none to any other actor.
//
// Covers: SEC-09
func TestOperatorAuthorizer(t *testing.T) {
	t.Parallel()
	var z apimw.OperatorAuthorizer
	ctx := context.Background()
	tests := []struct {
		name  string
		actor scope.Actor
		perm  string
		want  bool
	}{
		{"operator", scope.Actor{Type: scope.ActorPlatformOperator, ID: "x"}, "platform.tenants.write", true},
		{"operator, no permission named", scope.Actor{Type: scope.ActorPlatformOperator, ID: "x"}, "", false},
		{"admin", scope.Actor{Type: scope.ActorAdmin, ID: "usr_x"}, "platform.tenants.write", false},
		{"service", scope.Actor{Type: scope.ActorService, ID: "svc"}, "platform.tenants.read", false},
		{"zero actor", scope.Actor{}, "platform.tenants.read", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := z.Allowed(ctx, tt.actor, tt.perm); got != tt.want {
				t.Fatalf("Allowed() = %v", got)
			}
		})
	}
}
