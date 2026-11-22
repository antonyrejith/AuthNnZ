package audit

import (
	"testing"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// TestRecordRejectsUnknown: unknown access kinds and retention actions are
// refused with an internal error before anything is emitted.
//
// Covers: GOV-16
func TestRecordRejectsUnknown(t *testing.T) {
	t.Parallel()
	a := &Auditor{}
	cases := []struct {
		name string
		call func() error
	}{
		{"access empty kind", func() error { return a.RecordAccess(t.Context(), "", nil) }},
		{"access unknown kind", func() error { return a.RecordAccess(t.Context(), "delete", map[string]string{}) }},
		{"retention empty action", func() error { return a.RecordRetention(t.Context(), "", nil) }},
		{"retention unknown action", func() error { return a.RecordRetention(t.Context(), "audit.verified", nil) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if err := c.call(); !errs.Is(err, errs.CodeInternal) {
				t.Errorf("error = %v, want %s", err, errs.CodeInternal)
			}
		})
	}
}

// TestServiceTypesRegistered: the registry holds the audit-access and
// retention types.
//
// Covers: GOV-16, GOV-01
func TestServiceTypesRegistered(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cat  Category
		keys []string
	}{
		{TypeAuditSearched, CategoryAuditAccess, []string{"query"}},
		{TypeAuditViewed, CategoryAuditAccess, []string{"event_id"}},
		{TypeAuditExported, CategoryAuditAccess, []string{"query", "chain", "range"}},
		{TypeAuditVerified, CategoryAuditAccess, []string{"chain", "range"}},
		{TypeRetentionPolicyChanged, CategoryPlatform, []string{"details"}},
		{TypeRetentionApplied, CategoryPlatform, []string{"details"}},
	}
	r := NewRegistry()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			et, ok := r.Lookup(c.name)
			if !ok {
				t.Fatalf("%s is not registered", c.name)
			}
			if et.Category != c.cat || et.OCSFClassUID != ocsfAPIActivity {
				t.Errorf("category %s class %d", et.Category, et.OCSFClassUID)
			}
			if len(et.Details) != len(c.keys) {
				t.Errorf("details = %v, want keys %v", et.Details, c.keys)
			}
			for _, k := range c.keys {
				if et.Details[k] != FieldPlain {
					t.Errorf("detail %s class = %v", k, et.Details[k])
				}
			}
		})
	}
}
