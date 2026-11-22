// Package migrate applies the modules' database migrations (ADR-0014) and,
// for development and tests, creates the database roles.
//
// # Running migrations
//
// Each module owns one PostgreSQL schema and ships its migrations as goose
// SQL files embedded in the binary. A Set names the schema and the file
// system whose root holds the *.sql files:
//
//	//go:embed migrations/*.sql
//	var migrations embed.FS
//	sub, _ := fs.Sub(migrations, "migrations")
//	err := migrate.Up(ctx, logger, ownerURL, migrate.Set{Schema: "audit", FS: sub})
//
// Up runs as the owner role, which owns the schemas and tables (never the
// application or platform role). It holds a PostgreSQL advisory lock for the
// whole run, so concurrent callers (several replicas starting at once) apply
// each migration exactly once. For each set, in the order given, it creates
// the schema if missing and applies the pending migrations, each in its own
// transaction. Versions are tracked per schema in <schema>.goose_db_version,
// so modules migrate independently. While a set's migrations run, search_path
// is that schema alone; migrations still schema-qualify the names they
// create, so that functions and views do not depend on search_path.
//
// # Conventions for tenant-owned tables (ADR-0002)
//
// Every table that holds tenant data:
//
//   - has tenant_id uuid NOT NULL as the first column of its primary key
//     (and of every unique key and foreign key that references another
//     tenant-owned table, so references cannot cross tenants);
//   - enables and forces row-level security, so the policy applies to the
//     table owner too;
//   - has one policy for all commands that restricts both the rows read and
//     the rows written to the transaction's tenant;
//   - grants exactly the privileges the roles need, explicitly.
//
// For example:
//
//	-- +goose Up
//	CREATE TABLE identity.users (
//	    tenant_id uuid NOT NULL,
//	    id        uuid NOT NULL,
//	    email     text NOT NULL,
//	    PRIMARY KEY (tenant_id, id)
//	);
//	ALTER TABLE identity.users ENABLE ROW LEVEL SECURITY;
//	ALTER TABLE identity.users FORCE ROW LEVEL SECURITY;
//	CREATE POLICY tenant_isolation ON identity.users
//	    USING (tenant_id = current_setting('app.tenant_id')::uuid)
//	    WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);
//	GRANT USAGE ON SCHEMA identity TO portun_app, portun_platform;
//	GRANT SELECT, INSERT, UPDATE, DELETE ON identity.users TO portun_app, portun_platform;
//
//	-- +goose Down
//	DROP TABLE identity.users;
//
// The policy deliberately calls current_setting without missing_ok. When
// app.tenant_id is not set (a statement outside db.InTenantTx), evaluating
// the policy raises an error: "unrecognized configuration parameter" on a
// connection that never set it, "invalid input syntax for type uuid" on one
// that set it in an earlier transaction (the setting then reads as ”). A
// statement that evaluates the policy on no row returns no rows. Either way
// the database fails closed: it never returns or changes another tenant's
// rows. Never write the policy with current_setting(..., true) or a COALESCE
// that could turn a missing tenant into a match.
//
// dbtest.AssertRLS checks these rules on a migrated database.
package migrate

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// Set is one module's migrations: goose *.sql files at the root of FS,
// applied to Schema.
type Set struct {
	Schema string
	FS     fs.FS
}

// VersionTable is the goose version table in each schema.
const VersionTable = "goose_db_version"

// migrationLockID is the advisory lock that serializes Up on one database
// ("portunmg" as a big-endian int64).
const migrationLockID int64 = 0x706f7274756e6d67

// identPattern accepts plain lowercase identifiers (schema and role names),
// which need no quoting.
var identPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// Up applies every pending migration of sets, in order, connecting with
// ownerURL. It is idempotent: a second call applies nothing. Each applied
// migration is logged to logger (nil discards).
func Up(ctx context.Context, logger *slog.Logger, ownerURL secret.String, sets ...Set) error {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if err := validateSets(sets); err != nil {
		return err
	}
	cfg, err := pgx.ParseConfig(ownerURL.Reveal())
	if err != nil {
		// The parse error can quote the URL, password included.
		return errs.New(errs.CodeInvalidRequest, "migrate: the owner database URL is not a valid PostgreSQL connection string")
	}
	lockConn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "migrate: connect to the database")
	}
	// Closing the session releases the advisory lock.
	defer closeConn(ctx, lockConn)
	if _, err := lockConn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return wrap(err, "migrate: take the migration lock")
	}
	for _, s := range sets {
		if err := upSet(ctx, logger, cfg, s); err != nil {
			return err
		}
	}
	return nil
}

func validateSets(sets []Set) error {
	if len(sets) == 0 {
		return errs.New(errs.CodeInternal, "migrate: no migration sets given")
	}
	seen := make(map[string]bool, len(sets))
	for _, s := range sets {
		switch {
		case !identPattern.MatchString(s.Schema),
			strings.HasPrefix(s.Schema, "pg_"),
			s.Schema == "information_schema":
			return errs.New(errs.CodeInternal, "migrate: invalid schema name "+quote(s.Schema))
		case s.FS == nil:
			return errs.New(errs.CodeInternal, "migrate: schema "+quote(s.Schema)+" has no migration file system")
		case seen[s.Schema]:
			return errs.New(errs.CodeInternal, "migrate: schema "+quote(s.Schema)+" is given twice")
		}
		seen[s.Schema] = true
	}
	return nil
}

// upSet creates the schema if needed and applies its pending migrations.
func upSet(ctx context.Context, logger *slog.Logger, base *pgx.ConnConfig, s Set) error {
	cfg := base.Copy()
	cfg.RuntimeParams["search_path"] = s.Schema
	sqlDB := stdlib.OpenDB(*cfg)
	// The schema name matched identPattern; Sanitize quotes it anyway.
	if _, err := sqlDB.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{s.Schema}.Sanitize()); err != nil {
		_ = sqlDB.Close()
		return wrap(err, "migrate: create schema "+quote(s.Schema))
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, s.FS,
		goose.WithTableName(s.Schema+"."+VersionTable),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		_ = sqlDB.Close()
		return errs.Wrap(errs.CodeInternal, err, "migrate: load the migrations of schema "+quote(s.Schema))
	}
	defer func() { _ = provider.Close() }() // closes sqlDB
	results, err := provider.Up(ctx)
	if err != nil {
		return wrap(err, "migrate: apply the migrations of schema "+quote(s.Schema))
	}
	for _, r := range results {
		logger.InfoContext(ctx, "database migration applied",
			slog.String("schema", s.Schema),
			slog.Int64("version", r.Source.Version),
			slog.Duration("duration", r.Duration),
		)
	}
	return nil
}

// wrap maps err with db.MapError, keeping a migration-specific message for
// errors that are not transient. Errors that already carry an errs code pass
// through.
func wrap(err error, msg string) error {
	var e *errs.Error
	if errors.As(err, &e) {
		return err
	}
	if code := errs.CodeOf(db.MapError(err)); code == errs.CodeUnavailable || code == errs.CodeDeadlineExceeded {
		return errs.Wrap(code, err, msg)
	}
	return errs.Wrap(errs.CodeInternal, err, msg)
}

// closeConn closes a connection whose work is done. A failed close needs no
// action: the server ends the session (and releases its advisory locks) when
// the connection drops, and the work's own result is already decided.
func closeConn(ctx context.Context, conn *pgx.Conn) {
	_ = conn.Close(context.WithoutCancel(ctx))
}

func quote(s string) string { return `"` + s + `"` }
