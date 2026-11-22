package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

func pointers(err error) []string {
	var e *errs.Error
	if !errors.As(err, &e) {
		return nil
	}
	var out []string
	for _, f := range e.Fields() {
		out = append(out, f.Pointer)
	}
	return out
}

func has(list []string, p string) bool {
	for _, s := range list {
		if s == p {
			return true
		}
	}
	return false
}

// TestValidateTenant checks each rule of a new tenant and that the defaults
// are three environments named after their kinds.
//
// Covers: TEN-01
func TestValidateTenant(t *testing.T) {
	t.Parallel()
	ok := TenantSpec{Slug: "acme", Name: "Acme", Region: "eu-west-1", Plan: "standard"}
	tests := []struct {
		name    string
		mod     func(*TenantSpec)
		pointer string // empty: valid
	}{
		{"valid", func(*TenantSpec) {}, ""},
		{"three character slug", func(s *TenantSpec) { s.Slug = "abc" }, ""},
		{"two character slug", func(s *TenantSpec) { s.Slug = "ab" }, "/slug"},
		{"forty character slug", func(s *TenantSpec) { s.Slug = "a" + strings.Repeat("b", 39) }, ""},
		{"forty-one character slug", func(s *TenantSpec) { s.Slug = "a" + strings.Repeat("b", 40) }, "/slug"},
		{"slug with underscore", func(s *TenantSpec) { s.Slug = "ac_me" }, "/slug"},
		{"blank name", func(s *TenantSpec) { s.Name = " \t" }, "/name"},
		{"name 200 runes", func(s *TenantSpec) { s.Name = strings.Repeat("é", 200) }, ""},
		{"name 201 runes", func(s *TenantSpec) { s.Name = strings.Repeat("é", 201) }, "/name"},
		{"name with newline", func(s *TenantSpec) { s.Name = "a\nb" }, "/name"},
		{"region uppercase", func(s *TenantSpec) { s.Region = "EU" }, "/region"},
		{"plan 64 characters", func(s *TenantSpec) { s.Plan = strings.Repeat("p", 64) }, ""},
		{"plan 65 characters", func(s *TenantSpec) { s.Plan = strings.Repeat("p", 65) }, "/plan"},
		{"environment leading dash", func(s *TenantSpec) {
			s.Environments = []EnvSpec{{Name: "-a", Kind: KindStaging}}
		}, "/environments/0/name"},
		{"environment trailing dash", func(s *TenantSpec) {
			s.Environments = []EnvSpec{{Name: "a-", Kind: KindStaging}}
		}, "/environments/0/name"},
		{"environment empty name", func(s *TenantSpec) {
			s.Environments = []EnvSpec{{Name: "", Kind: KindStaging}}
		}, "/environments/0/name"},
		{"duplicate environment", func(s *TenantSpec) {
			s.Environments = []EnvSpec{{Name: "a", Kind: KindStaging}, {Name: "a", Kind: KindStaging}}
		}, "/environments/1/name"},
		{"unknown kind", func(s *TenantSpec) {
			s.Environments = []EnvSpec{{Name: "a", Kind: "qa"}}
		}, "/environments/0/kind"},
		{"host label overflow", func(s *TenantSpec) {
			s.Slug = "a" + strings.Repeat("b", 39)
			s.Environments = []EnvSpec{{Name: strings.Repeat("e", 23), Kind: KindStaging}}
		}, "/environments/0/name"},
		{"host label exactly 63", func(s *TenantSpec) {
			s.Slug = "a" + strings.Repeat("b", 39)
			s.Environments = []EnvSpec{{Name: strings.Repeat("e", 22), Kind: KindStaging}}
		}, ""},
		{"too many environments", func(s *TenantSpec) {
			for i := range MaxEnvironments + 1 {
				s.Environments = append(s.Environments, EnvSpec{Name: "e" + strings.Repeat("x", i), Kind: KindStaging})
			}
		}, "/environments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			spec := ok
			tt.mod(&spec)
			envs, err := ValidateTenant(spec)
			if tt.pointer == "" {
				if err != nil || len(envs) == 0 {
					t.Fatalf("err = %v, environments = %d", err, len(envs))
				}
				return
			}
			if !errs.Is(err, errs.CodeValidationFailed) || !has(pointers(err), tt.pointer) {
				t.Fatalf("err = %v, pointers %v, want a field error at %s", err, pointers(err), tt.pointer)
			}
		})
	}
	envs, err := ValidateTenant(ok)
	if err != nil || len(envs) != 3 || envs[0].Name != "development" || envs[1].Kind != "staging" || envs[2].Name != "production" {
		t.Fatalf("default environments = %+v, %v", envs, err)
	}
}

// TestValidateTenantReportsAllFields checks that every problem is reported
// at once.
func TestValidateTenantReportsAllFields(t *testing.T) {
	t.Parallel()
	_, err := ValidateTenant(TenantSpec{})
	got := pointers(err)
	for _, p := range []string{"/slug", "/name", "/region", "/plan"} {
		if !has(got, p) {
			t.Errorf("missing field error %s in %v", p, got)
		}
	}
}

// TestValidateUpdateAndReason checks the update fields and the reason of
// lifecycle changes.
func TestValidateUpdateAndReason(t *testing.T) {
	t.Parallel()
	str := func(s string) *string { return &s }
	tests := []struct {
		name    string
		err     error
		pointer string
	}{
		{"nothing to update", ValidateUpdate(nil, nil), ""},
		{"empty name", ValidateUpdate(str(""), nil), "/name"},
		{"bad plan", ValidateUpdate(nil, str("Pro Plan")), "/plan"},
		{"name and plan", ValidateUpdate(str("n"), str("pro")), "-"},
		{"empty reason", ValidateReason(""), "/reason"},
		{"blank reason", ValidateReason("  "), "/reason"},
		{"long reason", ValidateReason(strings.Repeat("r", 1001)), "/reason"},
		{"reason", ValidateReason("because"), "-"},
		{"partner name", ValidatePartner(""), "/name"},
		{"deep partner", CheckPartnerDepth(MaxPartnerDepth), "/parent_id"},
		{"partner at the limit", CheckPartnerDepth(MaxPartnerDepth - 1), "-"},
		{"top level partner", CheckPartnerDepth(0), "-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			switch tt.pointer {
			case "-":
				if tt.err != nil {
					t.Fatalf("err = %v, want none", tt.err)
				}
			default:
				if !errs.Is(tt.err, errs.CodeValidationFailed) || !has(pointers(tt.err), tt.pointer) {
					t.Fatalf("err = %v, pointers %v, want %q", tt.err, pointers(tt.err), tt.pointer)
				}
			}
		})
	}
}

// TestCheckAction checks the lifecycle transitions: active to suspended,
// suspended to active, active or suspended to deleting; everything else is a
// conflict and a deleted tenant is gone.
//
// Covers: TEN-09
func TestCheckAction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from   string
		action Action
		want   errs.Code // empty: allowed
	}{
		{TenantActive, ActionSuspend, ""},
		{TenantSuspended, ActionSuspend, errs.CodeConflict},
		{TenantSuspended, ActionResume, ""},
		{TenantActive, ActionResume, errs.CodeConflict},
		{TenantActive, ActionDelete, ""},
		{TenantSuspended, ActionDelete, ""},
		{TenantDeleting, ActionDelete, ""},
		{TenantDeleting, ActionSuspend, errs.CodeConflict},
		{TenantDeleting, ActionUpdate, errs.CodeConflict},
		{TenantActive, ActionUpdate, ""},
		{TenantSuspended, ActionUpdate, ""},
		{TenantDeleted, ActionSuspend, errs.CodeNotFound},
		{TenantDeleted, ActionResume, errs.CodeNotFound},
		{TenantDeleted, ActionDelete, errs.CodeNotFound},
		{TenantDeleted, ActionUpdate, errs.CodeNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.from+"/"+string(tt.action), func(t *testing.T) {
			t.Parallel()
			err := CheckAction(tt.from, tt.action)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if !errs.Is(err, tt.want) {
				t.Fatalf("err = %v, want %s", err, tt.want)
			}
		})
	}
	err := CheckAction(TenantSuspended, ActionSuspend)
	if !strings.Contains(err.Error(), "cannot be suspended") {
		t.Errorf("message = %q", err)
	}
}

// TestHosts checks host normalization and the default host names.
//
// Covers: TEN-01
func TestHosts(t *testing.T) {
	t.Parallel()
	norm := []struct{ in, want string }{
		{"acme.example.com", "acme.example.com"},
		{"ACME.Example.COM", "acme.example.com"},
		{"acme.example.com:8443", "acme.example.com"},
		{"acme.example.com.", "acme.example.com"},
		{"acme.example.com.:443", "acme.example.com"},
		{"  acme.example.com  ", "acme.example.com"},
		{"localhost", "localhost"},
	}
	for _, tt := range norm {
		got, err := NormalizeHost(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("NormalizeHost(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
	for _, in := range []string{
		"", ".", "a..b", "a b", "a.example:port", "a.example:99999", "[::1]:80", "-a.example", "a-.example",
		strings.Repeat("a", 64) + ".example", strings.Repeat("a.", 130) + "example", "a_b.example", "exämple.com",
	} {
		if _, err := NormalizeHost(in); !errs.Is(err, errs.CodeNotFound) {
			t.Errorf("NormalizeHost(%q) err = %v, want not found", in, err)
		}
	}
	if h, err := ValidateHost("Login.Acme.Example"); err != nil || h != "login.acme.example" {
		t.Errorf("ValidateHost = %q, %v", h, err)
	}
	for _, in := range []string{"login.acme.example:443", "bad host", ""} {
		if _, err := ValidateHost(in); !errs.Is(err, errs.CodeValidationFailed) || !has(pointers(err), "/host") {
			t.Errorf("ValidateHost(%q) err = %v", in, err)
		}
	}
	if got := DefaultHost("acme", "production", "portun.example"); got != "acme-production.portun.example" {
		t.Errorf("DefaultHost = %q", got)
	}
	if err := ValidateBaseDomain("portun.example"); err != nil {
		t.Errorf("valid base domain: %v", err)
	}
	for _, bad := range []string{"", "ab", "Portun.Example", "bad domain", strings.Repeat("a.", 100)} {
		if err := ValidateBaseDomain(bad); !errs.Is(err, errs.CodeInternal) {
			t.Errorf("ValidateBaseDomain(%q) err = %v", bad, err)
		}
	}
}
