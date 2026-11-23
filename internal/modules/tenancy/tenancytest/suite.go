//go:build integration

package tenancytest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/tenancy"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/db/page"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/keystore"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// RunSuite runs the tenancy scenarios as parallel subtests, each on its own
// database: tenant lifecycle and crypto-shredding, validation, hierarchy,
// isolation, hosts, pagination, export and the middleware. The module's
// packages each call it, so that the facade, the application layer and the
// PostgreSQL adapter are all measured by it.
//
// Covers: TEN-01, TEN-03, TEN-09, GOV-11, GOV-12
func RunSuite(t *testing.T) {
	t.Helper()
	for _, s := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"Lifecycle", lifecycle},
		{"Validation", validation},
		{"Hierarchy", hierarchy},
		{"Isolation", isolation},
		{"Hosts", hosts},
		{"Pagination", pagination},
		{"Export", export},
		{"ExportFailure", exportFailure},
		{"Middleware", middleware},
		{"Construction", construction},
	} {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			guard(t, func() { s.run(t) })
		})
	}
}

func wantCode(t *testing.T, err error, code errs.Code) {
	t.Helper()
	if !errs.Is(err, code) {
		t.Fatalf("error = %v (code %q), want code %q", err, errs.CodeOf(err), code)
	}
}

// mustPanic carries the error of must and check to guard.
type mustPanic struct{ err error }

// must returns v or, for an error, stops the test (see guard).
func must[T any](v T, err error) T {
	if err != nil {
		panic(mustPanic{err})
	}
	return v
}

func must2[A, B any](a A, b B, err error) (A, B) {
	if err != nil {
		panic(mustPanic{err})
	}
	return a, b
}

func check(err error) {
	if err != nil {
		panic(mustPanic{err})
	}
}

// guard runs fn and fails t for the errors of must, must2 and check.
func guard(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			mp, ok := p.(mustPanic)
			if !ok {
				panic(p)
			}
			t.Fatalf("unexpected error: %v", mp.err)
		}
	}()
	fn()
}

func hasPointer(err error, pointer string) bool {
	var e *errs.Error
	if !errors.As(err, &e) {
		return false
	}
	for _, f := range e.Fields() {
		if f.Pointer == pointer {
			return true
		}
	}
	return false
}

func envByName(t *testing.T, envs []tenancy.Environment, name string) tenancy.Environment {
	t.Helper()
	for _, e := range envs {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("no environment %q", name)
	return tenancy.Environment{}
}

// lifecycle: create, get, update (stale version), suspend, resume and
// delete with crypto-shredding.
//
// Covers: TEN-09, GOV-11, GOV-12
func lifecycle(t *testing.T) {
	t.Helper()
	f := New(t)
	ctx := t.Context()
	tn, envs := f.Provision(t)
	if tn.Status != tenancy.TenantActive || tn.Version != 1 || len(envs) != 3 {
		t.Fatalf("created tenant = %+v with %d environments", tn, len(envs))
	}
	if g := must(f.Module.GetTenant(ctx, tn.ID)); g.Slug != tn.Slug || g.Region != "eu" {
		t.Fatalf("GetTenant = %+v", g)
	}
	if listed := must(f.Module.ListEnvironments(ctx, tn.ID)); len(listed) != 3 || listed[0].TenantID != tn.ID {
		t.Fatalf("ListEnvironments = %+v", listed)
	}
	_, _, err := f.Module.ResolveHost(ctx, tn.Slug+"-production."+BaseDomain)
	if err != nil {
		t.Fatal(err)
	}

	name := "Renamed"
	_, err = f.Module.UpdateTenant(ctx, tn.ID, 7, tenancy.UpdateTenantInput{Name: &name})
	wantCode(t, err, errs.CodePreconditionFailed)
	_, err = f.Module.UpdateTenant(ctx, tn.ID, 1, tenancy.UpdateTenantInput{})
	wantCode(t, err, errs.CodeValidationFailed)
	up := must(f.Module.UpdateTenant(ctx, tn.ID, 1, tenancy.UpdateTenantInput{Name: &name}))
	if up.Name != name || up.Version != 2 || up.Plan != "standard" {
		t.Fatalf("updated = %+v", up)
	}
	plan := "enterprise"
	up = must(f.Module.UpdateTenant(ctx, tn.ID, 2, tenancy.UpdateTenantInput{Plan: &plan}))
	if up.Plan != plan || up.Version != 3 {
		t.Fatalf("updated = %+v", up)
	}

	_, err = f.Module.SuspendTenant(ctx, tn.ID, 3, "  ")
	wantCode(t, err, errs.CodeValidationFailed)
	if !hasPointer(err, "/reason") {
		t.Fatalf("no /reason field error: %v", err)
	}
	_, err = f.Module.ResumeTenant(ctx, tn.ID, 3, "not suspended")
	wantCode(t, err, errs.CodeConflict)
	sus := must(f.Module.SuspendTenant(ctx, tn.ID, 3, "non-payment"))
	if sus.Status != tenancy.TenantSuspended || sus.Version != 4 {
		t.Fatalf("suspended = %+v", sus)
	}
	// The cache was dropped by the change: the suspension is seen at once.
	_, _, err = f.Module.ResolveHost(ctx, tn.Slug+"-production."+BaseDomain)
	wantCode(t, err, errs.CodePermissionDenied)
	if !strings.Contains(err.Error(), "tenant is suspended") {
		t.Fatalf("message = %v", err)
	}
	_, err = f.Module.SuspendTenant(ctx, tn.ID, 4, "again")
	wantCode(t, err, errs.CodeConflict)
	_, err = f.Module.UpdateTenant(ctx, tn.ID, 4, tenancy.UpdateTenantInput{Name: &name})
	if err != nil {
		t.Fatalf("a suspended tenant can be renamed: %v", err)
	}
	res := must(f.Module.ResumeTenant(ctx, tn.ID, 5, "paid"))
	if res.Status != tenancy.TenantActive || res.Version != 6 {
		t.Fatalf("resumed = %+v", res)
	}
	scp, rt, err := f.Module.ResolveHost(ctx, tn.Slug+"-staging."+BaseDomain)
	if err != nil || scp.TenantID != tn.ID || scp.EnvironmentID != envByName(t, envs, "staging").ID || rt.ID != tn.ID {
		t.Fatalf("resolve = %+v %+v %v", scp, rt, err)
	}

	lifecycleDelete(t, f, tn)
}

// lifecycleDelete deletes the tenant (at version 6) and checks that it is
// gone and that its data keys cannot be unwrapped any more.
func lifecycleDelete(t *testing.T, f *Fixture, tn tenancy.Tenant) {
	t.Helper()
	ctx := t.Context()
	// Crypto-shredding: a data key readable before the delete is not after.
	key := f.CreateDataKey(t, tn.ID, "usr_1")
	if _, err := f.DataKey(t, tn.ID, key); err != nil {
		t.Fatalf("data key before delete: %v", err)
	}
	_, err := f.Module.DeleteTenant(ctx, tn.ID, 1, "offboarding")
	wantCode(t, err, errs.CodePreconditionFailed)
	_, err = f.Module.DeleteTenant(ctx, tn.ID, 6, "")
	wantCode(t, err, errs.CodeValidationFailed)
	del := must(f.Module.DeleteTenant(ctx, tn.ID, 6, "offboarding"))
	if del.Status != tenancy.TenantDeleted || del.Version != 7 {
		t.Fatalf("deleted = %+v", del)
	}
	for _, h := range []string{"development", "staging", "production"} {
		_, _, err = f.Module.ResolveHost(ctx, tn.Slug+"-"+h+"."+BaseDomain)
		wantCode(t, err, errs.CodeNotFound)
	}
	if _, err := f.DataKey(t, tn.ID, key); err == nil {
		t.Fatal("the data key of a deleted tenant can still be unwrapped")
	}
	_, err = f.Module.GetTenant(ctx, tn.ID)
	wantCode(t, err, errs.CodeNotFound)
	_, err = f.Module.ListEnvironments(ctx, tn.ID)
	wantCode(t, err, errs.CodeNotFound)
	_, err = f.Module.SuspendTenant(ctx, tn.ID, 7, "x")
	wantCode(t, err, errs.CodeNotFound)
	_, err = f.Module.DeleteTenant(ctx, tn.ID, 7, "x")
	wantCode(t, err, errs.CodeNotFound)
	_, err = f.Module.StartExport(ctx, tn.ID)
	wantCode(t, err, errs.CodeNotFound)
	_, err = f.Module.GetTenant(ctx, must(ids.New[ids.TenantKind]()))
	wantCode(t, err, errs.CodeNotFound)

	// The tombstone is listed only on request.
	list, _ := must2(f.Module.ListTenants(ctx, tenancy.ListTenantsInput{}))
	if len(list) != 0 {
		t.Fatalf("default list = %+v", list)
	}
	list, _ = must2(f.Module.ListTenants(ctx, tenancy.ListTenantsInput{Status: tenancy.TenantDeleted}))
	if len(list) != 1 || list[0].ID != tn.ID {
		t.Fatalf("deleted list = %+v", list)
	}
}

// validation: every violation is reported with its JSON pointer.
//
// Covers: TEN-09, GOV-12
func validation(t *testing.T) {
	t.Helper()
	f := New(t)
	ctx := t.Context()
	long := strings.Repeat("a", 201)
	valid := func() tenancy.CreateTenantInput {
		return tenancy.CreateTenantInput{Slug: Slug(t), Name: "Name", Region: "eu", Plan: "standard"}
	}
	tests := []struct {
		name    string
		mod     func(*tenancy.CreateTenantInput)
		pointer string
	}{
		{"slug too short", func(in *tenancy.CreateTenantInput) { in.Slug = "a" }, "/slug"},
		{"slug uppercase", func(in *tenancy.CreateTenantInput) { in.Slug = "Acme" }, "/slug"},
		{"slug trailing dash", func(in *tenancy.CreateTenantInput) { in.Slug = "acme-" }, "/slug"},
		{"slug leading digit", func(in *tenancy.CreateTenantInput) { in.Slug = "1acme" }, "/slug"},
		{"name empty", func(in *tenancy.CreateTenantInput) { in.Name = "" }, "/name"},
		{"name too long", func(in *tenancy.CreateTenantInput) { in.Name = long }, "/name"},
		{"name control character", func(in *tenancy.CreateTenantInput) { in.Name = "a\x00b" }, "/name"},
		{"region empty", func(in *tenancy.CreateTenantInput) { in.Region = "" }, "/region"},
		{"region invalid", func(in *tenancy.CreateTenantInput) { in.Region = "EU West" }, "/region"},
		{"region too long", func(in *tenancy.CreateTenantInput) { in.Region = strings.Repeat("a", 65) }, "/region"},
		{"plan empty", func(in *tenancy.CreateTenantInput) { in.Plan = "" }, "/plan"},
		{"environment name invalid", func(in *tenancy.CreateTenantInput) {
			in.Environments = []tenancy.EnvironmentInput{{Name: "Prod", Kind: "production"}}
		}, "/environments/0/name"},
		{"environment name duplicate", func(in *tenancy.CreateTenantInput) {
			in.Environments = []tenancy.EnvironmentInput{{Name: "a", Kind: "production"}, {Name: "a", Kind: "staging"}}
		}, "/environments/1/name"},
		{"environment kind invalid", func(in *tenancy.CreateTenantInput) {
			in.Environments = []tenancy.EnvironmentInput{{Name: "a", Kind: "qa"}}
		}, "/environments/0/kind"},
		{"host label too long", func(in *tenancy.CreateTenantInput) {
			in.Slug = "t" + strings.Repeat("a", 30)
			in.Environments = []tenancy.EnvironmentInput{{Name: strings.Repeat("e", 40), Kind: "staging"}}
		}, "/environments/0/name"},
		{"too many environments", func(in *tenancy.CreateTenantInput) {
			for i := range 11 {
				in.Environments = append(in.Environments, tenancy.EnvironmentInput{Name: "e" + string(rune('a'+i)), Kind: "staging"})
			}
		}, "/environments"},
		{"partner does not exist", func(in *tenancy.CreateTenantInput) { in.PartnerID = must(ids.New[ids.PartnerKind]()) }, "/partner_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard(t, func() {
				in := valid()
				tt.mod(&in)
				_, _, err := f.Module.CreateTenant(ctx, in)
				wantCode(t, err, errs.CodeValidationFailed)
				if !hasPointer(err, tt.pointer) {
					t.Fatalf("no field error at %s: %v", tt.pointer, err)
				}
			})
		})
	}
	first := valid()
	must2(f.Module.CreateTenant(ctx, first))
	second := valid()
	second.Slug = first.Slug
	_, _, err := f.Module.CreateTenant(ctx, second)
	wantCode(t, err, errs.CodeConflict)
	// A failed creation leaves nothing behind (reconciliation at the end of
	// the test proves no row is unaudited).
	list, _ := must2(f.Module.ListTenants(ctx, tenancy.ListTenantsInput{}))
	if len(list) != 1 {
		t.Fatalf("%d tenants after a rejected duplicate, want 1", len(list))
	}
	_, err = f.Module.CreatePartner(ctx, tenancy.CreatePartnerInput{Name: ""})
	wantCode(t, err, errs.CodeValidationFailed)
	_, err = f.Module.CreatePartner(ctx, tenancy.CreatePartnerInput{Name: "p", ParentID: must(ids.New[ids.PartnerKind]())})
	wantCode(t, err, errs.CodeValidationFailed)
	_, _, err = f.Module.ListTenants(ctx, tenancy.ListTenantsInput{Status: "bogus"})
	wantCode(t, err, errs.CodeValidationFailed)
}

// hierarchy: partners nest to three levels; tenants hang under partners and
// get their environments and default hosts.
//
// Covers: TEN-01, GOV-12
func hierarchy(t *testing.T) {
	t.Helper()
	f := New(t)
	ctx := t.Context()
	p1 := must(f.Module.CreatePartner(ctx, tenancy.CreatePartnerInput{Name: "Telco"}))
	p2 := must(f.Module.CreatePartner(ctx, tenancy.CreatePartnerInput{Name: "Telco EU", ParentID: p1.ID}))
	p3 := must(f.Module.CreatePartner(ctx, tenancy.CreatePartnerInput{Name: "Telco DE", ParentID: p2.ID}))
	if p1.Status != tenancy.PartnerActive || p1.Version != 1 || p2.ParentID != p1.ID || p3.ParentID != p2.ID {
		t.Fatalf("partners = %+v %+v %+v", p1, p2, p3)
	}
	_, err := f.Module.CreatePartner(ctx, tenancy.CreatePartnerInput{Name: "Too deep", ParentID: p3.ID})
	wantCode(t, err, errs.CodeValidationFailed)
	if !hasPointer(err, "/parent_id") {
		t.Fatalf("no /parent_id field error: %v", err)
	}

	slug := Slug(t)
	tn, envs, err := f.Module.CreateTenant(ctx, tenancy.CreateTenantInput{
		Slug: slug, Name: "Customer", Region: "eu", Plan: "standard", PartnerID: p3.ID,
		Environments: []tenancy.EnvironmentInput{{Name: "dev", Kind: "development"}, {Name: "live", Kind: "production"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tn.PartnerID != p3.ID || len(envs) != 2 || envs[0].TenantID != tn.ID || envs[1].Kind != tenancy.KindProduction {
		t.Fatalf("tenant = %+v environments = %+v", tn, envs)
	}
	for _, e := range envs {
		scp, _, err := f.Module.ResolveHost(ctx, slug+"-"+e.Name+"."+BaseDomain)
		if err != nil || scp.EnvironmentID != e.ID || scp.TenantID != tn.ID {
			t.Fatalf("default host of %s: %+v %v", e.Name, scp, err)
		}
	}
	other, _ := f.Provision(t)
	list, _ := must2(f.Module.ListTenants(ctx, tenancy.ListTenantsInput{PartnerID: p3.ID}))
	if len(list) != 1 || list[0].ID != tn.ID {
		t.Fatalf("tenants of the partner = %+v", list)
	}
	list, _ = must2(f.Module.ListTenants(ctx, tenancy.ListTenantsInput{Status: tenancy.TenantActive}))
	if len(list) != 2 {
		t.Fatalf("active tenants = %+v (other %s)", list, other.ID)
	}
	if list[0].CreatedAt.After(list[1].CreatedAt) {
		t.Fatal("tenants are not ordered by creation")
	}
}

// isolation: the application role sees no partners, cannot write tenants and
// sees only its tenant's environments; the schema follows the row-level
// security conventions.
//
// Covers: TEN-03
func isolation(t *testing.T) {
	t.Helper()
	f := New(t)
	ctx := t.Context()
	dbtest.AssertRLS(t, f.Env, "tenancy")
	a, _ := f.Provision(t)
	b, _ := f.Provision(t)
	must(f.Module.CreatePartner(ctx, tenancy.CreatePartnerInput{Name: "P"}))

	conn, err := pgx.Connect(ctx, f.Env.AppURL.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	denied := func(name, sql string) {
		t.Helper()
		_, err := conn.Exec(ctx, sql)
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "42501" {
			t.Errorf("%s: err = %v, want permission denied", name, err)
		}
	}
	denied("read partners", `SELECT * FROM tenancy.partners`)
	denied("read hosts", `SELECT * FROM tenancy.hosts`)
	denied("read operations", `SELECT * FROM tenancy.operations`)
	denied("read kek_ref", `SELECT kek_ref FROM tenancy.tenants`)
	denied("update tenants", `UPDATE tenancy.tenants SET name = 'x'`)
	denied("insert tenants", `INSERT INTO tenancy.tenants (id, slug, name, status, region, plan) VALUES (gen_random_uuid(), 'abc', 'n', 'active', 'eu', 'p')`)
	if _, err := conn.Exec(ctx, `UPDATE tenancy.environments SET name = 'x'`); err == nil {
		t.Error("the application role updated environments")
	}
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM tenancy.tenants`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("app role reads tenants: %d, %v", n, err)
	}
	// Without a tenant scope the policy fails closed.
	if _, err := conn.Exec(ctx, `SELECT count(*) FROM tenancy.environments`); err == nil {
		t.Error("environments readable without a tenant scope")
	}
	for _, tn := range []tenancy.Tenant{a, b} {
		if _, err := conn.Exec(ctx, `SELECT set_config('app.tenant_id', $1, false)`, tn.ID.UUID().String()); err != nil {
			t.Fatal(err)
		}
		var total, foreign int
		if err := conn.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE tenant_id <> $1) FROM tenancy.environments`, tn.ID.UUID()).Scan(&total, &foreign); err != nil {
			t.Fatal(err)
		}
		if total != 3 || foreign != 0 {
			t.Errorf("tenant %s sees %d environments, %d of them foreign", tn.Slug, total, foreign)
		}
	}
}

// hosts: registration, normalization, conflicts, removal and the cache.
//
// Covers: TEN-01, TEN-09, GOV-12
func hosts(t *testing.T) {
	t.Helper()
	f := New(t)
	ctx := t.Context()
	a, aenvs := f.Provision(t)
	b, benvs := f.Provision(t)
	prod := envByName(t, aenvs, "production")

	check(f.Module.AddHost(ctx, a.ID, prod.ID, "Login.Acme.Example"))
	for _, h := range []string{"login.acme.example", "LOGIN.acme.example:8443", "login.acme.example.", "login.acme.example.:443"} {
		scp, _, err := f.Module.ResolveHost(ctx, h)
		if err != nil || scp.TenantID != a.ID || scp.EnvironmentID != prod.ID {
			t.Fatalf("resolve %q = %+v, %v", h, scp, err)
		}
	}
	// A second resolution is served from the cache; a change drops it.
	f.Clock.Advance(time.Second)
	check(errOf3(f.Module.ResolveHost(ctx, "login.acme.example")))
	f.Clock.Advance(time.Minute)
	check(errOf3(f.Module.ResolveHost(ctx, "login.acme.example")))

	wantCode(t, f.Module.AddHost(ctx, a.ID, prod.ID, "login.acme.example"), errs.CodeConflict)
	wantCode(t, f.Module.AddHost(ctx, b.ID, benvs[0].ID, "login.acme.example"), errs.CodeConflict)
	wantCode(t, f.Module.AddHost(ctx, a.ID, benvs[0].ID, "other.acme.example"), errs.CodeNotFound)
	wantCode(t, f.Module.AddHost(ctx, must(ids.New[ids.TenantKind]()), prod.ID, "x.acme.example"), errs.CodeNotFound)
	for _, bad := range []string{"", "bad host", "a..b", "-a.example", "host:80", strings.Repeat("a", 64) + ".example"} {
		err := f.Module.AddHost(ctx, a.ID, prod.ID, bad)
		wantCode(t, err, errs.CodeValidationFailed)
	}
	for _, unknown := range []string{"", "nope.example", "a..b", "x:notaport", strings.Repeat("a.", 130) + "example"} {
		_, _, err := f.Module.ResolveHost(ctx, unknown)
		wantCode(t, err, errs.CodeNotFound)
	}

	check(f.Module.RemoveHost(ctx, "LOGIN.acme.example:1"))
	_, _, err := f.Module.ResolveHost(ctx, "login.acme.example")
	wantCode(t, err, errs.CodeNotFound)
	wantCode(t, f.Module.RemoveHost(ctx, "login.acme.example"), errs.CodeNotFound)
	wantCode(t, f.Module.RemoveHost(ctx, "bad host"), errs.CodeNotFound)

	// A host can be added to a suspended tenant but not to a deleted one.
	susp := must(f.Module.SuspendTenant(ctx, a.ID, 1, "review"))
	check(f.Module.AddHost(ctx, a.ID, prod.ID, "paused.acme.example"))
	_, _, err = f.Module.ResolveHost(ctx, "paused.acme.example")
	wantCode(t, err, errs.CodePermissionDenied)
	must(f.Module.DeleteTenant(ctx, a.ID, susp.Version, "gone"))
	wantCode(t, f.Module.AddHost(ctx, a.ID, prod.ID, "late.acme.example"), errs.CodeNotFound)
}

func errOf[A any](_ A, err error) error { return err }

func errOf3[A, B any](_ A, _ B, err error) error { return err }

// pagination: keyset pages over tenants.
//
// Covers: TEN-01
func pagination(t *testing.T) {
	t.Helper()
	f := New(t)
	ctx := t.Context()
	want := map[ids.TenantID]bool{}
	for range 5 {
		tn, _ := f.Provision(t)
		want[tn.ID] = true
	}
	var token string
	pages := 0
	for {
		rows, next, err := f.Module.ListTenants(ctx, tenancy.ListTenantsInput{Page: page.Request{Size: 2, Token: token}})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, r := range rows {
			if !want[r.ID] {
				t.Fatalf("unexpected or repeated tenant %s", r.Slug)
			}
			delete(want, r.ID)
		}
		if next == "" {
			break
		}
		token = next
	}
	if pages != 3 || len(want) != 0 {
		t.Fatalf("pages = %d, missing = %d", pages, len(want))
	}
	_, _, err := f.Module.ListTenants(ctx, tenancy.ListTenantsInput{Page: page.Request{Token: "garbage"}})
	wantCode(t, err, errs.CodeInvalidRequest)
	_, _, err = f.Module.ListTenants(ctx, tenancy.ListTenantsInput{Status: tenancy.TenantSuspended, Page: page.Request{Token: token}})
	wantCode(t, err, errs.CodeInvalidRequest)
	_, _, err = f.Module.ListTenants(ctx, tenancy.ListTenantsInput{Page: page.Request{Size: 1000}})
	wantCode(t, err, errs.CodeInvalidRequest)
}

func waitOperation(t *testing.T, f *Fixture, id ids.OperationID) tenancy.Operation {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		op := must(f.Module.GetOperation(t.Context(), id))
		if op.Status != tenancy.OperationRunning {
			return op
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("export did not finish")
	return tenancy.Operation{}
}

// export: the file, its permissions and content, the registered exporters,
// and shutdown.
//
// Covers: TEN-09, GOV-12
func export(t *testing.T) {
	t.Helper()
	f := New(t)
	ctx := t.Context()
	tn, envs := f.Provision(t)
	var sawScope ids.TenantID
	check(f.Module.RegisterExporter("widgets", func(ctx context.Context, tenant ids.TenantID, w io.Writer) error {
		st, err := scope.TenantFrom(ctx)
		if err != nil {
			return err //nolint:wrapcheck // test kit
		}
		sawScope = st.TenantID
		_, err = io.WriteString(w, `{"widget":"w1","tenant":"`+tenant.String()+`"}`) // no trailing newline
		return err                                                                   //nolint:wrapcheck // test kit
	}))
	check(f.Module.RegisterExporter("empty", func(context.Context, ids.TenantID, io.Writer) error { return nil }))
	wantCode(t, f.Module.RegisterExporter("widgets", func(context.Context, ids.TenantID, io.Writer) error { return nil }), errs.CodeInternal)
	wantCode(t, f.Module.RegisterExporter("Bad Name", func(context.Context, ids.TenantID, io.Writer) error { return nil }), errs.CodeInternal)
	wantCode(t, f.Module.RegisterExporter("nil", nil), errs.CodeInternal)

	op := must(f.Module.StartExport(ctx, tn.ID))
	if op.Status != tenancy.OperationRunning || op.Kind != tenancy.OperationExport || op.TenantID != tn.ID {
		t.Fatalf("started = %+v", op)
	}
	done := waitOperation(t, f, op.ID)
	if done.Status != tenancy.OperationSucceeded || done.ResultPath == "" || done.ErrorCode != "" {
		t.Fatalf("finished = %+v", done)
	}
	if sawScope != tn.ID {
		t.Errorf("exporter scope tenant = %s, want %s", sawScope, tn.ID)
	}
	if want := f.ExportDir + "/" + tn.ID.String() + "/" + op.ID.String() + ".jsonl"; done.ResultPath != want {
		t.Fatalf("result path = %q, want %q", done.ResultPath, want)
	}
	fi := must(os.Stat(done.ResultPath))
	di := must(os.Stat(f.ExportDir + "/" + tn.ID.String()))
	if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
		t.Errorf("modes: file %v, directory %v", fi.Mode().Perm(), di.Mode().Perm())
	}
	checkExportFile(t, must(os.ReadFile(done.ResultPath)), tn, envs)
	wantCode(t, errOf(f.Module.GetOperation(ctx, must(ids.New[ids.OperationKind]()))), errs.CodeNotFound)

	// A suspended tenant can still be exported (offboarding).
	sus := must(f.Module.SuspendTenant(ctx, tn.ID, 1, "review"))
	op2 := must(f.Module.StartExport(ctx, sus.ID))
	if st := waitOperation(t, f, op2.ID).Status; st != tenancy.OperationSucceeded {
		t.Fatalf("export of a suspended tenant: %s", st)
	}

	// Close waits for a running export and refuses new ones.
	op3 := must(f.Module.StartExport(ctx, tn.ID))
	if err := f.Module.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if st := must(f.Module.GetOperation(ctx, op3.ID)).Status; st == tenancy.OperationRunning {
		t.Fatal("Close returned while an export was running")
	}
	_, err := f.Module.StartExport(ctx, tn.ID)
	wantCode(t, err, errs.CodeUnavailable)
}

// exportFailure: a failing or panicking exporter fails the operation and
// leaves no file; a canceled shutdown cancels running exports.
//
// Covers: TEN-09, GOV-12
func exportFailure(t *testing.T) {
	t.Helper()
	f := New(t)
	ctx := t.Context()
	tn, _ := f.Provision(t)
	check(f.Module.RegisterExporter("a-fails", func(_ context.Context, _ ids.TenantID, w io.Writer) error {
		_, _ = io.WriteString(w, "partial\n")
		return errs.New(errs.CodeUnavailable, "module down")
	}))
	op := must(f.Module.StartExport(ctx, tn.ID))
	done := waitOperation(t, f, op.ID)
	if done.Status != tenancy.OperationFailed || done.ErrorCode != errs.CodeUnavailable || done.ResultPath != "" {
		t.Fatalf("finished = %+v", done)
	}
	assertNoFile(t, f, tn.ID, op.ID)

	f2 := New(t)
	tn2, _ := f2.Provision(t)
	check(f2.Module.RegisterExporter("boom", func(context.Context, ids.TenantID, io.Writer) error { panic("exporter bug") }))
	op2 := must(f2.Module.StartExport(ctx, tn2.ID))
	done = waitOperation(t, f2, op2.ID)
	if done.Status != tenancy.OperationFailed || done.ErrorCode != errs.CodeInternal {
		t.Fatalf("finished = %+v", done)
	}

	f3 := New(t)
	tn3, _ := f3.Provision(t)
	started := make(chan struct{})
	check(f3.Module.RegisterExporter("slow", func(ctx context.Context, _ ids.TenantID, _ io.Writer) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}))
	op3 := must(f3.Module.StartExport(ctx, tn3.ID))
	<-started
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	wantCode(t, f3.Module.Close(cctx), errs.CodeDeadlineExceeded)
	if st := must(f3.Module.GetOperation(ctx, op3.ID)); st.Status != tenancy.OperationFailed || st.ErrorCode != errs.CodeCanceled {
		t.Fatalf("canceled export = %+v", st)
	}
}

func assertNoFile(t *testing.T, f *Fixture, tn ids.TenantID, op ids.OperationID) {
	t.Helper()
	if _, err := os.Stat(f.ExportDir + "/" + tn.String() + "/" + op.String() + ".jsonl"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a failed export left its file: %v", err)
	}
}

// middleware: the scope of a request comes from its host and nothing else.
//
// Covers: TEN-01, TEN-09
func middleware(t *testing.T) {
	t.Helper()
	f := New(t)
	ctx := t.Context()
	tn, envs := f.Provision(t)
	prod := envByName(t, envs, "production")
	var got scope.Tenant
	var scoped bool
	h := f.Module.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, scoped = scope.Tenant{}, false
		if st, err := scope.TenantFrom(r.Context()); err == nil {
			got, scoped = st, true
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	do := func(host string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
		req.Host = host
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	host := tn.Slug + "-production." + BaseDomain
	for _, in := range []string{host, strings.ToUpper(host), host + ":8443", host + ".", host + ".:443"} {
		scoped = false
		if rec := do(in, nil); rec.Code != http.StatusNoContent || !scoped || got.TenantID != tn.ID || got.EnvironmentID != prod.ID {
			t.Errorf("host %q: status %d scoped %v scope %+v", in, rec.Code, scoped, got)
		}
	}
	// A forwarded host does not change the scope.
	rec := do("unknown.example", map[string]string{"X-Forwarded-Host": host, "Forwarded": "host=" + host})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Header().Get("Content-Type"), "problem+json") {
		t.Errorf("unknown host: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	sus := must(f.Module.SuspendTenant(ctx, tn.ID, 1, "review"))
	rec = do(host, nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "tenant is suspended") {
		t.Errorf("suspended: %d %s", rec.Code, rec.Body.String())
	}
	must(f.Module.ResumeTenant(ctx, tn.ID, sus.Version, "ok"))
	if rec := do(host, nil); rec.Code != http.StatusNoContent {
		t.Errorf("resumed: %d", rec.Code)
	}
}

// construction: required dependencies and the registry.
//
// Covers: GOV-11
func construction(t *testing.T) {
	t.Helper()
	f := New(t)
	deps := tenancy.Deps{DB: f.Env.DB, Auditor: f.Stack.Auditor, Keys: f.Stack.Keys, BaseDomain: BaseDomain, ExportDir: t.TempDir()}
	for name, mod := range map[string]func(*tenancy.Deps){
		"db":          func(d *tenancy.Deps) { d.DB = nil },
		"auditor":     func(d *tenancy.Deps) { d.Auditor = nil },
		"keys":        func(d *tenancy.Deps) { d.Keys = nil },
		"export dir":  func(d *tenancy.Deps) { d.ExportDir = "" },
		"base domain": func(d *tenancy.Deps) { d.BaseDomain = "Not A Domain" },
	} {
		d := deps
		mod(&d)
		if _, err := tenancy.New(d); !errs.Is(err, errs.CodeInternal) {
			t.Errorf("%s: err = %v, want internal", name, err)
		}
	}
	if _, err := tenancy.New(deps); err != nil {
		t.Fatal(err)
	}
	// Registering the events twice is an error; every type is a platform event.
	if err := tenancy.RegisterEvents(f.Stack.Registry); !errs.Is(err, errs.CodeInternal) {
		t.Errorf("second registration: %v", err)
	}
	for _, name := range []string{
		tenancy.TypePartnerCreated, tenancy.TypeTenantCreated, tenancy.TypeTenantUpdated, tenancy.TypeTenantSuspended,
		tenancy.TypeTenantResumed, tenancy.TypeTenantDeleted, tenancy.TypeTenantExportRequest, tenancy.TypeTenantExportComplete,
		tenancy.TypeEnvironmentCreated, tenancy.TypeTenantHostAdded, tenancy.TypeTenantHostRemoved,
	} {
		et, ok := f.Stack.Registry.Lookup(name)
		if !ok || et.Category != audit.CategoryPlatform || et.OCSFClassUID != 3004 {
			t.Errorf("event type %s = %+v, registered %v", name, et, ok)
		}
	}
	_ = keystore.TableTenantKEKs
}

// checkExportFile checks the content of an export file of the export
// scenario: the tenant, its environments and hosts, then the exporters.
func checkExportFile(t *testing.T, data []byte, tn tenancy.Tenant, envs []tenancy.Environment) {
	t.Helper()
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	// tenant, 3 environments, 3 hosts, then the exporters in name order.
	if len(lines) != 1+3+3+3 || !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatalf("%d lines in\n%s", len(lines), data)
	}
	var first struct {
		Type string
		Data map[string]any
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil || first.Type != "tenant" || first.Data["slug"] != tn.Slug {
		t.Fatalf("first line %q: %v", lines[0], err)
	}
	if _, ok := first.Data["kek_ref"]; ok {
		t.Error("the export leaks the key reference")
	}
	for i, l := range lines[1:4] {
		if !strings.Contains(l, `"type":"environment"`) || !strings.Contains(l, envs[i].ID.String()) {
			t.Errorf("unexpected environment line %q, want environment %s", l, envs[i].ID)
		}
	}
	for _, l := range lines[4:7] {
		if !strings.Contains(l, `"type":"host"`) || !strings.Contains(l, tn.Slug+"-") {
			t.Errorf("unexpected host line %q", l)
		}
	}
	if lines[7] != `{"exporter":"empty"}` || lines[8] != `{"exporter":"widgets"}` || !strings.HasPrefix(lines[9], `{"widget":"w1"`) {
		t.Errorf("exporter sections: %q", lines[7:])
	}
}
