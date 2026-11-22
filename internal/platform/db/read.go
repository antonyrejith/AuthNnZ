package db

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Querier is the read side of a transaction: it can run queries but has no
// Exec, CopyFrom or batch methods. Read callbacks receive only this, so code
// that merely reads never holds anything it could write with; writes go
// through audit.Change (ADR-0008, guarantee 2).
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ReadTenant runs fn in a read-only transaction bound to the context's
// tenant (see InTenantTx). PostgreSQL rejects any write in it.
func (d *DB) ReadTenant(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	return d.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error { return fn(ctx, tx) }, ReadOnly())
}

// ReadPlatform runs fn in a read-only platform transaction (see
// InPlatformTx), for lookups made before a tenant is known, such as
// resolving a host. PostgreSQL rejects any write in it.
func (d *DB) ReadPlatform(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	return d.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error { return fn(ctx, tx) }, ReadOnly())
}
