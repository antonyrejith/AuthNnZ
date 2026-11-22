// Package bypass is a fixture for the architecture test: every function here
// writes to the database without audit.Change and must be reported.
package bypass

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/antonyrejith/portun/internal/platform/db"
)

func tenantWrite(ctx context.Context, d *db.DB) error {
	return d.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "DELETE FROM identity.users")
		return err
	})
}

func platformWrite(ctx context.Context, d *db.DB) error {
	return d.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error { return nil })
}

func rawWrite(ctx context.Context, p *pgxpool.Pool) error {
	tx, err := p.Begin(ctx)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
