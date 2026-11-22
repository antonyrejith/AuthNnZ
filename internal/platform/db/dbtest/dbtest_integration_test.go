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
}

func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recorder) Error(args ...any) { r.Errorf("%s", fmt.Sprint(args...)) }

// TestAssertRLSAcceptsConventions proves the template's migrated table
// passes and the per-test database is isolated and usable.
//
// Covers: TEN-01
func TestAssertRLSAcceptsConventions(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	r := &recorder{TB: t}
	dbtest.AssertRLS(r, env, "dbtest_app")
	dbtest.AssertRLS(r, env) // all schemas
	if len(r.errors) > 0 {
		t.Fatalf("AssertRLS reported %v", r.errors)
	}
	if err := env.DB.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestAssertRLSCatchesViolations creates tables that break each rule and
// checks that AssertRLS reports them.
//
// Covers: TEN-01
func TestAssertRLSCatchesViolations(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	const policy = `CREATE POLICY p ON %[1]s.t USING (tenant_id = current_setting('app.tenant_id')::uuid)
		WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);`
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
			`CREATE POLICY leak ON %[1]s.t FOR SELECT USING (true);`, "do not restrict", ""},
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
