//go:build integration

package dbtest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The policy expressions of the conventions of package migrate, exactly as
// PostgreSQL deparses them with pg_get_expr. A policy written in the
// documented form deparses to these strings; anything else (an extra OR, a
// different column, missing_ok, COALESCE, the clauses in another order) does
// not, and AssertRLS rejects it.
const (
	// TenantPolicyExpr restricts rows to the transaction's tenant.
	TenantPolicyExpr = `(tenant_id = (current_setting('app.tenant_id'::text))::uuid)`
	// EnvironmentPolicyExpr restricts rows of a table with an
	// environment_id column to the transaction's tenant and environment.
	EnvironmentPolicyExpr = `((tenant_id = (current_setting('app.tenant_id'::text))::uuid) AND ` +
		`(environment_id = (current_setting('app.environment_id'::text))::uuid))`
)

// rlsTablesQuery lists every table with a tenant_id column in the given
// schemas (all non-system schemas when none are given) with the facts
// AssertRLS checks. $2 is the application role.
const rlsTablesQuery = `
SELECT c.oid, n.nspname, c.relname, c.relrowsecurity, c.relforcerowsecurity,
       pg_get_userbyid(c.relowner),
       (SELECT a2.attname FROM pg_index i
          JOIN pg_attribute a2 ON a2.attrelid = i.indrelid AND a2.attnum = i.indkey[0]
         WHERE i.indrelid = c.oid AND i.indisprimary),
       EXISTS (SELECT 1 FROM pg_attribute e
                WHERE e.attrelid = c.oid AND e.attname = 'environment_id' AND NOT e.attisdropped),
       has_table_privilege($2, c.oid, 'TRUNCATE'),
       has_table_privilege($2, c.oid, 'TRIGGER'),
       has_table_privilege($2, c.oid, 'REFERENCES')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attname = 'tenant_id' AND NOT a.attisdropped
WHERE c.relkind IN ('r', 'p')
  AND n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg\_%'
  AND (cardinality($1::text[]) = 0 OR n.nspname = ANY($1))
ORDER BY 2, 3`

// rlsPoliciesQuery lists the permissive policies of table $1 that apply to
// role $2: policies for PUBLIC or for a role $2 is a member of.
const rlsPoliciesQuery = `
SELECT p.polname, p.polcmd::text,
       coalesce(pg_get_expr(p.polqual, p.polrelid), ''),
       pg_get_expr(p.polwithcheck, p.polrelid)
FROM pg_policy p
WHERE p.polrelid = $1 AND p.polpermissive
  AND (0::oid = ANY (p.polroles)
       OR EXISTS (SELECT 1 FROM unnest(p.polroles) r WHERE r <> 0 AND pg_has_role($2, r, 'MEMBER')))
ORDER BY 1`

// AssertRLS fails the test for every tenant-owned table (a table with a
// tenant_id column) in schemas that breaks the conventions of package
// migrate. It checks, as the superuser:
//
//   - row-level security is enabled and forced;
//   - exactly one permissive policy applies to the application role (a
//     policy for PUBLIC or for a role it is a member of), it is for all
//     commands, and its USING and WITH CHECK expressions are exactly
//     TenantPolicyExpr, or EnvironmentPolicyExpr on a table with an
//     environment_id column (a missing WITH CHECK reuses USING);
//   - the application role has none of TRUNCATE (which ignores row-level
//     security), TRIGGER or REFERENCES;
//   - tenant_id is the first primary key column;
//   - the table is not owned by the application role;
//   - behaviourally: inside a transaction that is always rolled back, the
//     superuser inserts rows for two random tenants (and, on environment
//     tables, a second environment of the first tenant); the application
//     role, bound to the first tenant and environment, must see and delete
//     only that tenant's (and environment's) row. Rows are generated from
//     the column types; if they cannot be inserted (constraints, foreign
//     keys, unsupported types) the behavioural check is skipped for that
//     table and the test log says so and why. The static checks above
//     still apply.
//
// With no schemas it checks every non-system schema; a named schema that
// does not exist is also a failure.
func AssertRLS(tb testing.TB, env *Env, schemas ...string) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	appCfg, err := pgx.ParseConfig(env.AppURL.Reveal())
	if err != nil {
		tb.Fatal("dbtest: AssertRLS: invalid application URL")
	}
	conn, err := pgx.Connect(ctx, env.AdminURL.Reveal())
	if err != nil {
		tb.Fatalf("dbtest: AssertRLS: connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	problems, notes, err := rlsProblems(ctx, conn, appCfg.User, schemas)
	if err != nil {
		tb.Fatalf("dbtest: AssertRLS: %v", err)
	}
	for _, n := range notes {
		tb.Logf("row-level security: %s", n)
	}
	for _, p := range problems {
		tb.Errorf("row-level security: %s", p)
	}
}

// rlsTable is one row of rlsTablesQuery.
type rlsTable struct {
	oid                     uint32
	schema, name            string
	enabled, forced         bool
	owner                   string
	pkFirst                 *string
	hasEnvironment          bool
	truncate, trigger, refs bool
}

func (t rlsTable) qualified() string { return pgx.Identifier{t.schema, t.name}.Sanitize() }

// rlsPolicy is one row of rlsPoliciesQuery.
type rlsPolicy struct {
	name, cmd, qual string
	check           *string
}

func rlsProblems(ctx context.Context, conn *pgx.Conn, appRole string, schemas []string) (problems, notes []string, err error) {
	if schemas == nil {
		schemas = []string{}
	}
	rows, err := conn.Query(ctx,
		`SELECT s FROM unnest($1::text[]) s WHERE NOT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = s)`, schemas)
	if err != nil {
		return nil, nil, fmt.Errorf("check schemas: %w", err)
	}
	missing, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, nil, fmt.Errorf("check schemas: %w", err)
	}
	for _, s := range missing {
		problems = append(problems, fmt.Sprintf("schema %q does not exist", s))
	}
	rows, err = conn.Query(ctx, rlsTablesQuery, schemas, appRole)
	if err != nil {
		return nil, nil, fmt.Errorf("list tables: %w", err)
	}
	tables, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (rlsTable, error) {
		var t rlsTable
		err := r.Scan(&t.oid, &t.schema, &t.name, &t.enabled, &t.forced, &t.owner, &t.pkFirst,
			&t.hasEnvironment, &t.truncate, &t.trigger, &t.refs)
		if err != nil {
			return t, fmt.Errorf("scan: %w", err)
		}
		return t, nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list tables: %w", err)
	}
	for _, t := range tables {
		tableProblems, note, err := checkTable(ctx, conn, t, appRole)
		if err != nil {
			return nil, nil, err
		}
		for _, p := range tableProblems {
			problems = append(problems, t.schema+"."+t.name+": "+p)
		}
		if note != "" {
			notes = append(notes, t.schema+"."+t.name+": behavioural check skipped: "+note)
		}
	}
	return problems, notes, nil
}

// checkTable applies every rule of AssertRLS to t. It returns the problems
// and, if the behavioural check was skipped, the reason.
func checkTable(ctx context.Context, conn *pgx.Conn, t rlsTable, appRole string) (problems []string, skipped string, err error) {
	add := func(bad bool, format string, args ...any) {
		if bad {
			problems = append(problems, fmt.Sprintf(format, args...))
		}
	}
	add(!t.enabled, "row-level security is not enabled")
	add(!t.forced, "row-level security is not forced")
	policyProblems, err := checkPolicies(ctx, conn, t, appRole)
	if err != nil {
		return nil, "", err
	}
	problems = append(problems, policyProblems...)
	add(t.truncate, "the application role %q has TRUNCATE, which ignores row-level security", appRole)
	add(t.trigger, "the application role %q has TRIGGER", appRole)
	add(t.refs, "the application role %q has REFERENCES", appRole)
	add(t.pkFirst == nil, "has no primary key")
	add(t.pkFirst != nil && *t.pkFirst != "tenant_id", "tenant_id is not the first primary key column")
	add(t.owner == appRole, "is owned by the application role %q", appRole)
	leak, skipped, err := checkBehaviour(ctx, conn, t, appRole)
	if err != nil {
		return nil, "", err
	}
	add(leak != "", "%s", leak)
	return problems, skipped, nil
}

// checkPolicies applies the policy rules of AssertRLS to t.
func checkPolicies(ctx context.Context, conn *pgx.Conn, t rlsTable, appRole string) ([]string, error) {
	rows, err := conn.Query(ctx, rlsPoliciesQuery, t.oid, appRole)
	if err != nil {
		return nil, fmt.Errorf("list policies of %s: %w", t.qualified(), err)
	}
	policies, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (rlsPolicy, error) {
		var p rlsPolicy
		if err := r.Scan(&p.name, &p.cmd, &p.qual, &p.check); err != nil {
			return p, fmt.Errorf("scan: %w", err)
		}
		return p, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list policies of %s: %w", t.qualified(), err)
	}
	want := TenantPolicyExpr
	if t.hasEnvironment {
		want = EnvironmentPolicyExpr
	}
	var out []string
	good := 0
	for _, p := range policies {
		switch {
		case p.cmd != "*":
			out = append(out, fmt.Sprintf("permissive policy %q for command %s applies to %q; only one policy for all commands may", p.name, p.cmd, appRole))
		case p.qual != want:
			out = append(out, fmt.Sprintf("permissive policy %q does not restrict rows: USING is %s, want exactly %s", p.name, p.qual, want))
		case p.check != nil && *p.check != want:
			out = append(out, fmt.Sprintf("permissive policy %q does not restrict writes: WITH CHECK is %s, want exactly %s", p.name, *p.check, want))
		default:
			good++
		}
	}
	switch {
	case good == 0:
		out = append(out, fmt.Sprintf("no permissive policy for all commands applying to %q restricts rows to exactly %s", appRole, want))
	case good > 1:
		out = append(out, fmt.Sprintf("%d permissive tenant policies apply to %q; want exactly one", good, appRole))
	}
	return out, nil
}

// errSkip marks a behavioural check that cannot run on a table.
var errSkip = errors.New("skip")

// checkBehaviour runs the behavioural isolation check on t in a transaction
// that is always rolled back. It returns a problem, or the reason the check
// was skipped.
func checkBehaviour(ctx context.Context, conn *pgx.Conn, t rlsTable, appRole string) (problem, skipped string, err error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return "", "", fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	tenantA, tenantB, envA, envB := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	type key struct{ tenant, env uuid.UUID }
	rowsToInsert := []key{{tenantA, envA}, {tenantB, envB}}
	if t.hasEnvironment {
		rowsToInsert = append(rowsToInsert, key{tenantA, envB})
	}
	for _, k := range rowsToInsert {
		if err := insertTestRow(ctx, tx, t, k.tenant, k.env); err != nil {
			if errors.Is(err, errSkip) {
				return "", strings.TrimPrefix(err.Error(), errSkip.Error()+": "), nil
			}
			return "", "", err
		}
	}
	return checkAsApp(ctx, tx, t, appRole, tenantA, envA)
}

// checkAsApp switches tx to the application role bound to tenant and env and
// checks that it sees and deletes exactly the one row inserted for them.
func checkAsApp(ctx context.Context, tx pgx.Tx, t rlsTable, appRole string, tenantA, envA uuid.UUID) (problem, skipped string, err error) {
	var usage, canSelect, canDelete bool
	if err := tx.QueryRow(ctx,
		`SELECT has_schema_privilege($1, $3, 'USAGE'), has_table_privilege($1, $2::oid, 'SELECT'),
		        has_table_privilege($1, $2::oid, 'DELETE')`,
		appRole, t.oid, t.schema).Scan(&usage, &canSelect, &canDelete); err != nil {
		return "", "", fmt.Errorf("check privileges of %s: %w", t.qualified(), err)
	}
	if !usage || !canSelect {
		return "", fmt.Sprintf("the application role %q cannot read the table", appRole), nil
	}
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE `+pgx.Identifier{appRole}.Sanitize()); err != nil {
		return "", "", fmt.Errorf("switch to the application role: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true), set_config('app.environment_id', $2, true)`,
		tenantA.String(), envA.String()); err != nil {
		return "", "", fmt.Errorf("bind the test tenant: %w", err)
	}
	var visible, foreign int64
	envFilter := ""
	if t.hasEnvironment {
		envFilter = " OR environment_id <> $2"
	}
	if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE tenant_id <> $1`+envFilter+`) FROM `+t.qualified(),
		envArgs(t, tenantA, envA)...).Scan(&visible, &foreign); err != nil {
		return fmt.Sprintf("the application role bound to a tenant cannot read the table: %v", err), "", nil
	}
	if foreign > 0 {
		return fmt.Sprintf("the application role bound to one tenant sees %d rows of another tenant or environment", foreign), "", nil
	}
	if visible != 1 {
		return fmt.Sprintf("the application role bound to a tenant sees %d of its own rows, want 1", visible), "", nil
	}
	if canDelete {
		tag, err := tx.Exec(ctx, `DELETE FROM `+t.qualified())
		if err != nil {
			return fmt.Sprintf("the application role bound to a tenant cannot delete its rows: %v", err), "", nil
		}
		if tag.RowsAffected() != 1 {
			return fmt.Sprintf("the application role bound to one tenant deleted %d rows, want only its own 1", tag.RowsAffected()), "", nil
		}
	}
	return "", "", nil
}

func envArgs(t rlsTable, tenant, env uuid.UUID) []any {
	if t.hasEnvironment {
		return []any{tenant, env}
	}
	return []any{tenant}
}

// requiredColumnsQuery lists the columns of table $1 that an INSERT must
// set: NOT NULL, no default, not generated or identity.
const requiredColumnsQuery = `
SELECT a.attname, format_type(a.atttypid, a.atttypmod), t.typname
FROM pg_attribute a
JOIN pg_type t ON t.oid = a.atttypid
WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
  AND a.attnotnull AND NOT a.atthasdef AND a.attidentity = '' AND a.attgenerated = ''
  AND a.attname NOT IN ('tenant_id', 'environment_id')
ORDER BY a.attnum`

// insertTestRow inserts one row for tenant (and env, on environment tables)
// with generated values for the other required columns, inside a savepoint.
// It returns an error wrapping errSkip when that is not possible.
func insertTestRow(ctx context.Context, tx pgx.Tx, t rlsTable, tenant, env uuid.UUID) error {
	rows, err := tx.Query(ctx, requiredColumnsQuery, t.oid)
	if err != nil {
		return fmt.Errorf("list columns of %s: %w", t.qualified(), err)
	}
	type column struct{ name, typ, base string }
	cols, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (column, error) {
		var c column
		if err := r.Scan(&c.name, &c.typ, &c.base); err != nil {
			return c, fmt.Errorf("scan: %w", err)
		}
		return c, nil
	})
	if err != nil {
		return fmt.Errorf("list columns of %s: %w", t.qualified(), err)
	}
	names := []string{"tenant_id"}
	values := []string{"$1::uuid"}
	args := []any{tenant}
	if t.hasEnvironment {
		names = append(names, "environment_id")
		values = append(values, "$2::uuid")
		args = append(args, env)
	}
	for _, c := range cols {
		v, ok := sampleValue(c.base)
		if !ok {
			return fmt.Errorf("%w: cannot generate a value for column %q of type %s", errSkip, c.name, c.typ)
		}
		names = append(names, pgx.Identifier{c.name}.Sanitize())
		values = append(values, "("+v+")::"+c.typ)
	}
	stmt := "INSERT INTO " + t.qualified() + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(values, ", ") + ")"
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("savepoint: %w", err)
	}
	if _, err := sp.Exec(ctx, stmt, args...); err != nil {
		_ = sp.Rollback(ctx)
		return fmt.Errorf("%w: cannot insert test rows: %v", errSkip, err) //nolint:errorlint // the cause is reported, not matched
	}
	if err := sp.Commit(ctx); err != nil {
		return fmt.Errorf("release savepoint: %w", err)
	}
	return nil
}

// sampleValue returns an SQL expression with a fresh value of the base type
// typname.
func sampleValue(typname string) (string, bool) {
	switch typname {
	case "uuid":
		return "gen_random_uuid()", true
	case "text", "varchar", "bpchar", "citext", "name":
		return "'rls_' || md5(random()::text)", true
	case "int2":
		return "floor(random() * 32000)", true
	case "int4", "int8", "numeric", "float4", "float8":
		return "floor(random() * 2000000000)", true
	case "bool":
		return "false", true
	case "timestamptz", "timestamp", "date":
		return "now()", true
	case "bytea":
		return "uuid_send(gen_random_uuid())", true
	case "jsonb", "json":
		return "'{}'", true
	case "inet":
		return "'192.0.2.1'", true
	}
	return "", false
}
