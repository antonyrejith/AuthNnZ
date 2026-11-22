//go:build integration

package audit_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store/storetest"
	"github.com/antonyrejith/portun/internal/platform/db/page"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// TestSearchIsAudited: a search is spooled as audit.searched on the
// tenant's chain before results are returned, and fails closed when the
// record cannot be written.
//
// Covers: GOV-16
func TestSearchIsAudited(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		closeSpool bool
	}{
		{"recorded", false},
		{"spool unavailable", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := setup(t, nil)
			s := audit.NewSearch(f.env.DB, f.a)
			if c.closeSpool {
				if err := f.spool.Close(); err != nil {
					t.Fatal(err)
				}
			}
			res, err := s.Search(f.ctx(t), audit.SearchQuery{Type: "widget.created"}, page.Request{})
			if c.closeSpool {
				if err == nil {
					t.Fatal("Search succeeded without a durable access record")
				}
				if len(res.Records) != 0 || res.NextPageToken != "" {
					t.Errorf("results returned on failure: %+v", res)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if n, err := f.spool.ShipOnce(t.Context()); err != nil || n != 1 {
				t.Fatalf("ShipOnce = %d, %v", n, err)
			}
			evs := f.pub.events(t)
			if len(evs) != 1 {
				t.Fatalf("%d events spooled, want 1", len(evs))
			}
			ev := evs[0]
			if ev.Type != audit.TypeAuditSearched || ev.Category != audit.CategoryAuditAccess {
				t.Errorf("event = %s / %s", ev.Type, ev.Category)
			}
			if ev.TenantID != f.tenant {
				t.Errorf("event tenant = %s, want %s", ev.TenantID, f.tenant)
			}
			if _, ok := ev.Details["query"]; !ok {
				t.Errorf("details = %v, want query", ev.Details)
			}
		})
	}
}

// TestRetentionIsAudited: changing a retention policy is spooled as
// audit.retention_policy_changed on the platform chain.
//
// Covers: GOV-16, GOV-01
func TestRetentionIsAudited(t *testing.T) {
	t.Parallel()
	f := setup(t, nil)
	k, ref := storetest.SigningKey(t)
	svc, err := audit.NewRetention(audit.RetentionConfig{DB: f.env.DB, KMS: k, Key: ref, Logger: storetest.Logger()}, f.a)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetPolicy(t.Context(), audit.RetentionPolicy{Chain: uuid.New(), RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	if n, err := f.spool.ShipOnce(t.Context()); err != nil || n != 1 {
		t.Fatalf("ShipOnce = %d, %v", n, err)
	}
	evs := f.pub.events(t)
	if len(evs) != 1 || evs[0].Type != audit.TypeRetentionPolicyChanged || !evs[0].TenantID.IsZero() {
		t.Fatalf("events = %+v", evs)
	}
	if _, ok := evs[0].Details["details"]; !ok {
		t.Errorf("details = %v", evs[0].Details)
	}

	// Fail closed: with the spool closed the policy change is refused.
	if err := f.spool.Close(); err != nil {
		t.Fatal(err)
	}
	err = svc.SetPolicy(t.Context(), audit.RetentionPolicy{Chain: uuid.New(), RetentionDays: 10})
	if !errs.Is(err, errs.CodeUnavailable) {
		t.Errorf("SetPolicy with closed spool = %v", err)
	}
}
