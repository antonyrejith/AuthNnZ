// Package db is the PostgreSQL access layer: two connection pools,
// tenant-bound transactions enforced by row-level security, and the mapping
// of database errors to platform error codes (ADR-0002).
//
// # Roles and pools
//
// The application connects with two roles. AppRole (portun_app) runs all
// tenant work through InTenantTx; it is not a superuser, has no BYPASSRLS,
// cannot switch to a role that has either, owns neither the database nor any
// schema, table, view, sequence or function outside the system schemas, and
// has no CREATE privilege on such a schema, so it can neither bypass nor
// disable row-level security nor plant objects that change what others run.
// PlatformRole (portun_platform) has BYPASSRLS and runs platform-level work
// (tenant management, the audit writer, the change-capture reconciler)
// through InPlatformTx. Open checks all of this against pg_roles and refuses
// to start otherwise (secure defaults). Migrations run as a third, owner role
// (package migrate).
//
// # Tenant isolation
//
// InTenantTx reads the tenant from the context (scope.TenantFrom; a missing
// scope is an error, never a default), begins a transaction on the
// application pool and sets the transaction-local settings app.tenant_id and
// app.environment_id (the empty string when the scope has no environment, so
// environment-scoped policies fail closed) with set_config(..., true).
// Every tenant-owned table carries a policy comparing tenant_id with
// current_setting('app.tenant_id')::uuid (see package migrate for the
// conventions), so the database itself filters rows to the scope's tenant.
// Because the settings are transaction-local, a pooled connection never
// carries a tenant into the next transaction. As a second line of defence,
// the application pool checks every released connection and destroys it if
// app.tenant_id or app.environment_id is still set at session level, or the
// session switched roles (code in fn could have used set_config(..., false)).
//
// Outside InTenantTx the application role fails closed: on a connection that
// never set app.tenant_id, current_setting raises "unrecognized configuration
// parameter"; on a connection that set it in an earlier transaction, the
// setting reads as the empty string and the uuid cast raises "invalid input
// syntax". A statement that evaluates the policy therefore fails; a statement
// that finds no candidate rows returns no rows. It never returns rows of any
// tenant. Open also refuses an application role whose sessions start with
// app.tenant_id already set (a role or database default would otherwise act
// as a default tenant).
package db

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// Database roles (ADR-0002). Deployments create them; migrate.EnsureRoles
// creates them for development and tests.
const (
	AppRole      = "portun_app"
	PlatformRole = "portun_platform"
)

// Transaction-local settings that carry the tenant scope into PostgreSQL.
const (
	TenantSetting      = "app.tenant_id"
	EnvironmentSetting = "app.environment_id"
)

// DB holds the application and platform connection pools.
type DB struct {
	app      *pgxpool.Pool
	platform *pgxpool.Pool
	logger   *slog.Logger
}

// Open creates both pools, connects, and verifies the roles they connect as
// (see the package documentation). It fails if either database is
// unreachable or a role is unsafe. The caller closes the DB. logger receives
// the warnings the DB cannot return (a failed rollback); nil discards them.
func Open(ctx context.Context, cfg Config, logger *slog.Logger) (*DB, error) {
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	appCfg, err := poolConfig("application", cfg.AppURL, cfg.MaxConns, cfg)
	if err != nil {
		return nil, err
	}
	appCfg.AfterRelease = cleanSession
	platformCfg, err := poolConfig("platform", cfg.PlatformURL, cfg.PlatformMaxConns, cfg)
	if err != nil {
		return nil, err
	}
	app, err := pgxpool.NewWithConfig(ctx, appCfg)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "create the application connection pool")
	}
	platform, err := pgxpool.NewWithConfig(ctx, platformCfg)
	if err != nil {
		app.Close()
		return nil, errs.Wrap(errs.CodeInternal, err, "create the platform connection pool")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	d := &DB{app: app, platform: platform, logger: logger}
	if err := d.verifyRoles(ctx); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// poolConfig parses url and applies the pool settings. A parse error never
// carries the URL or its parse details, which can contain the password.
func poolConfig(name string, url secret.String, maxConns int32, cfg Config) (*pgxpool.Config, error) {
	pc, err := pgxpool.ParseConfig(url.Reveal())
	if err != nil {
		return nil, errs.New(errs.CodeInvalidRequest, "the "+name+" database URL is not a valid PostgreSQL connection string")
	}
	pc.MaxConns = maxConns
	pc.MaxConnLifetime = cfg.MaxConnLifetime
	pc.MaxConnIdleTime = cfg.MaxConnIdleTime
	cc := pc.ConnConfig
	cc.ConnectTimeout = cfg.ConnectTimeout
	cc.RuntimeParams["statement_timeout"] = milliseconds(cfg.StatementTimeout)
	cc.RuntimeParams["idle_in_transaction_session_timeout"] = milliseconds(cfg.IdleInTransactionTimeout)
	if cc.RuntimeParams["application_name"] == "" {
		cc.RuntimeParams["application_name"] = cfg.ApplicationName
	}
	return pc, nil
}

// cleanSessionQuery reports whether a released application connection still
// carries session state that could leak into the next transaction.
const cleanSessionQuery = `SELECT coalesce(current_setting('` + TenantSetting + `', true), '') = ''
   AND coalesce(current_setting('` + EnvironmentSetting + `', true), '') = ''
   AND current_user = session_user`

// cleanSessionTimeout bounds the check of a released connection.
const cleanSessionTimeout = 5 * time.Second

// cleanSession is the application pool's AfterRelease hook. InTenantTx sets
// the scope transaction-locally, but code inside a transaction could still
// set it for the session (set_config(..., false), SET without LOCAL) or
// switch roles. Such a connection is destroyed instead of being returned to
// the pool, so the next transaction, possibly of another tenant, never
// inherits it. A failed check also destroys the connection.
func cleanSession(conn *pgx.Conn) bool {
	ctx, cancel := context.WithTimeout(context.Background(), cleanSessionTimeout)
	defer cancel()
	var clean bool
	if err := conn.QueryRow(ctx, cleanSessionQuery).Scan(&clean); err != nil {
		return false
	}
	return clean
}

// Close closes both pools, waiting for acquired connections to be released.
func (d *DB) Close() {
	d.app.Close()
	d.platform.Close()
}

// Ping checks that both pools can reach the database.
func (d *DB) Ping(ctx context.Context) error {
	if err := d.app.Ping(ctx); err != nil {
		return MapError(err)
	}
	if err := d.platform.Ping(ctx); err != nil {
		return MapError(err)
	}
	return nil
}

// userSchema is the condition that the schema n is not a system schema.
const userSchema = `n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg\_%'`

// roleQuery describes the role a connection runs as. Role attributes are
// never inherited, but membership allows SET ROLE, so membership in a
// privileged role counts as having its privileges. pg_has_role(r, r, ...) is
// true, so "member of an owner" includes being the owner.
const roleQuery = `
SELECT r.rolname,
       r.rolsuper,
       r.rolbypassrls,
       EXISTS (SELECT 1 FROM pg_roles m
               WHERE m.oid <> r.oid AND m.rolsuper AND pg_has_role(r.oid, m.oid, 'MEMBER')),
       EXISTS (SELECT 1 FROM pg_roles m
               WHERE m.oid <> r.oid AND m.rolbypassrls AND pg_has_role(r.oid, m.oid, 'MEMBER')),
       EXISTS (SELECT 1 FROM pg_database d
               WHERE d.datname = current_database() AND pg_has_role(r.oid, d.datdba, 'MEMBER')),
       EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
               WHERE c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f')
                 AND ` + userSchema + `
                 AND pg_has_role(r.oid, c.relowner, 'MEMBER')),
       EXISTS (SELECT 1 FROM pg_namespace n
               WHERE ` + userSchema + ` AND pg_has_role(r.oid, n.nspowner, 'MEMBER')),
       EXISTS (SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
               WHERE ` + userSchema + ` AND pg_has_role(r.oid, p.proowner, 'MEMBER')),
       EXISTS (SELECT 1 FROM pg_namespace n
               WHERE ` + userSchema + ` AND has_schema_privilege(r.oid, n.oid, 'CREATE')),
       coalesce(current_setting('` + TenantSetting + `', true), '')
FROM pg_roles r
WHERE r.rolname = current_user`

// roleFacts is one row of roleQuery.
type roleFacts struct {
	name              string
	superuser         bool
	bypassRLS         bool
	memberOfSuperuser bool
	memberOfBypassRLS bool
	ownsDatabase      bool
	ownsRelations     bool
	ownsSchemas       bool
	ownsFunctions     bool
	canCreate         bool
	tenantSetting     string
}

func queryRole(ctx context.Context, pool *pgxpool.Pool) (roleFacts, error) {
	var f roleFacts
	err := pool.QueryRow(ctx, roleQuery).Scan(&f.name, &f.superuser, &f.bypassRLS,
		&f.memberOfSuperuser, &f.memberOfBypassRLS, &f.ownsDatabase,
		&f.ownsRelations, &f.ownsSchemas, &f.ownsFunctions, &f.canCreate, &f.tenantSetting)
	if err != nil {
		return roleFacts{}, MapError(err)
	}
	return f, nil
}

// verifyRoles enforces the role rules of the package documentation.
func (d *DB) verifyRoles(ctx context.Context) error {
	app, err := queryRole(ctx, d.app)
	if err != nil {
		return err
	}
	platform, err := queryRole(ctx, d.platform)
	if err != nil {
		return err
	}
	problems := append(appRoleProblems(app), platformRoleProblems(platform)...)
	if len(problems) > 0 {
		return errs.New(errs.CodeInvalidRequest, "insecure database roles: "+strings.Join(problems, "; "))
	}
	return nil
}

func appRoleProblems(f roleFacts) []string {
	var out []string
	add := func(bad bool, what string) {
		if bad {
			out = append(out, "application role "+quote(f.name)+" "+what)
		}
	}
	add(f.superuser, "is a superuser")
	add(f.bypassRLS, "has BYPASSRLS")
	add(f.memberOfSuperuser, "is a member of a superuser role")
	add(f.memberOfBypassRLS, "is a member of a role with BYPASSRLS")
	add(f.ownsDatabase, "owns the database")
	add(f.ownsRelations, "owns tables, views or sequences, so it could disable row-level security or change them")
	add(f.ownsSchemas, "owns a schema, so it could drop or replace its objects")
	add(f.ownsFunctions, "owns functions, so it could change code that policies or other roles run")
	add(f.canCreate, "has CREATE on a schema, so it could create objects that shadow or bypass the schema's own")
	add(f.tenantSetting != "", "starts sessions with "+TenantSetting+" set (a default tenant)")
	return out
}

func platformRoleProblems(f roleFacts) []string {
	var out []string
	add := func(bad bool, what string) {
		if bad {
			out = append(out, "platform role "+quote(f.name)+" "+what)
		}
	}
	add(!f.bypassRLS, "lacks BYPASSRLS")
	add(f.superuser, "is a superuser")
	add(f.memberOfSuperuser, "is a member of a superuser role")
	add(f.ownsDatabase, "owns the database")
	add(f.ownsRelations, "owns tables, views or sequences")
	return out
}

func quote(s string) string { return `"` + s + `"` }
