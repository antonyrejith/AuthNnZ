package apimw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/antonyrejith/portun/internal/platform/crypto"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/httpx"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// OperatorActorID is the ID of the actor of the bootstrap operator token.
const OperatorActorID = "bootstrap-operator"

// maxTokenLength bounds the credential read from a request.
const maxTokenLength = 4096

// AuthConfig configures the Authenticator.
type AuthConfig struct {
	// OperatorTokenSHA256 is the hex SHA-256 digest of the bootstrap operator
	// token (environment variable PORTUN_API_OPERATOR_TOKEN_SHA256). Empty
	// rejects every request.
	OperatorTokenSHA256 string
}

// Authenticator authenticates requests with the bootstrap operator bearer
// token (release B0; see the package documentation).
type Authenticator struct {
	digest  [sha256.Size]byte
	enabled bool
}

// NewAuthenticator returns an Authenticator. It fails when the digest is
// neither empty nor 64 hexadecimal characters.
func NewAuthenticator(cfg AuthConfig) (*Authenticator, error) {
	a := &Authenticator{}
	d := strings.TrimSpace(cfg.OperatorTokenSHA256)
	if d == "" {
		return a, nil
	}
	b, err := hex.DecodeString(d)
	if err != nil || len(b) != sha256.Size {
		return nil, errs.New(errs.CodeInvalidRequest, "the operator token digest must be 64 hexadecimal characters (SHA-256)")
	}
	copy(a.digest[:], b)
	a.enabled = true
	return a, nil
}

// errUnauthenticated is the error of every failed authentication; it does not
// say whether the header was missing, malformed or wrong.
func errUnauthenticated() error {
	return errs.New(errs.CodeUnauthenticated, "A valid bearer token is required.")
}

// Authenticate checks the request's bearer token and returns the actor. Every
// failure is errs.CodeUnauthenticated.
func (a *Authenticator) Authenticate(r *http.Request) (scope.Actor, error) {
	token, ok := bearerToken(r.Header)
	sum := sha256.Sum256([]byte(token))
	// The comparison runs for every request, with or without a token or a
	// configured digest, so that timing does not tell which case it was.
	match := crypto.Equal(sum[:], a.digest[:])
	if !ok || !a.enabled || !match {
		return scope.Actor{}, errUnauthenticated()
	}
	return scope.Actor{Type: scope.ActorPlatformOperator, ID: OperatorActorID}, nil
}

// bearerToken returns the token of a single "Authorization: Bearer <token>"
// header.
func bearerToken(h http.Header) (string, bool) {
	values := h.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}
	scheme, token, found := strings.Cut(values[0], " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	if token == "" || len(token) > maxTokenLength || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}

// Middleware authenticates every request and puts the actor into the request
// context. A failure is a 401 problem with a WWW-Authenticate header; the
// token is never logged.
func (a *Authenticator) Middleware() httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor, err := a.Authenticate(r)
			if err != nil {
				WriteProblem(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(scope.WithActor(r.Context(), actor)))
		})
	}
}

// Authorizer decides whether an actor holds a permission.
type Authorizer interface {
	Allowed(ctx context.Context, actor scope.Actor, permission string) bool
}

// OperatorAuthorizer is the B0 authorizer: platform operators hold every
// permission and nobody else holds any. Release B1 (T1.8) replaces it with
// per-scope administrator authorization.
type OperatorAuthorizer struct{}

// Allowed implements Authorizer.
func (OperatorAuthorizer) Allowed(_ context.Context, actor scope.Actor, permission string) bool {
	return actor.Type == scope.ActorPlatformOperator && permission != ""
}
