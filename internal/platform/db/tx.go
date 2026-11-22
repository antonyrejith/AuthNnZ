package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/antonyrejith/portun/internal/platform/scope"
)

// TxFunc is the work done inside a transaction. It must not commit or roll
// back tx; the caller does. Returning an error rolls the transaction back.
type TxFunc func(ctx context.Context, tx pgx.Tx) error

// TxOption changes how a transaction starts.
type TxOption func(*pgx.TxOptions)

// ReadOnly starts a READ ONLY transaction.
func ReadOnly() TxOption {
	return func(o *pgx.TxOptions) { o.AccessMode = pgx.ReadOnly }
}

// Serializable starts a SERIALIZABLE transaction. Such transactions can fail
// with a serialization failure (CodeConflict, Retryable reports true) and
// should then be retried as a whole.
func Serializable() TxOption {
	return func(o *pgx.TxOptions) { o.IsoLevel = pgx.Serializable }
}

// RepeatableRead starts a REPEATABLE READ transaction.
func RepeatableRead() TxOption {
	return func(o *pgx.TxOptions) { o.IsoLevel = pgx.RepeatableRead }
}

// txOptions applies opts to the defaults: READ COMMITTED, READ WRITE.
func txOptions(opts []TxOption) pgx.TxOptions {
	o := pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// setScopeSQL binds a transaction to a tenant scope. Both settings are always
// set transaction-locally: without an environment, app.environment_id is the
// empty string, so environment-scoped policies fail closed on the uuid cast
// instead of reading a value left on the connection.
const setScopeSQL = `SELECT set_config('` + TenantSetting + `', $1, true), set_config('` + EnvironmentSetting + `', $2, true)`

// InTenantTx runs fn in a transaction on the application pool, bound to the
// tenant and environment of the context scope (the environment setting is
// the empty string when the scope has none). Row-level security
// limits every statement in fn to that tenant's rows. Without a tenant scope
// it fails with CodeInternal before touching the database.
//
// The transaction commits when fn returns nil; it rolls back when fn returns
// an error or panics (the panic continues after the rollback). Errors are
// mapped with MapError.
func (d *DB) InTenantTx(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error, opts ...TxOption) error {
	t, err := scope.TenantFrom(ctx)
	if err != nil {
		return err //nolint:wrapcheck // already an *errs.Error with CodeInternal
	}
	bind := func(ctx context.Context, tx pgx.Tx) error {
		environment := ""
		if !t.EnvironmentID.IsZero() {
			environment = t.EnvironmentID.UUID().String()
		}
		if _, err := tx.Exec(ctx, setScopeSQL, t.TenantID.UUID().String(), environment); err != nil {
			return fmt.Errorf("bind the transaction to the tenant scope: %w", err)
		}
		return nil
	}
	return runTx(ctx, d.app, txOptions(opts), bind, fn)
}

// InPlatformTx runs fn in a transaction on the platform pool, which bypasses
// row-level security. Only platform components use it (ADR-0002). Commit,
// rollback and error mapping are as for InTenantTx.
func (d *DB) InPlatformTx(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error, opts ...TxOption) error {
	return runTx(ctx, d.platform, txOptions(opts), nil, fn)
}

// rollbackTimeout bounds a rollback, which runs even when ctx is done.
const rollbackTimeout = 5 * time.Second

// runTx begins a transaction, runs setup (if any) and fn, and commits.
func runTx(ctx context.Context, pool *pgxpool.Pool, o pgx.TxOptions, setup, fn TxFunc) error {
	tx, err := pool.BeginTx(ctx, o)
	if err != nil {
		return MapError(err)
	}
	defer func() {
		if p := recover(); p != nil {
			rollback(ctx, tx)
			panic(p)
		}
	}()
	if setup != nil {
		if err := setup(ctx, tx); err != nil {
			rollback(ctx, tx)
			return MapError(err)
		}
	}
	if err := fn(ctx, tx); err != nil {
		rollback(ctx, tx)
		return MapError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return MapError(err)
	}
	return nil
}

// rollback rolls tx back even if ctx is done. A failed rollback is logged
// only: pgx then closes the connection, which ends the transaction on the
// server, and the caller already returns the original error.
func rollback(ctx context.Context, tx pgx.Tx) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	if err := tx.Rollback(rctx); err != nil {
		slog.WarnContext(ctx, "database transaction rollback failed", slog.String("error", err.Error()))
	}
}
