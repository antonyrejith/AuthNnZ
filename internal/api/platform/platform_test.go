package platform_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/antonyrejith/portun/internal/api/apimw"
	"github.com/antonyrejith/portun/internal/api/platform"
	"github.com/antonyrejith/portun/internal/modules/tenancy"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/httpx"
	"github.com/antonyrejith/portun/internal/platform/idempotency"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

const (
	token    = "platform-operator-token-0123456789"
	tenantID = "ten_01h455vb4pex5vsknk084sn02q"
)

func tokenDigest() string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// readOnly allows reads only.
type readOnly struct{}

func (readOnly) Allowed(_ context.Context, _ scope.Actor, permission string) bool {
	return strings.HasSuffix(permission, ".read")
}

// newHandler returns the Platform API on modules that have no database: every
// request that reaches a handler would fail, so the tests here cover what the
// API decides on its own, before the tenancy module is asked.
func newHandler(t *testing.T, authz apimw.Authorizer) http.Handler {
	t.Helper()
	h, err := platform.New(platform.Deps{
		Tenancy:     new(tenancy.Module),
		Auth:        apimw.AuthConfig{OperatorTokenSHA256: tokenDigest()},
		Authorizer:  authz,
		Idempotency: new(idempotency.Store),
	})
	if err != nil {
		t.Fatal(err)
	}
	rt := httpx.NewRouter()
	rt.Handle(platform.Prefix, h)
	return httpx.Chain(rt, httpx.RequestID(httpx.RequestIDConfig{}), httpx.MaxBytes(1<<20))
}

type result struct {
	code    int
	header  http.Header
	body    string
	problem httpx.Problem
}

func send(h http.Handler, method, path, body string, headers map[string]string) result {
	r := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		if v == "" {
			r.Header.Del(k)
			continue
		}
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	b, _ := io.ReadAll(rec.Result().Body)
	res := result{code: rec.Code, header: rec.Header(), body: string(b)}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), httpx.ContentTypeProblem) {
		_ = json.Unmarshal(b, &res.problem)
	}
	return res
}

func authed(extra ...string) map[string]string {
	m := map[string]string{"Authorization": "Bearer " + token}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i]] = extra[i+1]
	}
	return m
}

// TestAPIRejectsUnauthenticated: every path, known or not, answers a request
// without a valid operator token with a 401 problem and does nothing else.
//
// Covers: SEC-09
func TestAPIRejectsUnauthenticated(t *testing.T) {
	t.Parallel()
	h := newHandler(t, nil)
	tests := []struct {
		name    string
		method  string
		path    string
		headers map[string]string
	}{
		{"no credentials", http.MethodGet, "/platform/v1/tenants", nil},
		{"wrong token", http.MethodGet, "/platform/v1/tenants", map[string]string{"Authorization": "Bearer nope"}},
		{"basic scheme", http.MethodGet, "/platform/v1/tenants", map[string]string{"Authorization": "Basic " + token}},
		{"write without credentials", http.MethodPost, "/platform/v1/tenants", nil},
		{"unknown path", http.MethodGet, "/platform/v1/nope", nil},
		{"custom method without credentials", http.MethodPost, "/platform/v1/tenants/" + tenantID + ":suspend", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := send(h, tt.method, tt.path, `{}`, tt.headers)
			if res.code != http.StatusUnauthorized || res.problem.Code != errs.CodeUnauthenticated ||
				res.header.Get("WWW-Authenticate") != "Bearer" || strings.Contains(res.body, token) {
				t.Fatalf("%d %v %s", res.code, res.header, res.body)
			}
		})
	}
}

// TestAPIRejectsWithoutPermission: an authenticated caller without the
// operation's permission gets 403, also for the colon-method operations.
//
// Covers: SEC-09
func TestAPIRejectsWithoutPermission(t *testing.T) {
	t.Parallel()
	h := newHandler(t, readOnly{})
	for _, tt := range []struct {
		method, path, body string
		headers            map[string]string
	}{
		{http.MethodPost, "/platform/v1/tenants", `{"slug":"acme","name":"Acme","region":"eu","plan":"standard"}`, nil},
		{http.MethodPost, "/platform/v1/partners", `{"name":"Acme"}`, nil},
		{http.MethodPost, "/platform/v1/tenants/" + tenantID + ":suspend", "", authed("If-Match", `"1"`, "Change-Reason", "x")},
		{http.MethodPost, "/platform/v1/tenants/" + tenantID + ":export", "", nil},
		{http.MethodDelete, "/platform/v1/hosts/app.example.com", "", nil},
	} {
		headers := tt.headers
		if headers == nil {
			headers = authed()
		}
		res := send(h, tt.method, tt.path, tt.body, headers)
		if res.code != http.StatusForbidden || res.problem.Code != errs.CodePermissionDenied {
			t.Errorf("%s %s: %d %s", tt.method, tt.path, res.code, res.body)
		}
	}
}

// TestAPIUnmatchedRequests: unknown paths and wrong methods are problems.
func TestAPIUnmatchedRequests(t *testing.T) {
	t.Parallel()
	h := newHandler(t, nil)
	res := send(h, http.MethodGet, "/platform/v1/nope", "", authed())
	if res.code != http.StatusNotFound || res.problem.Code != errs.CodeNotFound {
		t.Errorf("unknown path: %d %s", res.code, res.body)
	}
	res = send(h, http.MethodPut, "/platform/v1/tenants", "", authed())
	if res.code != http.StatusMethodNotAllowed || res.problem.Code != errs.CodeMethodNotAllowed || res.header.Get("Allow") == "" {
		t.Errorf("wrong method: %d %v %s", res.code, res.header, res.body)
	}
}

// TestAPIValidation: requests that break the specification are rejected with
// problems that name the field, before any handler runs.
//
// Covers: SEC-09
func TestAPIValidation(t *testing.T) {
	t.Parallel()
	h := newHandler(t, nil)
	const goodTenant = `{"slug":"acme","name":"Acme","region":"eu","plan":"standard"}`
	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		headers     map[string]string
		wantCode    int
		wantProblem errs.Code
		wantPointer string // "" when the problem has no field error
	}{
		{"path pattern", http.MethodGet, "/platform/v1/tenants/not-an-id", "", nil, 422, errs.CodeValidationFailed, "/path/tenant_id"},
		{"path prefix of another kind", http.MethodGet, "/platform/v1/tenants/ptn_01h455vb4pex5vsknk084sn02q", "", nil, 422, errs.CodeValidationFailed, "/path/tenant_id"},
		{"path ID that does not decode", http.MethodGet, "/platform/v1/tenants/ten_uuuuuuuuuuuuuuuuuuuuuuuuuu", "", nil, 422, errs.CodeValidationFailed, "/path/tenant_id"},
		{"operation ID pattern", http.MethodGet, "/platform/v1/operations/ten_01h455vb4pex5vsknk084sn02q", "", nil, 422, errs.CodeValidationFailed, "/path/operation_id"},
		{"page size zero", http.MethodGet, "/platform/v1/tenants?page_size=0", "", nil, 422, errs.CodeValidationFailed, "/query/page_size"},
		{"page size too big", http.MethodGet, "/platform/v1/tenants?page_size=501", "", nil, 422, errs.CodeValidationFailed, "/query/page_size"},
		{"page size not a number", http.MethodGet, "/platform/v1/tenants?page_size=many", "", nil, 422, errs.CodeValidationFailed, "/query/page_size"},
		{"status not in the enum", http.MethodGet, "/platform/v1/tenants?status=gone", "", nil, 422, errs.CodeValidationFailed, "/query/status"},
		{"partner ID pattern", http.MethodGet, "/platform/v1/tenants?partner_id=x", "", nil, 422, errs.CodeValidationFailed, "/query/partner_id"},
		{"slug pattern", http.MethodPost, "/platform/v1/tenants", `{"slug":"BAD SLUG","name":"Acme","region":"eu","plan":"standard"}`, nil, 422, errs.CodeValidationFailed, "/slug"},
		{"nested field", http.MethodPost, "/platform/v1/tenants", `{"slug":"acme","name":"Acme","region":"eu","plan":"standard","environments":[{"name":"dev","kind":"sandbox"}]}`, nil, 422, errs.CodeValidationFailed, "/environments/0/kind"},
		{"unknown field", http.MethodPost, "/platform/v1/tenants", `{"slug":"acme","name":"Acme","region":"eu","plan":"standard","admin":true}`, nil, 400, errs.CodeInvalidRequest, ""},
		{"missing fields", http.MethodPost, "/platform/v1/tenants", `{}`, nil, 422, errs.CodeValidationFailed, "/slug"},
		{"not JSON", http.MethodPost, "/platform/v1/tenants", `{"slug":`, nil, 400, errs.CodeInvalidRequest, ""},
		{"wrong content type", http.MethodPost, "/platform/v1/tenants", goodTenant, authed("Content-Type", "text/plain"), 400, errs.CodeInvalidRequest, ""},
		{"merge patch needs its own content type", http.MethodPatch, "/platform/v1/tenants/" + tenantID, `{"name":"x"}`, authed("If-Match", `"1"`), 400, errs.CodeInvalidRequest, ""},
		{"body without a body", http.MethodPost, "/platform/v1/partners", "", nil, 400, errs.CodeInvalidRequest, ""},
		{"payload too large", http.MethodPost, "/platform/v1/partners", `{"name":"` + strings.Repeat("x", 2<<20) + `"}`, nil, 413, errs.CodePayloadTooLarge, ""},
		{"update without If-Match", http.MethodPatch, "/platform/v1/tenants/" + tenantID, `{"name":"x"}`, authed("Content-Type", "application/merge-patch+json"), 412, errs.CodePreconditionFailed, ""},
		{"suspend without If-Match", http.MethodPost, "/platform/v1/tenants/" + tenantID + ":suspend", "", authed("Change-Reason", "x"), 412, errs.CodePreconditionFailed, ""},
		{"weak If-Match", http.MethodPost, "/platform/v1/tenants/" + tenantID + ":resume", "", authed("If-Match", `W/"1"`, "Change-Reason", "x"), 412, errs.CodePreconditionFailed, ""},
		{"If-Match star", http.MethodPost, "/platform/v1/tenants/" + tenantID + ":delete", "", authed("If-Match", `*`, "Change-Reason", "x"), 412, errs.CodePreconditionFailed, ""},
		{"If-Match not a version", http.MethodPost, "/platform/v1/tenants/" + tenantID + ":delete", "", authed("If-Match", `"abc"`, "Change-Reason", "x"), 412, errs.CodePreconditionFailed, ""},
		{"suspend without reason", http.MethodPost, "/platform/v1/tenants/" + tenantID + ":suspend", "", authed("If-Match", `"1"`), 422, errs.CodeValidationFailed, "/headers/Change-Reason"},
		{"delete with a blank reason", http.MethodPost, "/platform/v1/tenants/" + tenantID + ":delete", "", authed("If-Match", `"1"`, "Change-Reason", "   "), 422, errs.CodeValidationFailed, "/headers/Change-Reason"},
		{"reason too long", http.MethodPost, "/platform/v1/tenants/" + tenantID + ":suspend", "", authed("If-Match", `"1"`, "Change-Reason", strings.Repeat("r", 501)), 422, errs.CodeValidationFailed, "/headers/Change-Reason"},
		{"ticket too long", http.MethodPost, "/platform/v1/tenants/" + tenantID + ":suspend", "", authed("If-Match", `"1"`, "Change-Reason", "x", "Change-Ticket", strings.Repeat("t", 101)), 422, errs.CodeValidationFailed, "/headers/Change-Ticket"},
		{"idempotency key too long", http.MethodPost, "/platform/v1/tenants", goodTenant, authed("Idempotency-Key", strings.Repeat("k", 256)), 422, errs.CodeValidationFailed, "/headers/Idempotency-Key"},
		{"host path too long", http.MethodDelete, "/platform/v1/hosts/" + strings.Repeat("a", 254), "", nil, 422, errs.CodeValidationFailed, "/path/host"},
		{"host body", http.MethodPost, "/platform/v1/tenants/" + tenantID + "/hosts", `{"environment_id":"nope","host":"a.example.com"}`, nil, 422, errs.CodeValidationFailed, "/environment_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			headers := tt.headers
			if headers == nil {
				headers = authed()
			}
			res := send(h, tt.method, tt.path, tt.body, headers)
			if res.code != tt.wantCode || res.problem.Code != tt.wantProblem || res.problem.Status != tt.wantCode ||
				res.header.Get("Content-Type") != httpx.ContentTypeProblem {
				t.Fatalf("%d %v %s", res.code, res.header, res.body)
			}
			if tt.wantPointer != "" {
				found := false
				for _, f := range res.problem.Errors {
					found = found || f.Pointer == tt.wantPointer
				}
				if !found {
					t.Fatalf("no field error at %s: %s", tt.wantPointer, res.body)
				}
			}
			if res.problem.RequestID == "" || res.problem.Instance == "" || strings.Contains(res.body, token) {
				t.Fatalf("problem lacks request_id or instance, or leaks the token: %s", res.body)
			}
		})
	}
}

// TestNewRequiresDependencies: a missing dependency or a bad token digest
// stops the server from starting.
func TestNewRequiresDependencies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		deps platform.Deps
	}{
		{"nothing", platform.Deps{}},
		{"no idempotency store", platform.Deps{Tenancy: new(tenancy.Module)}},
		{"no tenancy module", platform.Deps{Idempotency: new(idempotency.Store)}},
		{"bad digest", platform.Deps{Tenancy: new(tenancy.Module), Idempotency: new(idempotency.Store), Auth: apimw.AuthConfig{OperatorTokenSHA256: "zz"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := platform.New(tt.deps); err == nil {
				t.Fatal("New() succeeded")
			}
		})
	}
}
