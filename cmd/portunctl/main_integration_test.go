//go:build integration

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(m, dbtest.Options{Sets: []migrate.Set{audit.StoreMigrations()}}))
}

// TestAuditCommands exports, verifies and tampers with the platform trail
// through the CLI.
//
// Covers: GOV-16, GOV-18
func TestAuditCommands(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	url := env.PlatformURL.Reveal()
	dir := t.TempDir()
	file := filepath.Join(dir, "export.jsonl")
	runOK := func(want int, args ...string) string {
		t.Helper()
		var out, errOut bytes.Buffer
		if code := run(t.Context(), args, &out, &errOut); code != want {
			t.Fatalf("portunctl %v = %d, want %d\nstdout: %s\nstderr: %s", args, code, want, out.String(), errOut.String())
		}
		return out.String()
	}
	out := runOK(exitOK, "audit", "verify", "--db-url", url, "--actor", "ops", "--reason", "check")
	if !strings.Contains(out, "chain platform: OK") {
		t.Errorf("verify output: %s", out)
	}
	runOK(exitOK, "audit", "export", "--db-url", url, "--actor", "ops", "--out", file)
	out = runOK(exitOK, "audit", "verify", "--file", file, "--json")
	if !strings.Contains(out, `"failures": null`) && !strings.Contains(out, `"failures": []`) {
		t.Errorf("json output: %s", out)
	}
	runOK(exitOK, "audit", "verify", "--db-url", url, "--actor", "ops", "--from", "1", "--to", "2026-01-01T00:00:00Z")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	tampered := filepath.Join(dir, "tampered.jsonl")
	if err := os.WriteFile(tampered, bytes.Replace(data, []byte(`"ops"`), []byte(`"eve"`), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	out = runOK(exitFailures, "audit", "verify", "--file", tampered)
	if !strings.Contains(out, "FAILED") || !strings.Contains(out, "hash_mismatch") {
		t.Errorf("tampered output: %s", out)
	}
	runOK(exitOK, "audit", "keys", "--db-url", url)
	keys := filepath.Join(dir, "keys.pem")
	if err := os.WriteFile(keys, []byte("none"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{},
		{"audit"},
		{"audit", "nope"},
		{"audit", "verify", "--bogus"},
		{"audit", "verify", "--from", "x"},
		{"audit", "verify", "--to", "x"},
		{"audit", "verify", "--file", filepath.Join(dir, "missing")},
		{"audit", "verify", "--file", file, "--trusted-keys", keys},
		{"audit", "verify", "--file", file, "--trusted-keys", filepath.Join(dir, "missing")},
		{"audit", "verify", "--file", file, "--tsa-roots", keys},
		{"audit", "verify", "--file", file, "--tsa-roots", filepath.Join(dir, "missing")},
		{"audit", "export", "--db-url", url, "--out", file},
		{"audit", "export", "--bogus"},
		{"audit", "keys", "--bogus"},
		{"audit", "keys", "--db-url", "::"},
	} {
		runOK(exitError, args...)
	}
}
