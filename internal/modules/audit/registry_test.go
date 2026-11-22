package audit_test

import (
	"slices"
	"testing"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// TestRegister checks the registration rules.
//
// Covers: GOV-01
func TestRegister(t *testing.T) {
	t.Parallel()
	ok := audit.EventType{
		Name: "user.created", Category: audit.CategoryData, OCSFClassUID: 3001,
		Fields: map[string]audit.FieldClass{"/email": audit.FieldPersonal}, Details: map[string]audit.FieldClass{"via": audit.FieldPlain},
	}
	cases := []struct {
		name string
		t    audit.EventType
		ok   bool
	}{
		{"valid", ok, true},
		{"uppercase", audit.EventType{Name: "User.created", Category: audit.CategoryData, OCSFClassUID: 1}, false},
		{"no verb", audit.EventType{Name: "user", Category: audit.CategoryData, OCSFClassUID: 1}, false},
		{"three parts", audit.EventType{Name: "a.b.c", Category: audit.CategoryData, OCSFClassUID: 1}, false},
		{"unknown category", audit.EventType{Name: "a.b", Category: "misc", OCSFClassUID: 1}, false},
		{"no ocsf", audit.EventType{Name: "a.b", Category: audit.CategoryData}, false},
		{"bad pattern", audit.EventType{Name: "a.b", Category: audit.CategoryData, OCSFClassUID: 1, Fields: map[string]audit.FieldClass{"email": audit.FieldSecret}}, false},
		{"bad detail", audit.EventType{Name: "a.b", Category: audit.CategoryData, OCSFClassUID: 1, Details: map[string]audit.FieldClass{"": audit.FieldSecret}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := audit.NewRegistry()
			err := r.Register(c.t)
			if (err == nil) != c.ok {
				t.Fatalf("Register = %v, want ok=%v", err, c.ok)
			}
			if err != nil && !errs.Is(err, errs.CodeInternal) {
				t.Errorf("error code = %s", errs.CodeOf(err))
			}
			if c.ok {
				if err := r.Register(c.t); err == nil {
					t.Error("duplicate registration succeeded")
				}
				got, found := r.Lookup(c.t.Name)
				if !found || got.OCSFClassUID != c.t.OCSFClassUID {
					t.Errorf("Lookup = %+v, %v", got, found)
				}
			}
		})
	}
}

// TestBuiltInTypes: the registry starts with the module's own types.
//
// Covers: GOV-01
func TestBuiltInTypes(t *testing.T) {
	t.Parallel()
	names := audit.NewRegistry().Names()
	for _, n := range []string{audit.TypeTransactionFailed, audit.TypeDataKeyCreated} {
		if !slices.Contains(names, n) {
			t.Errorf("built-in type %s missing from %v", n, names)
		}
	}
	if _, ok := audit.NewRegistry().Lookup("nope.never"); ok {
		t.Error("Lookup found an unregistered type")
	}
}

// TestNewRequiresDeps: New refuses missing dependencies.
//
// Covers: GOV-11
func TestNewRequiresDeps(t *testing.T) {
	t.Parallel()
	if _, err := audit.New(audit.Deps{}); !errs.Is(err, errs.CodeInternal) {
		t.Errorf("New(empty) = %v", err)
	}
}
