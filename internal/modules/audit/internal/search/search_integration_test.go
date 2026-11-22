//go:build integration

package search_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/antonyrejith/portun/internal/modules/audit"
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

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

// recorder records accesses; it fails while fail is set.
type recorder struct {
	mu    sync.Mutex
	kinds []string
	fail  bool
}

func (r *recorder) RecordAccess(_ context.Context, kind string, _ any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("spool full")
	}
	r.kinds = append(r.kinds, kind)
	return nil
}

func seed(t *testing.T, env *dbtest.Env, tn ids.TenantID) []audit.Event {
	t.Helper()
	var evs []audit.Event
	for i := range 6 {
		ev := storetest.Event(t, tn, "widget.created")
		if i%2 == 1 {
			ev.Type, ev.Outcome, ev.Actor.ID = "widget.deleted", audit.OutcomeFailure, "usr_other"
			ev.Channel.Type = "console"
		}
		ev.Time = time.Date(2026, 11, 1, i, 0, 0, 0, time.UTC)
		evs = append(evs, ev)
	}
	storetest.Append(t, env.DB, store.NewAppender(nil), evs...)
	return evs
}

// TestSearch filters, pages and isolates tenants, and records every access
// before returning data.
//
// Covers: GOV-01, GOV-16
func TestSearch(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	t1, t2 := storetest.NewTenant(t), storetest.NewTenant(t)
	evs := seed(t, env, t1)
	seed(t, env, t2)
	storetest.Append(t, env.DB, store.NewAppender(nil), storetest.Event(t, ids.TenantID{}, "tenant.created"))
	rec := &recorder{}
	svc := &search.Service{DB: env.DB, Recorder: rec}
	ctx := scope.WithTenant(t.Context(), scope.Tenant{TenantID: t1})
	tests := []struct {
		name string
		q    search.Query
		want int
	}{
		{"all", search.Query{}, 6},
		{"actor", search.Query{ActorID: "usr_other"}, 3},
		{"type", search.Query{Type: "widget.created"}, 3},
		{"outcome", search.Query{Outcome: "failure"}, 3},
		{"channel", search.Query{Channel: "console"}, 3},
		{"category", search.Query{Category: "data"}, 6},
		{"target", search.Query{TargetType: "widget", TargetID: "w1"}, 6},
		{"time", search.Query{From: evs[2].Time, To: evs[4].Time}, 2},
		{"request", search.Query{RequestID: evs[0].Request.RequestID}, 1},
		{"trace", search.Query{TraceID: "trace-widget.created"}, 6},
		{"no match", search.Query{ActorID: "nobody"}, 0},
	}
	for _, tt := range tests {
		res, err := svc.Search(ctx, tt.q, page.Request{})
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if len(res.Records) != tt.want {
			t.Errorf("%s: %d records, want %d", tt.name, len(res.Records), tt.want)
		}
	}
	// Keyset pagination, newest first.
	var seqs []uint64
	req := page.Request{Size: 4}
	for {
		res, err := svc.Search(ctx, search.Query{}, req)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res.Records {
			seqs = append(seqs, r.Sequence)
		}
		if res.NextPageToken == "" {
			break
		}
		req.Token = res.NextPageToken
	}
	if len(seqs) != 6 || seqs[0] != 6 || seqs[5] != 1 {
		t.Errorf("pages = %v", seqs)
	}
	if _, err := svc.Search(ctx, search.Query{Type: "x"}, req); !errs.Is(err, errs.CodeInvalidRequest) {
		t.Errorf("a token reused for another query: %v", err)
	}
	// View: own records only.
	got, err := svc.Get(ctx, evs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var stored audit.Event
	if err := json.Unmarshal(got.Event, &stored); err != nil || stored.ID != evs[0].ID || stored.Integrity == nil {
		t.Fatalf("Get = %+v, %v", stored, err)
	}
	ctx2 := scope.WithTenant(t.Context(), scope.Tenant{TenantID: t2})
	if _, err := svc.Get(ctx2, evs[0].ID); !errs.Is(err, errs.CodeNotFound) {
		t.Errorf("another tenant's record: %v", err)
	}
	if want := len(tests) + 2 + 2; len(rec.kinds) != want {
		t.Errorf("recorded accesses = %d, want %d", len(rec.kinds), want)
	}
}

// TestAccessRecordingFailsClosed returns nothing when the access cannot be
// recorded.
//
// Covers: GOV-16
func TestAccessRecordingFailsClosed(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	tn := storetest.NewTenant(t)
	evs := seed(t, env, tn)
	svc := &search.Service{DB: env.DB, Recorder: &recorder{fail: true}}
	ctx := scope.WithTenant(t.Context(), scope.Tenant{TenantID: tn})
	if res, err := svc.Search(ctx, search.Query{}, page.Request{}); err == nil || len(res.Records) > 0 {
		t.Errorf("Search = %d records, %v", len(res.Records), err)
	}
	if _, err := svc.Get(ctx, evs[0].ID); err == nil {
		t.Error("Get succeeded")
	}
	var buf bytes.Buffer
	if err := svc.Export(ctx, search.ExportRange{}, &buf); err == nil || buf.Len() > 0 {
		t.Errorf("Export wrote %d bytes, %v", buf.Len(), err)
	}
	if _, err := (&search.Service{DB: env.DB}).Search(ctx, search.Query{}, page.Request{}); err == nil {
		t.Error("Search without a recorder succeeded")
	}
}

// TestTenantExportVerifies exports a tenant's chain, which verifies offline.
//
// Covers: GOV-16, GOV-18
func TestTenantExportVerifies(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	tn := storetest.NewTenant(t)
	seed(t, env, tn)
	rec := &recorder{}
	svc := &search.Service{DB: env.DB, Recorder: rec}
	ctx := scope.WithTenant(t.Context(), scope.Tenant{TenantID: tn})
	var buf bytes.Buffer
	if err := svc.Export(ctx, search.ExportRange{}, &buf); err != nil {
		t.Fatal(err)
	}
	src, err := verify.ReadExport(&buf)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := verify.Chain(t.Context(), src, verify.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() || rep.Events != 6 {
		t.Errorf("verify = %+v", rep)
	}
	if len(rec.kinds) != 1 || rec.kinds[0] != search.AccessExport {
		t.Errorf("accesses = %v", rec.kinds)
	}
}
