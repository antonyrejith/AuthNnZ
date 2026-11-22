//go:build integration

package reconcile_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/reconcile"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store/storetest"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

// change runs fn in a tenant transaction and, when rows is not nil, writes
// an audit record declaring them to the outbox in the same transaction.
func change(t *testing.T, env *dbtest.Env, tn ids.TenantID, rows []audit.RowRef, fn func(ctx context.Context, tx pgx.Tx) error) {
	t.Helper()
	ctx := scope.WithTenant(t.Context(), scope.Tenant{TenantID: tn})
	err := env.DB.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		if rows == nil {
			return nil
		}
		ev := storetest.Event(t, tn, "widget.created")
		ev.Change = &audit.ChangeSet{Rows: rows}
		_, err := tx.Exec(ctx, `INSERT INTO outbox.messages (tenant_id, id, stream, subject, payload) VALUES ($1, $2, 'AUDIT', $3, $4)`,
			tn.UUID(), uuid.New(), "audit."+tn.String(), storetest.JSON(t, ev))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func insertWidget(tn ids.TenantID, id uuid.UUID) func(ctx context.Context, tx pgx.Tx) error {
	return func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO audittest_app.widgets (tenant_id, id, name) VALUES ($1, $2, 'w')`, tn.UUID(), id)
		return err
	}
}

func widgetRow(tn ids.TenantID, id uuid.UUID) audit.RowRef {
	return audit.RowRef{Table: "audittest_app.widgets", Key: []string{tn.UUID().String(), id.String()}}
}

// TestReconcile checks each reconciliation outcome against a real change
// stream.
//
// Covers: GOV-12
func TestReconcile(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	tr := reconcile.Track(t, env, "audittest_app.rate_counters")
	tn := storetest.NewTenant(t)
	type finding struct{ table, op string }
	tests := []struct {
		name string
		run  func()
		want []finding
	}{
		{
			name: "declared change",
			run: func() {
				w := uuid.New()
				change(t, env, tn, []audit.RowRef{widgetRow(tn, w)}, insertWidget(tn, w))
			},
		},
		{
			name: "extra undeclared row in the same transaction",
			run: func() {
				w1, w2 := uuid.New(), uuid.New()
				change(t, env, tn, []audit.RowRef{widgetRow(tn, w1)}, func(ctx context.Context, tx pgx.Tx) error {
					if err := insertWidget(tn, w1)(ctx, tx); err != nil {
						return err
					}
					return insertWidget(tn, w2)(ctx, tx)
				})
			},
			want: []finding{{"audittest_app.widgets", "insert"}},
		},
		{
			name: "write without an audit record",
			run: func() {
				change(t, env, tn, nil, func(ctx context.Context, tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `UPDATE audittest_app.widgets SET name = 'x'`)
					return err
				})
			},
			want: []finding{{"audittest_app.widgets", "update"}, {"audittest_app.widgets", "update"}, {"audittest_app.widgets", "update"}},
		},
		{
			name: "excluded table and outbox bookkeeping",
			run: func() {
				change(t, env, tn, nil, func(ctx context.Context, tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `INSERT INTO audittest_app.rate_counters (tenant_id, k, n) VALUES ($1, 'login', 1)`, tn.UUID())
					return err
				})
				err := env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
					if _, err := tx.Exec(ctx, `UPDATE outbox.messages SET published_at = now()`); err != nil {
						return err
					}
					_, err := tx.Exec(ctx, `DELETE FROM outbox.messages`)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		tt.run()
		got := tr.Unaudited(t)
		if len(got) != len(tt.want) {
			t.Fatalf("%s: findings = %+v, want %v", tt.name, got, tt.want)
		}
		for i, u := range got {
			if u.Table != tt.want[i].table || u.Op != tt.want[i].op || u.XID == 0 || len(u.Key) != 2 {
				t.Errorf("%s: finding %d = %+v, want %v", tt.name, i, u, tt.want[i])
			}
		}
	}
}

// TestAssertAllAudited is a suite whose every change is declared.
//
// Covers: GOV-12
func TestAssertAllAudited(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	reconcile.AssertAllAudited(t, env)
	tn := storetest.NewTenant(t)
	for range 3 {
		w := uuid.New()
		change(t, env, tn, []audit.RowRef{widgetRow(tn, w)}, insertWidget(tn, w))
	}
}
