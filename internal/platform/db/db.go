// Package db is the PostgreSQL access layer: two connection pools,
// tenant-bound transactions enforced by row-level security, and the mapping
// of database errors to platform error codes (ADR-0002).
//
// # Roles and pools
//
// The application connects with two roles. AppRole (portun_app) runs all
// tenant work through InTenantTx; it is not a superuser, has no BYPASSRLS,
// cannot switch to a role that has either, and owns neither the database nor
// any table, so it can neither bypass nor disable row-level security.
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
// application pool and sets the transaction-local settings app.tenant_id and,
// when the scope has one, app.environment_id with set_config(..., true).
// Every tenant-owned table carries a policy comparing tenant_id with
// current_setting('app.tenant_id')::uuid (see package migrate for the
// conventions), so the database itself filters rows to the scope's tenant.
// Because the settings are transaction-local, a pooled connection never
// carries a tenant into the next transaction.
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
               WHERE c.relkind IN ('r', 'p')
                 AND n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg\_%'
                 AND pg_has_role(r.oid, c.relowner, 'MEMBER')),
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
	ownsTables        bool
	tenantSetting     string
}

func queryRole(ctx context.Context, pool *pgxpool.Pool) (roleFacts, error) {
	var f roleFacts
	err := pool.QueryRow(ctx, roleQuery).Scan(&f.name, &f.superuser, &f.bypassRLS,
		&f.memberOfSuperuser, &f.memberOfBypassRLS, &f.ownsDatabase, &f.ownsTables, &f.tenantSetting)
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
	add(f.ownsTables, "owns tables, so it could disable row-level security")
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
	add(f.ownsTables, "owns tables")
	return out
}

func quote(s string) string { return `"` + s + `"` }
