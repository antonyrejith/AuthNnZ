package main

import (
	"strings"
	"testing"
)

func TestViolation(t *testing.T) {
	t.Parallel()
	const m = "example.com/p/internal/"
	tests := []struct {
		name, from, imp string
		want            string
	}{
		{"domain stdlib", m + "modules/users/internal/domain", "time", ""},
		{"domain ids", m + "modules/users/internal/domain/user", m + "platform/ids", ""},
		{"domain errs", m + "modules/users/internal/domain", m + "platform/errs", ""},
		{"domain db", m + "modules/users/internal/domain", m + "platform/db", "domain layer"},
		{"domain third party", m + "modules/users/internal/domain", "github.com/google/uuid", "domain layer"},
		{"app adapters same module", m + "modules/users/internal/app", m + "modules/users/internal/adapters/pg", "application layer"},
		{"app adapters other module", m + "modules/users/internal/app/cmds", m + "modules/orgs/internal/adapters", "application layer"},
		{"app domain", m + "modules/users/internal/app", m + "modules/users/internal/domain", ""},
		{"adapters app", m + "modules/users/internal/adapters", m + "modules/users/internal/app", ""},
		{"platform module", m + "platform/httpx", m + "modules/users", "platform packages"},
		{"platform platform", m + "platform/httpx", m + "platform/errs", ""},
		{"module platform", m + "modules/users", m + "platform/db", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := violation(tt.from, tt.imp)
			if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
				t.Fatalf("violation(%q, %q) = %q, want %q", tt.from, tt.imp, got, tt.want)
			}
		})
	}
}

func TestDecodeAndCheck(t *testing.T) {
	t.Parallel()
	in := `{"ImportPath":"x/internal/platform/a","Imports":["fmt","x/internal/modules/u"]}
{"ImportPath":"x/internal/platform/b","Imports":["fmt"]}`
	pkgs, err := decode(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	got := check(pkgs)
	if len(got) != 1 || !strings.Contains(got[0], "platform/a imports x/internal/modules/u") {
		t.Fatalf("check() = %v", got)
	}
}
