//go:build integration

package platform_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/api/apimw"
	"github.com/antonyrejith/portun/internal/api/platform"
	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/tenancy"
	"github.com/antonyrejith/portun/internal/modules/tenancy/tenancytest"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/httpx"
	"github.com/antonyrejith/portun/internal/platform/idempotency"
	"github.com/antonyrejith/portun/internal/platform/keystore"
	"github.com/antonyrejith/portun/internal/platform/outbox"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(m, dbtest.Options{Sets: []migrate.Set{
		outbox.Migrations(), keystore.Migrations(), audit.StoreMigrations(), tenancy.Migrations(), idempotency.Migrations(),
	}}))
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// api is the Platform API on a real database behind an httptest.Server, with
// the middleware of a production server.
type api struct {
	*tenancytest.Fixture
	srv  *httptest.Server
	logs *syncBuffer
}

func newAPI(t *testing.T) *api {
	t.Helper()
	f := tenancytest.New(t)
	store, err := idempotency.New(idempotency.Config{DB: f.Env.DB})
	if err != nil {
		t.Fatal(err)
	}
	h, err := platform.New(platform.Deps{
		Tenancy: f.Module, Auth: apimw.AuthConfig{OperatorTokenSHA256: tokenDigest()}, Idempotency: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	rt := httpx.NewRouter()
	rt.Handle(platform.Prefix, h)
	srv := httptest.NewServer(httpx.Chain(rt,
		httpx.Recover(logger), httpx.RequestID(httpx.RequestIDConfig{}), httpx.AccessLog(logger), httpx.MaxBytes(1<<20)))
	t.Cleanup(srv.Close)
	return &api{Fixture: f, srv: srv, logs: logs}
}

// call sends a request with the operator token (headers override).
func (a *api) call(t *testing.T, method, path, body string, headers ...string) result {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, a.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	res := result{code: resp.StatusCode, header: resp.Header, body: string(b)}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), httpx.ContentTypeProblem) {
		if err := json.Unmarshal(b, &res.problem); err != nil {
			t.Fatalf("problem body: %v: %s", err, b)
		}
	}
	return res
}

func decode[T any](t *testing.T, r result) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(r.body), &v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
	return v
}

func wantStatus(t *testing.T, r result, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("status = %d, want %d: %s", r.code, code, r.body)
	}
}

func wantProblem(t *testing.T, r result, status int, code errs.Code) {
	t.Helper()
	if r.code != status || r.problem.Code != code {
		t.Fatalf("status %d code %q, want %d %q: %s", r.code, r.problem.Code, status, code, r.body)
	}
}

type tenantBody struct {
	ID           string `json:"id"`
	PartnerID    string `json:"partner_id"`
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Region       string `json:"region"`
	Plan         string `json:"plan"`
	Version      int64  `json:"version"`
	Environments []struct {
		ID       string `json:"id"`
		TenantID string `json:"tenant_id"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
	} `json:"environments"`
}

type listBody struct {
	Items         []tenantBody `json:"items"`
	NextPageToken string       `json:"next_page_token"`
}

type operationBody struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
}

func tenantJSON(slug string, extra string) string {
	return `{"slug":"` + slug + `","name":"Tenant ` + slug + `","region":"eu","plan":"standard"` + extra + `}`
}

// auditEvents returns the audit events of a type that were recorded in the
// outbox (no audit writer runs in this test).
func (a *api) auditEvents(t *testing.T, typ string) []audit.Event {
	t.Helper()
	var out []audit.Event
	err := a.Env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT payload::text FROM outbox.messages WHERE stream = 'AUDIT' AND payload->>'type' = $1 ORDER BY created_at, id`, typ)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var payload string
			if err := rows.Scan(&payload); err != nil {
				return err
			}
			var ev audit.Event
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				return err
			}
			out = append(out, ev)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestPlatformAPILifecycle drives every operation of the Platform API over
// HTTP against a real database: partners, tenant creation, reads, listing and
// paging, merge-patch updates with ETag and If-Match, suspend, resume,
// environments, host names, export with its operation, and delete. Every row
// written is reconciled against the audit records when the test ends.
//
// Covers: TEN-09, TEN-01, GOV-11, SEC-09
func TestPlatformAPILifecycle(t *testing.T) {
	t.Parallel()
	a := newAPI(t)

	// Partners: 201 with the created partner, nested under a parent.
	res := a.call(t, http.MethodPost, "/platform/v1/partners", `{"name":"Acme Reseller"}`)
	wantStatus(t, res, http.StatusCreated)
	partner := decode[struct {
		ID, Name, Status string
		Version          int64
		ParentID         string `json:"parent_id"`
	}](t, res)
	if !strings.HasPrefix(partner.ID, "ptn_") || partner.Name != "Acme Reseller" || partner.Status != "active" || partner.Version != 1 || partner.ParentID != "" {
		t.Fatalf("partner = %+v: %s", partner, res.body)
	}
	res = a.call(t, http.MethodPost, "/platform/v1/partners", `{"name":"Sub Reseller","parent_id":"`+partner.ID+`"}`)
	wantStatus(t, res, http.StatusCreated)
	if got := decode[struct {
		ParentID string `json:"parent_id"`
	}](t, res); got.ParentID != partner.ID {
		t.Fatalf("child partner = %s", res.body)
	}
	res = a.call(t, http.MethodPost, "/platform/v1/partners", `{"name":"Orphan","parent_id":"ptn_01h455vb4pex5vsknk084sn02q"}`)
	if res.code < 400 || res.problem.Code == "" {
		t.Fatalf("a partner under an unknown parent: %d %s", res.code, res.body)
	}

	// Create a tenant: 201 with ETag, Location, the tenant and its environments.
	slug := tenancytest.Slug(t)
	res = a.call(t, http.MethodPost, "/platform/v1/tenants", tenantJSON(slug, `,"partner_id":"`+partner.ID+`"`), "Change-Reason", "contract signed", "Change-Ticket", "CHG-1")
	wantStatus(t, res, http.StatusCreated)
	created := decode[tenantBody](t, res)
	if created.Status != "active" || created.Version != 1 || created.Slug != slug || created.PartnerID != partner.ID || len(created.Environments) != 3 ||
		res.header.Get("ETag") != `"1"` || res.header.Get("Location") != "/platform/v1/tenants/"+created.ID ||
		!strings.HasPrefix(res.header.Get("Content-Type"), "application/json") {
		t.Fatalf("created = %+v, headers %v", created, res.header)
	}
	tenantPath := "/platform/v1/tenants/" + created.ID
	var prodEnv string
	for _, e := range created.Environments {
		if e.Kind == "production" && e.TenantID == created.ID {
			prodEnv = e.ID
		}
	}
	if prodEnv == "" {
		t.Fatalf("no production environment: %s", res.body)
	}
	res = a.call(t, http.MethodPost, "/platform/v1/tenants", tenantJSON(slug, ""))
	wantProblem(t, res, http.StatusConflict, errs.CodeConflict)

	// Read it back.
	res = a.call(t, http.MethodGet, tenantPath, "")
	wantStatus(t, res, http.StatusOK)
	if got := decode[tenantBody](t, res); got.ID != created.ID || got.Name != "Tenant "+slug || res.header.Get("ETag") != `"1"` {
		t.Fatalf("get = %s, headers %v", res.body, res.header)
	}

	// List and paging; the second tenant is another partner's.
	slug2 := tenancytest.Slug(t)
	res = a.call(t, http.MethodPost, "/platform/v1/tenants", tenantJSON(slug2, ""))
	wantStatus(t, res, http.StatusCreated)
	second := decode[tenantBody](t, res)
	res = a.call(t, http.MethodGet, "/platform/v1/tenants?page_size=1", "")
	wantStatus(t, res, http.StatusOK)
	page1 := decode[listBody](t, res)
	if len(page1.Items) != 1 || page1.NextPageToken == "" || page1.Items[0].ID != created.ID {
		t.Fatalf("page 1 = %s", res.body)
	}
	res = a.call(t, http.MethodGet, "/platform/v1/tenants?page_size=1&page_token="+page1.NextPageToken, "")
	wantStatus(t, res, http.StatusOK)
	page2 := decode[listBody](t, res)
	if len(page2.Items) != 1 || page2.Items[0].ID != second.ID || page2.NextPageToken != "" {
		t.Fatalf("page 2 = %s", res.body)
	}
	res = a.call(t, http.MethodGet, "/platform/v1/tenants?partner_id="+partner.ID, "")
	if got := decode[listBody](t, res); res.code != http.StatusOK || len(got.Items) != 1 || got.Items[0].ID != created.ID {
		t.Fatalf("partner filter = %s", res.body)
	}
	res = a.call(t, http.MethodGet, "/platform/v1/tenants?status=suspended", "")
	if got := decode[listBody](t, res); res.code != http.StatusOK || got.Items == nil || len(got.Items) != 0 || !strings.Contains(res.body, `"items":[]`) {
		t.Fatalf("status filter = %s", res.body)
	}
	res = a.call(t, http.MethodGet, "/platform/v1/tenants?page_token=garbage", "")
	if res.code != http.StatusUnprocessableEntity && res.code != http.StatusBadRequest {
		t.Fatalf("garbage page token: %d %s", res.code, res.body)
	}

	// Update with merge patch.
	patch := func(ifMatch, body string, headers ...string) result {
		h := append([]string{"Content-Type", "application/merge-patch+json", "If-Match", ifMatch}, headers...)
		return a.call(t, http.MethodPatch, tenantPath, body, h...)
	}
	res = patch(`"1"`, `{"name":"Renamed"}`)
	wantStatus(t, res, http.StatusOK)
	if got := decode[tenantBody](t, res); got.Name != "Renamed" || got.Version != 2 || got.Plan != "standard" || res.header.Get("ETag") != `"2"` {
		t.Fatalf("patched = %s, headers %v", res.body, res.header)
	}
	res = patch(`"2"`, `{"plan":"enterprise"}`)
	wantStatus(t, res, http.StatusOK)
	if got := decode[tenantBody](t, res); got.Plan != "enterprise" || got.Name != "Renamed" || res.header.Get("ETag") != `"3"` {
		t.Fatalf("patched = %s", res.body)
	}
	wantProblem(t, patch(`"1"`, `{"name":"Stale"}`), http.StatusPreconditionFailed, errs.CodePreconditionFailed)
	wantProblem(t, patch(`W/"3"`, `{"name":"Weak"}`), http.StatusPreconditionFailed, errs.CodePreconditionFailed)
	wantProblem(t, patch(``, `{"name":"None"}`), http.StatusPreconditionFailed, errs.CodePreconditionFailed)
	res = patch(`"3"`, `{}`)
	wantProblem(t, res, http.StatusUnprocessableEntity, errs.CodeValidationFailed)
	res = patch(`"3"`, `{"plan":"Not A Plan"}`)
	wantProblem(t, res, http.StatusUnprocessableEntity, errs.CodeValidationFailed)
	if len(res.problem.Errors) == 0 || res.problem.Errors[0].Pointer != "/plan" {
		t.Fatalf("plan error = %s", res.body)
	}
	if got := decode[tenantBody](t, a.call(t, http.MethodGet, tenantPath, "")); got.Version != 3 {
		t.Fatalf("a rejected update changed the tenant: %+v", got)
	}

	// Suspend needs If-Match and a reason; the reason is audited.
	suspend := func(ifMatch string, headers ...string) result {
		h := append([]string{"If-Match", ifMatch}, headers...)
		return a.call(t, http.MethodPost, tenantPath+":suspend", "", h...)
	}
	wantProblem(t, suspend(`"3"`), http.StatusUnprocessableEntity, errs.CodeValidationFailed)
	wantProblem(t, suspend(`"2"`, "Change-Reason", "late payment"), http.StatusPreconditionFailed, errs.CodePreconditionFailed)
	res = suspend(`"3"`, "Change-Reason", "  late payment  ", "Change-Ticket", "CHG-42")
	wantStatus(t, res, http.StatusOK)
	if got := decode[tenantBody](t, res); got.Status != "suspended" || got.Version != 4 || res.header.Get("ETag") != `"4"` {
		t.Fatalf("suspended = %s", res.body)
	}
	wantProblem(t, suspend(`"4"`, "Change-Reason", "again"), http.StatusConflict, errs.CodeConflict)
	if _, _, err := a.Module.ResolveHost(t.Context(), slug+"-production."+tenancytest.BaseDomain); !errs.Is(err, errs.CodePermissionDenied) {
		t.Fatalf("a suspended tenant still resolves: %v", err)
	}
	events := a.auditEvents(t, tenancy.TypeTenantSuspended)
	if len(events) != 1 {
		t.Fatalf("%d suspend events", len(events))
	}
	ev := events[0]
	if ev.Reason == nil || ev.Reason.Text != "late payment" || ev.Reason.Ticket != "CHG-42" ||
		ev.Actor.Type != "platform_operator" || ev.Actor.ID != apimw.OperatorActorID ||
		ev.Request.Operation != "suspendTenant" || ev.Channel.Type != "api" || ev.Request.RequestID == "" {
		t.Fatalf("suspend event: reason %+v actor %+v request %+v channel %+v", ev.Reason, ev.Actor, ev.Request, ev.Channel)
	}
	if created := a.auditEvents(t, tenancy.TypeTenantCreated); len(created) != 2 || created[0].Reason == nil || created[0].Reason.Text != "contract signed" || created[0].Reason.Ticket != "CHG-1" ||
		created[1].Reason != nil {
		t.Fatalf("create events: %+v", created)
	}

	// Resume.
	res = a.call(t, http.MethodPost, tenantPath+":resume", "", "If-Match", `"4"`, "Change-Reason", "paid")
	wantStatus(t, res, http.StatusOK)
	if got := decode[tenantBody](t, res); got.Status != "active" || got.Version != 5 || res.header.Get("ETag") != `"5"` {
		t.Fatalf("resumed = %s", res.body)
	}
	wantProblem(t, a.call(t, http.MethodPost, tenantPath+":resume", "", "If-Match", `"5"`, "Change-Reason", "again"), http.StatusConflict, errs.CodeConflict)

	// Environments.
	res = a.call(t, http.MethodGet, tenantPath+"/environments", "")
	wantStatus(t, res, http.StatusOK)
	envs := decode[struct {
		Items []struct{ ID, Name, Kind string } `json:"items"`
	}](t, res)
	if len(envs.Items) != 3 {
		t.Fatalf("environments = %s", res.body)
	}

	// Host names: add (normalized), duplicate, resolve, remove, remove again.
	res = a.call(t, http.MethodPost, tenantPath+"/hosts", `{"environment_id":"`+prodEnv+`","host":"App.Example.COM"}`)
	wantStatus(t, res, http.StatusCreated)
	if got := decode[struct {
		Host, TenantID, EnvironmentID string
	}](t, res); got.Host != "app.example.com" {
		t.Fatalf("host = %s", res.body)
	}
	if st, _, err := a.Module.ResolveHost(t.Context(), "app.example.com"); err != nil || st.EnvironmentID.String() != prodEnv {
		t.Fatalf("ResolveHost = %+v, %v", st, err)
	}
	wantProblem(t, a.call(t, http.MethodPost, tenantPath+"/hosts", `{"environment_id":"`+prodEnv+`","host":"app.example.com"}`), http.StatusConflict, errs.CodeConflict)
	wantProblem(t, a.call(t, http.MethodPost, tenantPath+"/hosts", `{"environment_id":"`+prodEnv+`","host":"bad host"}`), http.StatusUnprocessableEntity, errs.CodeValidationFailed)
	res = a.call(t, http.MethodDelete, "/platform/v1/hosts/app.example.com", "", "Change-Reason", "decommissioned")
	wantStatus(t, res, http.StatusNoContent)
	if res.body != "" {
		t.Fatalf("204 with a body: %q", res.body)
	}
	if _, _, err := a.Module.ResolveHost(t.Context(), "app.example.com"); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("a removed host still resolves: %v", err)
	}
	wantProblem(t, a.call(t, http.MethodDelete, "/platform/v1/hosts/app.example.com", ""), http.StatusNotFound, errs.CodeNotFound)
	if got := a.auditEvents(t, tenancy.TypeTenantHostRemoved); len(got) != 1 || got[0].Reason == nil || got[0].Reason.Text != "decommissioned" {
		t.Fatalf("host removal events = %+v", got)
	}

	// Export: 202 with Location and the operation; poll until it succeeded.
	res = a.call(t, http.MethodPost, tenantPath+":export", "", "Change-Reason", "customer request")
	wantStatus(t, res, http.StatusAccepted)
	op := decode[operationBody](t, res)
	if !strings.HasPrefix(op.ID, "opr_") || op.Kind != "export" || op.TenantID != created.ID || res.header.Get("Location") != "/platform/v1/operations/"+op.ID {
		t.Fatalf("export = %s, headers %v", res.body, res.header)
	}
	deadline := time.Now().Add(30 * time.Second)
	for op.Status == "running" {
		if time.Now().After(deadline) {
			t.Fatalf("the export is still running: %+v", op)
		}
		time.Sleep(50 * time.Millisecond)
		res = a.call(t, http.MethodGet, "/platform/v1/operations/"+op.ID, "")
		wantStatus(t, res, http.StatusOK)
		op = decode[operationBody](t, res)
	}
	if op.Status != "succeeded" {
		t.Fatalf("export finished as %+v: %s", op, res.body)
	}
	wantProblem(t, a.call(t, http.MethodGet, "/platform/v1/operations/opr_01h455vb4pex5vsknk084sn02q", ""), http.StatusNotFound, errs.CodeNotFound)

	// Delete: shreds the tenant; it is gone from reads.
	wantProblem(t, a.call(t, http.MethodPost, tenantPath+":delete", "", "If-Match", `"1"`, "Change-Reason", "offboarding"), http.StatusPreconditionFailed, errs.CodePreconditionFailed)
	res = a.call(t, http.MethodPost, tenantPath+":delete", "", "If-Match", `"5"`, "Change-Reason", "offboarding")
	wantStatus(t, res, http.StatusOK)
	if got := decode[tenantBody](t, res); got.Status != "deleted" || got.Version != 6 {
		t.Fatalf("deleted = %s", res.body)
	}
	wantProblem(t, a.call(t, http.MethodGet, tenantPath, ""), http.StatusNotFound, errs.CodeNotFound)
	wantProblem(t, a.call(t, http.MethodGet, tenantPath+"/environments", ""), http.StatusNotFound, errs.CodeNotFound)
	wantProblem(t, a.call(t, http.MethodPost, tenantPath+":suspend", "", "If-Match", `"6"`, "Change-Reason", "x"), http.StatusNotFound, errs.CodeNotFound)
	if got := decode[listBody](t, a.call(t, http.MethodGet, "/platform/v1/tenants", "")); len(got.Items) != 1 || got.Items[0].ID != second.ID {
		t.Fatalf("the list after delete = %+v", got)
	}

	// Access logs use route templates, not the mount prefix or concrete IDs.
	logs := a.logs.String()
	for _, route := range []string{
		"/platform/v1/tenants/{tenant_id}", "/platform/v1/tenants/{tenant_id}:suspend", "/platform/v1/tenants/{tenant_id}/hosts",
		"/platform/v1/hosts/{host}", "/platform/v1/operations/{operation_id}", "/platform/v1/tenants",
	} {
		if !strings.Contains(logs, `"route":"`+route+`"`) {
			t.Errorf("no access log with route %s", route)
		}
	}
	if strings.Contains(logs, `"route":"/platform/v1/"`) || strings.Contains(logs, token) {
		t.Errorf("the access log has the mount prefix as route, or the token")
	}
}

// TestPlatformAPIErrors covers the error contract end to end on a real
// server: 401 and 403, validation with pointers, and problem bodies.
//
// Covers: SEC-09, TEN-01
func TestPlatformAPIErrors(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	res := a.call(t, http.MethodGet, "/platform/v1/tenants", "", "Authorization", "")
	wantProblem(t, res, http.StatusUnauthorized, errs.CodeUnauthenticated)
	if res.header.Get("WWW-Authenticate") != "Bearer" || res.header.Get("Content-Type") != httpx.ContentTypeProblem {
		t.Fatalf("401 headers: %v", res.header)
	}
	wantProblem(t, a.call(t, http.MethodGet, "/platform/v1/tenants", "", "Authorization", "Bearer wrong"), http.StatusUnauthorized, errs.CodeUnauthenticated)
	wantProblem(t, a.call(t, http.MethodGet, "/platform/v1/nope", ""), http.StatusNotFound, errs.CodeNotFound)

	res = a.call(t, http.MethodGet, "/platform/v1/tenants/not-an-id", "")
	wantProblem(t, res, http.StatusUnprocessableEntity, errs.CodeValidationFailed)
	if len(res.problem.Errors) != 1 || res.problem.Errors[0].Pointer != "/path/tenant_id" {
		t.Fatalf("errors = %s", res.body)
	}
	res = a.call(t, http.MethodPost, "/platform/v1/tenants", tenantJSON("Bad Slug", ""))
	wantProblem(t, res, http.StatusUnprocessableEntity, errs.CodeValidationFailed)
	if len(res.problem.Errors) == 0 || res.problem.Errors[0].Pointer != "/slug" {
		t.Fatalf("errors = %s", res.body)
	}
	// A tenant that was never created, with a well-formed ID.
	missing := "ten_01h455vb4pex5vsknk084sn02q"
	wantProblem(t, a.call(t, http.MethodGet, "/platform/v1/tenants/"+missing, ""), http.StatusNotFound, errs.CodeNotFound)
	wantProblem(t, a.call(t, http.MethodPost, "/platform/v1/tenants/"+missing+":export", ""), http.StatusNotFound, errs.CodeNotFound)
	wantProblem(t, a.call(t, http.MethodPost, "/platform/v1/tenants/"+missing+"/hosts", `{"environment_id":"env_01h455vb4pex5vsknk084sn02q","host":"a.example.com"}`), http.StatusNotFound, errs.CodeNotFound)
}

// TestPlatformAPIIdempotency: a POST with an Idempotency-Key runs once; its
// repeat is replayed with the same status, headers and body; another body
// with the same key is rejected; keys cover custom methods too.
//
// Covers: SEC-09, GOV-11
func TestPlatformAPIIdempotency(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	slug := tenancytest.Slug(t)
	body := tenantJSON(slug, "")
	first := a.call(t, http.MethodPost, "/platform/v1/tenants", body, "Idempotency-Key", "create-1")
	wantStatus(t, first, http.StatusCreated)
	if first.header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("the first response is marked replayed: %v", first.header)
	}
	again := a.call(t, http.MethodPost, "/platform/v1/tenants", body, "Idempotency-Key", "create-1")
	wantStatus(t, again, http.StatusCreated)
	if again.header.Get("Idempotent-Replayed") != "true" || again.body != first.body ||
		again.header.Get("ETag") != first.header.Get("ETag") || again.header.Get("Location") != first.header.Get("Location") {
		t.Fatalf("replay: %v %s (first %v %s)", again.header, again.body, first.header, first.body)
	}
	if got := decode[listBody](t, a.call(t, http.MethodGet, "/platform/v1/tenants", "")); len(got.Items) != 1 {
		t.Fatalf("the repeat created another tenant: %+v", got)
	}
	reused := a.call(t, http.MethodPost, "/platform/v1/tenants", tenantJSON(tenancytest.Slug(t), ""), "Idempotency-Key", "create-1")
	wantProblem(t, reused, http.StatusUnprocessableEntity, errs.CodeValidationFailed)
	if len(reused.problem.Errors) != 1 || reused.problem.Errors[0].Pointer != "/headers/Idempotency-Key" ||
		!strings.Contains(reused.problem.Errors[0].Detail, "Idempotency-Key reused with a different request") {
		t.Fatalf("reuse = %s", reused.body)
	}
	wantProblem(t, a.call(t, http.MethodPost, "/platform/v1/tenants", body, "Idempotency-Key", strings.Repeat("k", 256)), http.StatusUnprocessableEntity, errs.CodeValidationFailed)

	// A custom-method POST: the repeat does not suspend twice.
	id := decode[tenantBody](t, first).ID
	suspend := func() result {
		return a.call(t, http.MethodPost, "/platform/v1/tenants/"+id+":suspend", "", "If-Match", `"1"`, "Change-Reason", "x", "Idempotency-Key", "suspend-1")
	}
	s1, s2 := suspend(), suspend()
	wantStatus(t, s1, http.StatusOK)
	if s2.code != http.StatusOK || s2.header.Get("Idempotent-Replayed") != "true" || s2.body != s1.body {
		t.Fatalf("suspend replay: %d %v %s", s2.code, s2.header, s2.body)
	}
	if got := a.auditEvents(t, tenancy.TypeTenantSuspended); len(got) != 1 {
		t.Fatalf("%d suspend events, want 1", len(got))
	}
	// A key on a non-POST request is ignored.
	for range 2 {
		res := a.call(t, http.MethodGet, "/platform/v1/tenants/"+id, "", "Idempotency-Key", "get-1")
		if res.code != http.StatusOK || res.header.Get("Idempotent-Replayed") != "" {
			t.Fatalf("GET with a key: %d %v", res.code, res.header)
		}
	}
}
