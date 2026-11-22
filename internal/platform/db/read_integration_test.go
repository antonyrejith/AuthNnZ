//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// TestReadHelpersAreReadOnly proves ReadTenant and ReadPlatform cannot
// write even when the callback tries to through the underlying
// transaction, and that ReadTenant needs a tenant scope.
//
// Covers: GOV-11, TEN-03
func TestReadHelpersAreReadOnly(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	tenant, err := ids.New[ids.TenantKind]()
	if err != nil {
		t.Fatal(err)
	}
	ctx := scope.WithTenant(t.Context(), scope.Tenant{TenantID: tenant})
	write := func(ctx context.Context, q db.Querier) error {
		// A write attempted through the read interface must fail: the
		// transaction is read-only.
		var one int
		return q.QueryRow(ctx, `CREATE TEMP TABLE read_only_probe(x int) ON COMMIT DROP; SELECT 1`).Scan(&one)
	}
	tests := []struct {
		name string
		run  func() error
	}{
		{"tenant", func() error { return env.DB.ReadTenant(ctx, write) }},
		{"platform", func() error { return env.DB.ReadPlatform(t.Context(), write) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := tt.run(); err == nil {
				t.Fatal("a write succeeded inside a read-only helper")
			}
		})
	}
	err = env.DB.ReadTenant(t.Context(), func(context.Context, db.Querier) error { return nil })
	if !errs.Is(err, errs.CodeInternal) {
		t.Fatalf("ReadTenant without scope: %v, want internal", err)
	}
	var n int
	if err := env.DB.ReadPlatform(t.Context(), func(ctx context.Context, q db.Querier) error {
		return q.QueryRow(ctx, `SELECT 1`).Scan(&n)
	}); err != nil || n != 1 {
		t.Fatalf("ReadPlatform select: %d, %v", n, err)
	}
}
