//go:build integration

package migrate_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"

	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// TestEnsureRolesNeedsPrivilege: connecting as the application role, which
// may not create roles, fails with an error code and creates no role.
//
// Covers: SEC-09
func TestEnsureRolesNeedsPrivilege(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	role := "t_nopriv_" + uuid.NewString()[:8]
	err := migrate.EnsureLoginRole(t.Context(), env.AppURL, role, false, secret.NewString("first-password-0001"))
	if err == nil {
		t.Fatal("EnsureLoginRole succeeded as the application role")
	}
	if code := errs.CodeOf(err); !code.Known() {
		t.Errorf("error %v has no platform code", err)
	}
	var exists bool
	if err := connect(t, env.AdminURL).QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("role was created despite the failure")
	}
}

// TestUpLogsAppliedMigrationsOnly: each applied migration is logged with its
// schema and version, and a second run applies and logs nothing.
func TestUpLogsAppliedMigrationsOnly(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	set := migrate.Set{Schema: "log_probe", FS: fstest.MapFS{
		"00001_a.sql": {Data: []byte("-- +goose Up\nCREATE TABLE log_probe.a (id int);\n")},
		"00002_b.sql": {Data: []byte("-- +goose Up\nCREATE TABLE log_probe.b (id int);\n")},
	}}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	if err := migrate.Up(t.Context(), logger, env.OwnerURL, set); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if n := strings.Count(out, "database migration applied"); n != 2 {
		t.Fatalf("%d applied-migration records, want 2:\n%s", n, out)
	}
	for _, want := range []string{"schema=log_probe", "version=1", "version=2"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	buf.Reset()
	if err := migrate.Up(t.Context(), logger, env.OwnerURL, set); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("second run logged: %s", buf.String())
	}
}
