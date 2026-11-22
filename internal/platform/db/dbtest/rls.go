//go:build integration

package dbtest

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
)

// tenantPolicyPattern matches the policy expression of the conventions in
// pg_get_expr form: (tenant_id = (current_setting('app.tenant_id'::text))::uuid).
const tenantPolicyPattern = `%current_setting(''app.tenant_id''%`

// rlsQuery lists every table with a tenant_id column in the given schemas
// (all non-system schemas when none are given) with the facts AssertRLS
// checks.
const rlsQuery = `
SELECT n.nspname, c.relname, c.relrowsecurity, c.relforcerowsecurity,
       pg_get_userbyid(c.relowner),
       (SELECT a2.attname FROM pg_index i
          JOIN pg_attribute a2 ON a2.attrelid = i.indrelid AND a2.attnum = i.indkey[0]
         WHERE i.indrelid = c.oid AND i.indisprimary),
       (SELECT count(*) FROM pg_policy p
         WHERE p.polrelid = c.oid AND p.polcmd = '*' AND p.polpermissive
           AND pg_get_expr(p.polqual, p.polrelid) LIKE '` + tenantPolicyPattern + `'
           AND (p.polwithcheck IS NULL OR pg_get_expr(p.polwithcheck, p.polrelid) LIKE '` + tenantPolicyPattern + `')),
       (SELECT count(*) FROM pg_policy p
         WHERE p.polrelid = c.oid AND p.polpermissive
           AND (coalesce(pg_get_expr(p.polqual, p.polrelid), '') NOT LIKE '` + tenantPolicyPattern + `'
                OR (p.polwithcheck IS NOT NULL AND pg_get_expr(p.polwithcheck, p.polrelid) NOT LIKE '` + tenantPolicyPattern + `')))
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attname = 'tenant_id' AND NOT a.attisdropped
WHERE c.relkind IN ('r', 'p')
  AND n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg\_%'
  AND (cardinality($1::text[]) = 0 OR n.nspname = ANY($1))
ORDER BY 1, 2`

// AssertRLS fails the test for every tenant-owned table (a table with a
// tenant_id column) in schemas that breaks the conventions of package
// migrate: row-level security not enabled or not forced; no permissive
// policy for all commands that restricts reads and writes to
// current_setting('app.tenant_id'); any other permissive policy that does
// not; tenant_id not the first primary key column; or the table owned by the
// application role. With no schemas it checks every non-system schema; a
// named schema that does not exist is also a failure.
func AssertRLS(tb testing.TB, env *Env, schemas ...string) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	appCfg, err := pgx.ParseConfig(env.AppURL.Reveal())
	if err != nil {
		tb.Fatal("dbtest: AssertRLS: invalid application URL")
	}
	conn, err := pgx.Connect(ctx, env.OwnerURL.Reveal())
	if err != nil {
		tb.Fatalf("dbtest: AssertRLS: connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	problems, err := rlsProblems(ctx, conn, appCfg.User, schemas)
	if err != nil {
		tb.Fatalf("dbtest: AssertRLS: %v", err)
	}
	for _, p := range problems {
		tb.Errorf("row-level security: %s", p)
	}
}

func rlsProblems(ctx context.Context, conn *pgx.Conn, appRole string, schemas []string) ([]string, error) {
	if schemas == nil {
		schemas = []string{}
	}
	var problems []string
	rows, err := conn.Query(ctx,
		`SELECT s FROM unnest($1::text[]) s WHERE NOT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = s)`, schemas)
	if err != nil {
		return nil, fmt.Errorf("check schemas: %w", err)
	}
	missing, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("check schemas: %w", err)
	}
	for _, s := range missing {
		problems = append(problems, fmt.Sprintf("schema %q does not exist", s))
	}
	rows, err = conn.Query(ctx, rlsQuery, schemas)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	type table struct {
		schema, name           string
		enabled, forced        bool
		owner                  string
		pkFirst                *string
		tenantPolicies, others int64
	}
	tables, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (table, error) {
		var tb table
		err := r.Scan(&tb.schema, &tb.name, &tb.enabled, &tb.forced, &tb.owner, &tb.pkFirst, &tb.tenantPolicies, &tb.others)
		if err != nil {
			return tb, fmt.Errorf("scan: %w", err)
		}
		return tb, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	for _, tb := range tables {
		add := func(bad bool, format string, args ...any) {
			if bad {
				problems = append(problems, tb.schema+"."+tb.name+": "+fmt.Sprintf(format, args...))
			}
		}
		add(!tb.enabled, "row-level security is not enabled")
		add(!tb.forced, "row-level security is not forced")
		add(tb.tenantPolicies == 0, "no permissive policy for all commands restricts rows to current_setting('app.tenant_id')")
		add(tb.others > 0, "%d permissive policies do not restrict rows to current_setting('app.tenant_id')", tb.others)
		add(tb.pkFirst == nil, "has no primary key")
		add(tb.pkFirst != nil && *tb.pkFirst != "tenant_id", "tenant_id is not the first primary key column")
		add(tb.owner == appRole, "is owned by the application role %q", appRole)
	}
	return problems, nil
}
