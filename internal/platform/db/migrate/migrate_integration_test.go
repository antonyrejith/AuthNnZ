//go:build integration

package migrate_test

import (
	"context"
	"embed"
	"io/fs"
	"net/url"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

//go:embed testdata
var testdata embed.FS

func sets(tb testing.TB) []migrate.Set {
	tb.Helper()
	sub := func(dir string) fs.FS {
		s, err := fs.Sub(testdata, "testdata/"+dir)
		if err != nil {
			tb.Fatal(err)
		}
		return s
	}
	return []migrate.Set{{Schema: "dbtest_app", FS: sub("app")}, {Schema: "dbtest_aux", FS: sub("settings")}}
}

func TestMain(m *testing.M) {
	// The template stays empty: these tests run the migrations themselves.
	os.Exit(dbtest.Main(m, dbtest.Options{}))
}

// TestUpIsIdempotentWithOneVersionTablePerSchema applies two sets twice and
// checks the version tables and the result.
func TestUpIsIdempotentWithOneVersionTablePerSchema(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	for range 2 {
		if err := migrate.Up(t.Context(), env.OwnerURL, sets(t)...); err != nil {
			t.Fatal(err)
		}
	}
	conn := connect(t, env.OwnerURL)
	rows, err := conn.Query(t.Context(), `SELECT schemaname || '.' || tablename FROM pg_tables WHERE tablename = $1 ORDER BY 1`, migrate.VersionTable)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dbtest_app.goose_db_version", "dbtest_aux.goose_db_version"}
	if len(tables) != 2 || tables[0] != want[0] || tables[1] != want[1] {
		t.Fatalf("version tables = %v, want %v", tables, want)
	}
	tests := []struct {
		query string
		want  int
	}{
		{`SELECT max(version_id) FROM dbtest_app.goose_db_version`, 1},
		{`SELECT max(version_id) FROM dbtest_aux.goose_db_version`, 2},
		{`SELECT count(*) FROM dbtest_aux.goose_db_version WHERE version_id > 0`, 2}, // each applied once
		{`SELECT count(*) FROM dbtest_aux.settings`, 1},                              // seed ran once
	}
	for _, tt := range tests {
		var got int
		if err := conn.QueryRow(t.Context(), tt.query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != tt.want {
			t.Errorf("%s = %d, want %d", tt.query, got, tt.want)
		}
	}
	dbtest.AssertRLS(t, env, "dbtest_app", "dbtest_aux")
}

// TestUpConcurrent runs Up from several goroutines at once; the advisory
// lock makes each migration apply exactly once.
func TestUpConcurrent(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	ss := sets(t)
	errc := make(chan error, 4)
	for range cap(errc) {
		go func() { errc <- migrate.Up(context.Background(), env.OwnerURL, ss...) }()
	}
	for range cap(errc) {
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := connect(t, env.OwnerURL).QueryRow(t.Context(), `SELECT count(*) FROM dbtest_aux.settings`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("seed rows = %d (err %v), want 1", n, err)
	}
}

// TestEnsureRoles creates login roles, re-runs the call (idempotent, password
// reset) and checks attributes and that the password works.
//
// Covers: SEC-09
func TestEnsureRoles(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	tests := []struct {
		name      string
		bypassRLS bool
	}{
		{"app_like", false},
		{"platform_like", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			role := "t_" + tt.name + "_" + uuid.NewString()[:8]
			admin := connect(t, env.AdminURL)
			t.Cleanup(func() {
				_, _ = admin.Exec(context.Background(), `DROP ROLE IF EXISTS `+pgx.Identifier{role}.Sanitize())
			})
			for _, pw := range []string{"first-password-0001", "second-password-002"} {
				if err := migrate.EnsureLoginRole(t.Context(), env.AdminURL, role, tt.bypassRLS, secret.NewString(pw)); err != nil {
					t.Fatal(err)
				}
				var super, bypass, login, createRole bool
				if err := admin.QueryRow(t.Context(), `SELECT rolsuper, rolbypassrls, rolcanlogin, rolcreaterole FROM pg_roles WHERE rolname = $1`, role).
					Scan(&super, &bypass, &login, &createRole); err != nil {
					t.Fatal(err)
				}
				if super || createRole || !login || bypass != tt.bypassRLS {
					t.Fatalf("attributes super=%v bypass=%v login=%v createrole=%v", super, bypass, login, createRole)
				}
				connect(t, withUser(t, env.AdminURL, role, pw))
			}
		})
	}
	// The real roles exist with the secure attributes (created by Main).
	var appSafe, platformSafe bool
	if err := connect(t, env.AdminURL).QueryRow(t.Context(), `
		SELECT bool_and(NOT rolsuper AND NOT rolbypassrls) FILTER (WHERE rolname = $1),
		       bool_and(NOT rolsuper AND rolbypassrls) FILTER (WHERE rolname = $2)
		FROM pg_roles`, db.AppRole, db.PlatformRole).Scan(&appSafe, &platformSafe); err != nil {
		t.Fatal(err)
	}
	if !appSafe || !platformSafe {
		t.Fatalf("roles: app safe %v, platform safe %v", appSafe, platformSafe)
	}
}

// TestEnsureRolesResetsSettingsAndMemberships proves that EnsureRoles
// removes role-level and database-level settings (a default tenant) and
// revokes memberships (a path to a more privileged role) of an existing role.
//
// Covers: SEC-09
func TestEnsureRolesResetsSettingsAndMemberships(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	role := "t_reset_" + uuid.NewString()[:8]
	admin := connect(t, env.AdminURL)
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DROP ROLE IF EXISTS `+pgx.Identifier{role}.Sanitize())
	})
	const pw = "first-password-0001"
	if err := migrate.EnsureLoginRole(t.Context(), env.AdminURL, role, false, secret.NewString(pw)); err != nil {
		t.Fatal(err)
	}
	r := pgx.Identifier{role}.Sanitize()
	for _, stmt := range []string{
		`ALTER ROLE ` + r + ` SET app.tenant_id = '` + uuid.NewString() + `'`,
		`ALTER ROLE ` + r + ` IN DATABASE ` + pgx.Identifier{env.Name}.Sanitize() + ` SET app.environment_id = '` + uuid.NewString() + `'`,
		`GRANT pg_read_all_data TO ` + r,
		`GRANT pg_monitor TO ` + r,
	} {
		if _, err := admin.Exec(t.Context(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	count := func() (settings, memberships int) {
		t.Helper()
		if err := admin.QueryRow(t.Context(), `
			SELECT (SELECT count(*) FROM pg_db_role_setting WHERE setrole = r.oid),
			       (SELECT count(*) FROM pg_auth_members WHERE member = r.oid)
			FROM pg_roles r WHERE r.rolname = $1`, role).Scan(&settings, &memberships); err != nil {
			t.Fatal(err)
		}
		return settings, memberships
	}
	if s, m := count(); s != 2 || m != 2 {
		t.Fatalf("setup: %d settings, %d memberships; want 2 and 2", s, m)
	}
	if err := migrate.EnsureLoginRole(t.Context(), env.AdminURL, role, false, secret.NewString(pw)); err != nil {
		t.Fatal(err)
	}
	if s, m := count(); s != 0 || m != 0 {
		t.Fatalf("after EnsureRoles: %d settings, %d memberships; want none", s, m)
	}
}

func connect(t *testing.T, u secret.String) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), u.Reveal())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func withUser(t *testing.T, s secret.String, user, password string) secret.String {
	t.Helper()
	u, err := url.Parse(s.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(user, password)
	return secret.NewString(u.String())
}
