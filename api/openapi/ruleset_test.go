package openapi_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// vacuumPath returns bin/vacuum of the repository (built by make tools), or
// skips the test with the reason.
func vacuumPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "bin", "vacuum"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Skipf("bin/vacuum not found (%v): run `make tools` to build it; the ruleset is not tested", err)
	}
	return p
}

// lint runs vacuum with the Portun ruleset on spec and returns its exit
// success and combined output.
func lint(t *testing.T, vacuum, spec string) (bool, string) {
	t.Helper()
	ruleset, err := filepath.Abs("ruleset.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), vacuum, "lint", "--no-update-check", "--no-banner", "--errors", "--ruleset", ruleset, spec)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("run vacuum: %v", err)
	}
	return err == nil, out.String()
}

// TestRuleset: the Platform specification passes the Portun ruleset, and a
// copy that breaks one convention each fails it: a missing operationId,
// x-required-permission or x-audit-event, a malformed value, an error
// response that is not application/problem+json, and an error response whose
// schema is not Problem.
//
// Covers: GOV-11
func TestRuleset(t *testing.T) {
	t.Parallel()
	vacuum := vacuumPath(t)
	spec, err := os.ReadFile(filepath.Join("platform", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	original := string(spec)

	if ok, out := lint(t, vacuum, filepath.Join("platform", "openapi.yaml")); !ok {
		t.Fatalf("the Platform specification fails its own ruleset:\n%s", out)
	}

	mutate := func(old, replacement string) string {
		if strings.Count(original, old) < 1 {
			t.Fatalf("the specification has no %q to break", old)
		}
		return strings.Replace(original, old, replacement, 1)
	}
	tests := []struct {
		name string
		spec string
	}{
		{"no x-audit-event", mutate("      x-audit-event: none\n", "")},
		{"no x-required-permission", mutate("      x-required-permission: platform.partners.write\n", "")},
		{"no operationId", mutate("      operationId: createPartner\n", "")},
		{"malformed permission", mutate("x-required-permission: platform.partners.write", "x-required-permission: Everything")},
		{"malformed audit event", mutate("x-audit-event: partner.created", "x-audit-event: Partner Created")},
		{"audit reason other than required", mutate("      x-audit-reason: required", "      x-audit-reason: optional")},
		{"error response not problem+json", mutate("    NotFound:\n      description: The resource does not exist (`not_found`).\n      content:\n        application/problem+json:", "    NotFound:\n      description: The resource does not exist (`not_found`).\n      content:\n        application/json:")},
		{"error response with another schema", mutate("    Conflict:\n      description: The request conflicts with the state of the resource (`conflict`).\n      content:\n        application/problem+json:\n          schema:\n            $ref: '#/components/schemas/Problem'", "    Conflict:\n      description: The request conflicts with the state of the resource (`conflict`).\n      content:\n        application/problem+json:\n          schema:\n            $ref: '#/components/schemas/Tenant'")},
	}
	dir := t.TempDir()
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(dir, strings.ReplaceAll(strings.ToLower(tt.name), " ", "-")+string(rune('a'+i))+".yaml")
			if err := os.WriteFile(path, []byte(tt.spec), 0o600); err != nil {
				t.Fatal(err)
			}
			if ok, out := lint(t, vacuum, path); ok {
				t.Fatalf("vacuum accepted a specification with %s:\n%s", tt.name, out)
			}
		})
	}
}
