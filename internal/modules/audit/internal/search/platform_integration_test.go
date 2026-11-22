//go:build integration

package search_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/search"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store/storetest"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/verify"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/db/page"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// TestPlatformSearchAndExport searches and exports the platform chain.
//
// Covers: GOV-01, GOV-16
func TestPlatformSearchAndExport(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	storetest.Append(t, env.DB, store.NewAppender(nil),
		storetest.Event(t, ids.TenantID{}, "tenant.created"), storetest.Event(t, ids.TenantID{}, "tenant.updated"))
	rec := &recorder{}
	svc := &search.Service{DB: env.DB, Recorder: rec}
	res, err := svc.SearchChain(t.Context(), store.ChainOf(ids.TenantID{}), search.Query{Type: "tenant.created"}, page.Request{})
	if err != nil || len(res.Records) != 1 {
		t.Fatalf("SearchChain = %d, %v", len(res.Records), err)
	}
	var buf bytes.Buffer
	if err := svc.ExportChain(t.Context(), store.ChainOf(ids.TenantID{}), search.ExportRange{}, &buf); err != nil {
		t.Fatal(err)
	}
	src, err := verify.ReadExport(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if rep, err := verify.Chain(t.Context(), src, verify.Options{}); err != nil || !rep.OK() || rep.Events != 2 {
		t.Fatalf("verify = %+v, %v", rep, err)
	}
	if len(rec.kinds) != 2 {
		t.Errorf("accesses = %v", rec.kinds)
	}
	rec.fail = true
	if err := svc.ExportChain(t.Context(), store.ChainOf(ids.TenantID{}), search.ExportRange{}, &buf); err == nil {
		t.Error("ExportChain succeeded without recording")
	}
	// Invalid queries and missing scope.
	tn := storetest.NewTenant(t)
	ctx := scope.WithTenant(t.Context(), scope.Tenant{TenantID: tn})
	for _, q := range []search.Query{
		{ActorID: strings.Repeat("x", search.MaxFilterLength+1)},
		{From: time.Now(), To: time.Now().Add(-time.Hour)},
	} {
		if _, err := svc.Search(ctx, q, page.Request{}); !errs.Is(err, errs.CodeValidationFailed) {
			t.Errorf("Search(%+v) = %v", q, err)
		}
	}
	if _, err := svc.Search(t.Context(), search.Query{}, page.Request{}); err == nil {
		t.Error("Search without a tenant scope succeeded")
	}
	if _, err := svc.Get(t.Context(), ids.AuditEventID{}); err == nil {
		t.Error("Get without a tenant scope succeeded")
	}
	if err := svc.Export(t.Context(), search.ExportRange{}, &buf); err == nil {
		t.Error("Export without a tenant scope succeeded")
	}
}
