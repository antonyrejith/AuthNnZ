//go:build integration

package db_test

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/scope"
	"github.com/antonyrejith/portun/internal/platform/secret"
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

func newTenant(t *testing.T) scope.Tenant {
	t.Helper()
	id, err := ids.New[ids.TenantKind]()
	if err != nil {
		t.Fatal(err)
	}
	return scope.Tenant{TenantID: id}
}

func tenantCtx(t *testing.T, tn scope.Tenant) context.Context {
	t.Helper()
	return scope.WithTenant(t.Context(), tn)
}

// insertWidget inserts a widget for the scope's tenant and returns its id.
func insertWidget(t *testing.T, d *db.DB, tn scope.Tenant, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	err := d.InTenantTx(tenantCtx(t, tn), func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO dbtest_app.widgets (tenant_id, id, name) VALUES ($1, $2, $3)`,
			tn.TenantID.UUID(), id, name)
		return err
	})
	if err != nil {
		t.Fatalf("insert widget: %v", err)
	}
	return id
}

func countWidgets(t *testing.T, d *db.DB, tn scope.Tenant) int {
	t.Helper()
	var n int
	err := d.InTenantTx(tenantCtx(t, tn), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM dbtest_app.widgets`).Scan(&n)
	}, db.ReadOnly())
	if err != nil {
		t.Fatalf("count widgets: %v", err)
	}
	return n
}

// TestTenantIsolation proves that one tenant can neither see nor change
// another tenant's rows, even with statements that do not filter by tenant.
//
// Covers: TEN-03
func TestTenantIsolation(t *testing.T) { //nolint:tparallel // the group of statements runs before the final checks, which need its result
	t.Parallel()
	env := dbtest.New(t)
	a, b := newTenant(t), newTenant(t)
	insertWidget(t, env.DB, a, "a1")
	bID := insertWidget(t, env.DB, b, "b1")

	tests := []struct {
		name string
		sql  string
		args []any
		rows int64
		code errs.Code // "" means success with rows affected
	}{
		{"select by id", `SELECT 1 FROM dbtest_app.widgets WHERE id = $1`, []any{bID}, 0, ""},
		{"update all", `UPDATE dbtest_app.widgets SET name = 'x'`, nil, 1, ""},
		{"update other", `UPDATE dbtest_app.widgets SET name = 'x' WHERE id = $1`, []any{bID}, 0, ""},
		{"delete other", `DELETE FROM dbtest_app.widgets WHERE id = $1`, []any{bID}, 0, ""},
		{
			"insert as other", `INSERT INTO dbtest_app.widgets (tenant_id, id, name) VALUES ($1, $2, 'evil')`,
			[]any{b.TenantID.UUID(), uuid.New()},
			0, errs.CodeInternal,
		},
		{"move row to other", `UPDATE dbtest_app.widgets SET tenant_id = $1`, []any{b.TenantID.UUID()}, 0, errs.CodeInternal},
	}
	t.Run("statements as tenant A", func(t *testing.T) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				var affected int64
				err := env.DB.InTenantTx(tenantCtx(t, a), func(ctx context.Context, tx pgx.Tx) error {
					tag, err := tx.Exec(ctx, tt.sql, tt.args...)
					affected = tag.RowsAffected()
					return err
				})
				if tt.code != "" {
					if !errs.Is(err, tt.code) {
						t.Fatalf("err = %v, want code %s", err, tt.code)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if affected != tt.rows {
					t.Fatalf("rows affected = %d, want %d", affected, tt.rows)
				}
			})
		}
	})
	// B's row is untouched.
	var name string
	err := env.DB.InTenantTx(tenantCtx(t, b), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT name FROM dbtest_app.widgets WHERE id = $1`, bID).Scan(&name)
	})
	if err != nil || name != "b1" {
		t.Fatalf("tenant B row: name %q, err %v; want b1", name, err)
	}
	if n := countWidgets(t, env.DB, b); n != 1 {
		t.Fatalf("tenant B sees %d rows, want 1", n)
	}
}

// TestEnvironmentSetting proves the environment of the scope is bound too.
//
// Covers: TEN-03
func TestEnvironmentSetting(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	envID, err := ids.New[ids.EnvironmentKind]()
	if err != nil {
		t.Fatal(err)
	}
	// Without an environment the setting is set too, to the empty string,
	// so that environment-scoped policies fail closed.
	tests := []struct {
		name string
		tn   scope.Tenant
		want string
	}{
		{"without environment", newTenant(t), ""},
		{"with environment", scope.Tenant{TenantID: newTenant(t).TenantID, EnvironmentID: envID}, envID.UUID().String()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var tenant, environment string
			err := env.DB.InTenantTx(tenantCtx(t, tt.tn), func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT current_setting('app.tenant_id'), current_setting('app.environment_id')`).
					Scan(&tenant, &environment)
			})
			if err != nil {
				t.Fatal(err)
			}
			if tenant != tt.tn.TenantID.UUID().String() || environment != tt.want {
				t.Fatalf("settings = (%q, %q), want (%q, %q)", tenant, environment, tt.tn.TenantID.UUID(), tt.want)
			}
		})
	}
}

// TestAppRoleFailsClosedOutsideTenantTx proves that the application role
// sees no tenant's rows without a tenant transaction.
//
// Covers: TEN-03, SEC-09
func TestAppRoleFailsClosedOutsideTenantTx(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	insertWidget(t, env.DB, newTenant(t), "w")
	conn, err := pgx.Connect(t.Context(), env.AppURL.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	for _, sql := range []string{
		`SELECT count(*) FROM dbtest_app.widgets`,
		`UPDATE dbtest_app.widgets SET name = 'x'`,
		`DELETE FROM dbtest_app.widgets`,
	} {
		if _, err := conn.Exec(t.Context(), sql); err == nil {
			t.Errorf("%s: succeeded without a tenant setting", sql)
		}
	}
	var n int
	if err := env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM dbtest_app.widgets WHERE name = 'w'`).Scan(&n)
	}); err != nil || n != 1 {
		t.Fatalf("row changed: count %d, err %v", n, err)
	}
}

// TestPooledConnectionCarriesNoTenant proves that the tenant setting is
// transaction-local: with a pool of one connection, the connection used by a
// tenant transaction has no tenant afterwards and fails closed.
//
// Covers: TEN-03
func TestPooledConnectionCarriesNoTenant(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	d, err := db.Open(t.Context(), db.Config{AppURL: env.AppURL, PlatformURL: env.PlatformURL, MaxConns: 1, PlatformMaxConns: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tn := newTenant(t)
	insertWidget(t, d, tn, "w")
	var pidInTx int
	if err := d.InTenantTx(tenantCtx(t, tn), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pidInTx)
	}); err != nil {
		t.Fatal(err)
	}
	conn, err := db.AppPool(d).Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	var pid int
	var setting *string
	if err := conn.QueryRow(t.Context(), `SELECT pg_backend_pid(), current_setting('app.tenant_id', true)`).Scan(&pid, &setting); err != nil {
		t.Fatal(err)
	}
	if pid != pidInTx {
		t.Fatalf("pool handed out backend %d, want the same backend %d", pid, pidInTx)
	}
	if setting != nil && *setting != "" {
		t.Fatalf("pooled connection still carries tenant %q", *setting)
	}
	if _, err := conn.Exec(t.Context(), `SELECT count(*) FROM dbtest_app.widgets`); err == nil {
		t.Fatal("query on the pooled connection succeeded without a tenant")
	}
}

// TestSessionLevelScopeDoesNotLeak reproduces the security review's
// pool-of-one scenario: code in tenant A's transaction sets the scope at
// session level (set_config(..., false)). The next transaction, of tenant B
// without an environment, must see neither A's tenant nor A's environment,
// and the connection must not return to the pool with that state.
//
// Covers: TEN-03
func TestSessionLevelScopeDoesNotLeak(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	d, err := db.Open(t.Context(), db.Config{AppURL: env.AppURL, PlatformURL: env.PlatformURL, MaxConns: 1, PlatformMaxConns: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	envA, err := ids.New[ids.EnvironmentKind]()
	if err != nil {
		t.Fatal(err)
	}
	a := scope.Tenant{TenantID: newTenant(t).TenantID, EnvironmentID: envA}
	b := newTenant(t)
	var pidA int
	if err := d.InTenantTx(tenantCtx(t, a), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT pg_backend_pid(), set_config('app.tenant_id', $1, false), set_config('app.environment_id', $2, false)`,
			a.TenantID.UUID().String(), envA.UUID().String()).Scan(&pidA, new(string), new(string))
	}); err != nil {
		t.Fatal(err)
	}
	var tenant, environment string
	if err := d.InTenantTx(tenantCtx(t, b), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT current_setting('app.tenant_id'), current_setting('app.environment_id')`).Scan(&tenant, &environment)
	}); err != nil {
		t.Fatal(err)
	}
	if tenant != b.TenantID.UUID().String() || environment != "" {
		t.Fatalf("tenant B's transaction sees (%q, %q), want (%q, \"\")", tenant, environment, b.TenantID.UUID())
	}
	conn, err := db.AppPool(d).Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	var pid int
	var sessionTenant, sessionEnvironment *string
	if err := conn.QueryRow(t.Context(), `SELECT pg_backend_pid(), current_setting('app.tenant_id', true), current_setting('app.environment_id', true)`).
		Scan(&pid, &sessionTenant, &sessionEnvironment); err != nil {
		t.Fatal(err)
	}
	if pid == pidA {
		t.Fatalf("the pool reused backend %d, which carries a session-level tenant", pid)
	}
	if (sessionTenant != nil && *sessionTenant != "") || (sessionEnvironment != nil && *sessionEnvironment != "") {
		t.Fatalf("pooled connection carries session scope (%v, %v)", sessionTenant, sessionEnvironment)
	}
}

// TestOpenRefusesAppRoleObjects proves that Open refuses an application role
// that owns a schema, a view, a sequence or a function, or may create objects
// in a schema.
//
// Covers: SEC-09
func TestOpenRefusesAppRoleObjects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ddl  string
		want string
	}{
		{"owns a schema", `CREATE SCHEMA app_owned AUTHORIZATION portun_app`, "owns a schema"},
		{"owns a view", `CREATE VIEW dbtest_app.v AS SELECT 1 AS x; ALTER VIEW dbtest_app.v OWNER TO portun_app`, "owns tables, views or sequences"},
		{"owns a sequence", `CREATE SEQUENCE dbtest_app.s; ALTER SEQUENCE dbtest_app.s OWNER TO portun_app`, "owns tables, views or sequences"},
		{"owns a function", `CREATE FUNCTION dbtest_app.f() RETURNS int LANGUAGE sql AS 'SELECT 1';
			ALTER FUNCTION dbtest_app.f() OWNER TO portun_app`, "owns functions"},
		{"create on schema", `GRANT CREATE ON SCHEMA dbtest_app TO portun_app`, "has CREATE on a schema"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := dbtest.New(t)
			admin, err := pgx.Connect(t.Context(), env.AdminURL.Reveal())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = admin.Close(context.Background()) }()
			if _, err := admin.Exec(t.Context(), tt.ddl); err != nil {
				t.Fatal(err)
			}
			d, err := db.Open(t.Context(), db.Config{AppURL: env.AppURL, PlatformURL: env.PlatformURL}, nil)
			if err == nil {
				d.Close()
				t.Fatal("Open accepted the application role")
			}
			if !errs.Is(err, errs.CodeInvalidRequest) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want CodeInvalidRequest mentioning %q", err, tt.want)
			}
		})
	}
}

// TestInTenantTxRequiresScope proves that a missing tenant scope is an
// internal error and that no transaction is started.
//
// Covers: TEN-03
func TestInTenantTxRequiresScope(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	before := db.AppPool(env.DB).Stat().AcquireCount()
	tests := []struct {
		name string
		ctx  context.Context
	}{
		{"no scope", t.Context()},
		{"zero tenant", scope.WithTenant(t.Context(), scope.Tenant{})},
	}
	for _, tt := range tests {
		called := false
		err := env.DB.InTenantTx(tt.ctx, func(context.Context, pgx.Tx) error {
			called = true
			return nil
		})
		if !errs.Is(err, errs.CodeInternal) || called {
			t.Fatalf("%s: err = %v, fn called = %v; want CodeInternal and no call", tt.name, err, called)
		}
	}
	if after := db.AppPool(env.DB).Stat().AcquireCount(); after != before {
		t.Fatalf("pool acquisitions went from %d to %d; want none", before, after)
	}
}

// TestRollback proves that an error or a panic in fn rolls back its writes.
func TestRollback(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	errBoom := errors.New("boom")
	tests := []struct {
		name  string
		fail  func() error
		panic bool
	}{
		{"error", func() error { return errBoom }, false},
		{"panic", func() error { panic("boom") }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tn := newTenant(t)
			var err error
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				err = env.DB.InTenantTx(tenantCtx(t, tn), func(ctx context.Context, tx pgx.Tx) error {
					if _, err := tx.Exec(ctx, `INSERT INTO dbtest_app.widgets (tenant_id, id, name) VALUES ($1, $2, 'w')`,
						tn.TenantID.UUID(), uuid.New()); err != nil {
						return err
					}
					return tt.fail()
				})
			}()
			if tt.panic {
				if recovered != "boom" {
					t.Fatalf("recovered %v, want the original panic", recovered)
				}
			} else if !errors.Is(err, errBoom) || !errs.Is(err, errs.CodeInternal) {
				t.Fatalf("err = %v, want errBoom with CodeInternal", err)
			}
			if n := countWidgets(t, env.DB, tn); n != 0 {
				t.Fatalf("%d rows committed, want 0", n)
			}
		})
	}
}

// TestErrorMappingFromDatabase checks the mapping of real server errors.
func TestErrorMappingFromDatabase(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	tn := newTenant(t)
	insertWidget(t, env.DB, tn, "dup")
	tests := []struct {
		name string
		fn   func(ctx context.Context, tx pgx.Tx) error
		opts []db.TxOption
		want errs.Code
	}{
		{"unique violation", func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO dbtest_app.widgets (tenant_id, id, name) VALUES ($1, $2, 'dup')`, tn.TenantID.UUID(), uuid.New())
			return err
		}, nil, errs.CodeConflict},
		{"no rows", func(ctx context.Context, tx pgx.Tx) error {
			var s string
			return tx.QueryRow(ctx, `SELECT name FROM dbtest_app.widgets WHERE id = $1`, uuid.New()).Scan(&s)
		}, nil, errs.CodeNotFound},
		{"write in read-only", func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM dbtest_app.widgets`)
			return err
		}, []db.TxOption{db.ReadOnly()}, errs.CodeInternal},
		{"statement timeout", func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = 50`); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `SELECT pg_sleep(1)`)
			return err
		}, nil, errs.CodeDeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := env.DB.InTenantTx(tenantCtx(t, tn), tt.fn, tt.opts...)
			if !errs.Is(err, tt.want) {
				t.Fatalf("err = %v, want code %s", err, tt.want)
			}
			if _, msg, _ := errs.Public(err); msg == "" || containsSQL(msg) {
				t.Fatalf("client message %q leaks SQL", msg)
			}
		})
	}
}

func containsSQL(s string) bool {
	for _, kw := range []string{"SELECT", "INSERT", "DELETE", "widgets", "SQLSTATE"} {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

// TestPlatformTxSeesAllTenants proves that the platform role bypasses
// row-level security.
//
// Covers: TEN-03
func TestPlatformTxSeesAllTenants(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	insertWidget(t, env.DB, newTenant(t), "a")
	insertWidget(t, env.DB, newTenant(t), "b")
	var tenants int
	err := env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(DISTINCT tenant_id) FROM dbtest_app.widgets`).Scan(&tenants)
	}, db.ReadOnly(), db.Serializable())
	if err != nil {
		t.Fatal(err)
	}
	if tenants != 2 {
		t.Fatalf("platform sees %d tenants, want 2", tenants)
	}
	if err := env.DB.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestOpenRefusesUnsafeRoles proves the secure defaults of Open.
//
// Covers: SEC-09
func TestOpenRefusesUnsafeRoles(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	tests := []struct {
		name          string
		app, platform func(*dbtest.Env) string
	}{
		{"app is superuser", adminURL, platformURL},
		{"app has BYPASSRLS", platformURL, platformURL},
		{"app owns the tables", ownerURL, platformURL},
		{"platform lacks BYPASSRLS", appURL, appURL},
		{"platform is superuser", appURL, adminURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := db.Config{AppURL: secretOf(tt.app(env)), PlatformURL: secretOf(tt.platform(env))}
			d, err := db.Open(t.Context(), cfg, nil)
			if err == nil {
				d.Close()
				t.Fatal("Open accepted unsafe roles")
			}
			if !errs.Is(err, errs.CodeInvalidRequest) {
				t.Fatalf("err = %v, want CodeInvalidRequest", err)
			}
		})
	}
}

// TestOpenRefusesDefaultTenant proves that a role-level default for
// app.tenant_id (which would act as a default tenant) is refused.
//
// Covers: SEC-09, TEN-03
func TestOpenRefusesDefaultTenant(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	admin, err := pgx.Connect(t.Context(), env.AdminURL.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(context.Background()) }()
	// Database-scoped role setting: affects only this test's database.
	if _, err := admin.Exec(t.Context(), `ALTER ROLE portun_app IN DATABASE `+pgx.Identifier{env.Name}.Sanitize()+
		` SET app.tenant_id = '`+uuid.NewString()+`'`); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(t.Context(), db.Config{AppURL: env.AppURL, PlatformURL: env.PlatformURL}, nil)
	if err == nil {
		d.Close()
		t.Fatal("Open accepted a default tenant")
	}
	if !errs.Is(err, errs.CodeInvalidRequest) {
		t.Fatalf("err = %v, want CodeInvalidRequest", err)
	}
}

func adminURL(e *dbtest.Env) string    { return e.AdminURL.Reveal() }
func ownerURL(e *dbtest.Env) string    { return e.OwnerURL.Reveal() }
func appURL(e *dbtest.Env) string      { return e.AppURL.Reveal() }
func platformURL(e *dbtest.Env) string { return e.PlatformURL.Reveal() }

func secretOf(s string) secret.String { return secret.NewString(s) }
