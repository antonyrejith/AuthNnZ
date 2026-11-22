//go:build integration

package dbtest_test

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
)

//go:embed testdata/app/*.sql
var appMigrations embed.FS

func TestMain(m *testing.M) {
	sub, err := fs.Sub(appMigrations, "testdata/app")
	if err != nil {
		panic(err)
	}
	os.Exit(dbtest.Main(m, dbtest.Options{Sets: []migrate.Set{{Schema: "dbtest_app", FS: sub}}}))
}

// recorder captures Errorf calls; everything else goes to the real test.
type recorder struct {
	testing.TB
	mu     sync.Mutex
	errors []string
	logs   []string
}

func (r *recorder) Logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recorder) Error(args ...any) { r.Errorf("%s", fmt.Sprint(args...)) }

// TestAssertRLSAcceptsConventions proves the template's migrated tables
// (tenant and environment scoped) pass, including the behavioural check,
// and the per-test database is isolated and usable.
//
// Covers: TEN-03
func TestAssertRLSAcceptsConventions(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	r := &recorder{TB: t}
	dbtest.AssertRLS(r, env, "dbtest_app")
	dbtest.AssertRLS(r, env) // all schemas
	if len(r.errors) > 0 {
		t.Fatalf("AssertRLS reported %v", r.errors)
	}
	if len(r.logs) > 0 {
		t.Fatalf("AssertRLS skipped checks: %v", r.logs)
	}
	if err := env.DB.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestAssertRLSCatchesViolations creates tables that break each rule and
// checks that AssertRLS reports them, including the leaks an independent
// review proved the earlier pattern match accepted.
//
// Covers: TEN-03
func TestAssertRLSCatchesViolations(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	const policy = `CREATE POLICY p ON %[1]s.t USING (tenant_id = current_setting('app.tenant_id')::uuid)
		WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);`
	const table = `CREATE TABLE %[1]s.t (tenant_id uuid NOT NULL, id uuid NOT NULL, PRIMARY KEY (tenant_id, id));
		ALTER TABLE %[1]s.t ENABLE ROW LEVEL SECURITY; ALTER TABLE %[1]s.t FORCE ROW LEVEL SECURITY;`
	const grants = `GRANT USAGE ON SCHEMA %[1]s TO portun_app; GRANT SELECT, INSERT, UPDATE, DELETE ON %[1]s.t TO portun_app;`
	tests := []struct {
		name   string
		schema string
		ddl    string
		want   string
		owner  string // run the DDL as this URL: "admin" or the owner (default)
	}{
		{"no force", "bad_force", `CREATE TABLE %[1]s.t (tenant_id uuid NOT NULL, id uuid NOT NULL, PRIMARY KEY (tenant_id, id));
			ALTER TABLE %[1]s.t ENABLE ROW LEVEL SECURITY;` + policy, "not forced", ""},
		{"not enabled", "bad_enable", `CREATE TABLE %[1]s.t (tenant_id uuid NOT NULL, id uuid NOT NULL, PRIMARY KEY (tenant_id, id));
			ALTER TABLE %[1]s.t FORCE ROW LEVEL SECURITY;` + policy, "not enabled", ""},
		{"no policy", "bad_policy", `CREATE TABLE %[1]s.t (tenant_id uuid NOT NULL, id uuid NOT NULL, PRIMARY KEY (tenant_id, id));
			ALTER TABLE %[1]s.t ENABLE ROW LEVEL SECURITY; ALTER TABLE %[1]s.t FORCE ROW LEVEL SECURITY;`, "no permissive policy", ""},
		{"permissive leak", "bad_leak", `CREATE TABLE %[1]s.t (tenant_id uuid NOT NULL, id uuid NOT NULL, PRIMARY KEY (tenant_id, id));
			ALTER TABLE %[1]s.t ENABLE ROW LEVEL SECURITY; ALTER TABLE %[1]s.t FORCE ROW LEVEL SECURITY;` + policy +
			`CREATE POLICY leak ON %[1]s.t FOR SELECT USING (true);`, "for command r", ""},
		{
			"pk order", "bad_pk", `CREATE TABLE %[1]s.t (tenant_id uuid NOT NULL, id uuid NOT NULL, PRIMARY KEY (id, tenant_id));
			ALTER TABLE %[1]s.t ENABLE ROW LEVEL SECURITY; ALTER TABLE %[1]s.t FORCE ROW LEVEL SECURITY;` + policy,
			"not the first primary key column", "",
		},
		{
			"no pk", "bad_nopk", `CREATE TABLE %[1]s.t (tenant_id uuid NOT NULL, id uuid NOT NULL);
			ALTER TABLE %[1]s.t ENABLE ROW LEVEL SECURITY; ALTER TABLE %[1]s.t FORCE ROW LEVEL SECURITY;` + policy,
			"no primary key", "",
		},
		{"owned by app", "bad_owner", `CREATE TABLE %[1]s.t (tenant_id uuid NOT NULL, id uuid NOT NULL, PRIMARY KEY (tenant_id, id));
			ALTER TABLE %[1]s.t ENABLE ROW LEVEL SECURITY; ALTER TABLE %[1]s.t FORCE ROW LEVEL SECURITY;` + policy +
			`ALTER TABLE %[1]s.t OWNER TO portun_app;`, "owned by the application role", "admin"},
		{"or true", "bad_ortrue", table + `CREATE POLICY p ON %[1]s.t
			USING (tenant_id = current_setting('app.tenant_id')::uuid OR true)
			WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);` + grants, "does not restrict rows", ""},
		{"or true behaviour", "bad_ortrue2", table + `CREATE POLICY p ON %[1]s.t
			USING (tenant_id = current_setting('app.tenant_id')::uuid OR true);` + grants, "rows of another tenant", ""},
		{"wrong column", "bad_column", table + `CREATE POLICY p ON %[1]s.t
			USING (id = current_setting('app.tenant_id')::uuid)
			WITH CHECK (id = current_setting('app.tenant_id')::uuid);` + grants, "does not restrict rows", ""},
		{"missing_ok is null", "bad_missingok", table + `CREATE POLICY p ON %[1]s.t
			USING (current_setting('app.tenant_id', true) IS NULL OR tenant_id = current_setting('app.tenant_id', true)::uuid)
			WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);` + grants, "does not restrict rows", ""},
		{"loose with check", "bad_check", table + `CREATE POLICY p ON %[1]s.t
			USING (tenant_id = current_setting('app.tenant_id')::uuid)
			WITH CHECK (true);` + grants, "does not restrict writes", ""},
		{"truncate", "bad_truncate", table + policy + grants + `GRANT TRUNCATE ON %[1]s.t TO portun_app;`, "has TRUNCATE", ""},
		{"trigger", "bad_trigger", table + policy + grants + `GRANT TRIGGER ON %[1]s.t TO portun_app;`, "has TRIGGER", ""},
		{"references", "bad_refs", table + policy + grants + `GRANT REFERENCES ON %[1]s.t TO portun_app;`, "has REFERENCES", ""},
		{"policy for another role", "bad_role", table + `CREATE POLICY p ON %[1]s.t TO portun_platform
			USING (tenant_id = current_setting('app.tenant_id')::uuid)
			WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);` + grants, "no permissive policy", ""},
		{"policy per command", "bad_cmd", table + `CREATE POLICY p ON %[1]s.t FOR SELECT
			USING (tenant_id = current_setting('app.tenant_id')::uuid);` + grants, "for command r", ""},
		{"two tenant policies", "bad_two", table + policy + `CREATE POLICY p2 ON %[1]s.t
			USING (tenant_id = current_setting('app.tenant_id')::uuid);` + grants, "want exactly one", ""},
		{
			"environment clause missing", "bad_env", `CREATE TABLE %[1]s.t (tenant_id uuid NOT NULL, environment_id uuid NOT NULL,
			id uuid NOT NULL, PRIMARY KEY (tenant_id, id));
			ALTER TABLE %[1]s.t ENABLE ROW LEVEL SECURITY; ALTER TABLE %[1]s.t FORCE ROW LEVEL SECURITY;` + policy + grants,
			"want exactly ((tenant_id", "",
		},
		{"missing schema", "no_such_schema", "", "does not exist", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.ddl != "" {
				url := env.OwnerURL
				if tt.owner == "admin" {
					url = env.AdminURL
				}
				conn, err := pgx.Connect(t.Context(), url.Reveal())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = conn.Close(context.Background()) }()
				if _, err := conn.Exec(t.Context(), fmt.Sprintf("CREATE SCHEMA %[1]s;"+tt.ddl, tt.schema)); err != nil {
					t.Fatal(err)
				}
			}
			r := &recorder{TB: t}
			dbtest.AssertRLS(r, env, tt.schema)
			if !anyContains(r.errors, tt.want) {
				t.Fatalf("AssertRLS reported %q, want a problem containing %q", r.errors, tt.want)
			}
		})
	}
}

func anyContains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// TestAssertRLSNotesSkippedBehaviour proves that a table whose test rows
// cannot be inserted still gets the static checks and that the skipped
// behavioural check is reported in the test log with the reason.
//
// Covers: TEN-03
func TestAssertRLSNotesSkippedBehaviour(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	conn, err := pgx.Connect(t.Context(), env.OwnerURL.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(t.Context(), `CREATE SCHEMA skip_fk;
		CREATE TABLE skip_fk.parent (id uuid PRIMARY KEY);
		CREATE TABLE skip_fk.t (tenant_id uuid NOT NULL, id uuid NOT NULL,
			parent_id uuid NOT NULL REFERENCES skip_fk.parent (id), PRIMARY KEY (tenant_id, id));
		ALTER TABLE skip_fk.t ENABLE ROW LEVEL SECURITY; ALTER TABLE skip_fk.t FORCE ROW LEVEL SECURITY;
		CREATE POLICY p ON skip_fk.t USING (tenant_id = current_setting('app.tenant_id')::uuid);
		GRANT USAGE ON SCHEMA skip_fk TO portun_app; GRANT SELECT ON skip_fk.t TO portun_app;`); err != nil {
		t.Fatal(err)
	}
	r := &recorder{TB: t}
	dbtest.AssertRLS(r, env, "skip_fk")
	if len(r.errors) > 0 {
		t.Fatalf("AssertRLS reported %v", r.errors)
	}
	if !anyContains(r.logs, "behavioural check skipped: cannot insert test rows") {
		t.Fatalf("logs = %q, want a skipped behavioural check with its reason", r.logs)
	}
}
