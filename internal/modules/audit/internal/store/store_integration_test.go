//go:build integration

package store_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store/storetest"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/verify"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

func verifyDB(t *testing.T, d *db.DB, tenant ids.TenantID) *verify.Report {
	t.Helper()
	var rep *verify.Report
	err := d.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rep, err = verify.Chain(ctx, verify.DBSource{Q: tx, Chain: store.ChainOf(tenant)}, verify.Options{})
		return err
	}, db.RepeatableRead())
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestAppendGaplessParallel appends to many chains concurrently.
//
// Covers: GOV-01
func TestAppendGaplessParallel(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	a := store.NewAppender(nil)
	tenants := []ids.TenantID{{}, storetest.NewTenant(t), storetest.NewTenant(t), storetest.NewTenant(t)}
	const perChain, workers = 15, 3
	var wg sync.WaitGroup
	for _, tn := range tenants {
		for range workers {
			wg.Go(func() {
				for range perChain {
					storetest.Append(t, env.DB, a, storetest.Event(t, tn, "widget.created"))
				}
			})
		}
	}
	wg.Wait()
	for _, tn := range tenants {
		rep := verifyDB(t, env.DB, tn)
		if !rep.OK() || rep.Events != perChain*workers || rep.ToSeq != perChain*workers {
			t.Errorf("chain %s: %+v", store.ChainName(tn.UUID()), rep)
		}
	}
}

// TestAppendDuplicate skips a record whose ID is stored.
//
// Covers: GOV-01
func TestAppendDuplicate(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	a := store.NewAppender(nil)
	tn := storetest.NewTenant(t)
	ev := storetest.Event(t, tn, "widget.created")
	first := storetest.Append(t, env.DB, a, ev, storetest.Event(t, tn, "widget.updated"))
	again := storetest.Append(t, env.DB, a, ev)
	if first[0].Duplicate || !again[0].Duplicate || again[0].Sequence != 1 {
		t.Fatalf("first %+v again %+v", first[0], again[0])
	}
	if n := storetest.Count(t, env.DB, `SELECT count(*) FROM audit.events WHERE tenant_id = $1`, tn.UUID()); n != 2 {
		t.Errorf("events = %d, want 2", n)
	}
}

// TestAppendOnly checks that no role can change or remove history outside
// the retention path.
//
// Covers: GOV-01
func TestAppendOnly(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	a := store.NewAppender(nil)
	tn := storetest.NewTenant(t)
	storetest.Append(t, env.DB, a, storetest.Event(t, tn, "widget.created"))
	err := env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		return store.PublishSigningKey(ctx, tx, store.SigningKey{KeyRef: "k", Version: 1, PublicKey: []byte{1}})
	})
	if err != nil {
		t.Fatal(err)
	}
	// Statements on tables that hold rows (row triggers fire per row).
	statements := []string{
		`UPDATE audit.events SET outcome = 'failure'`,
		`DELETE FROM audit.events`,
		`UPDATE audit.event_ids SET sequence = 9`,
		`DELETE FROM audit.event_ids`,
		`DELETE FROM audit.signing_keys`,
		`TRUNCATE audit.events`,
	}
	ctx := t.Context()
	for _, who := range []struct {
		name string
		url  string
	}{{"platform", env.PlatformURL.Reveal()}, {"app", env.AppURL.Reveal()}, {"owner", env.OwnerURL.Reveal()}} {
		conn, err := pgx.Connect(ctx, who.url)
		if err != nil {
			t.Fatal(err)
		}
		for _, stmt := range statements {
			_, err := conn.Exec(ctx, stmt)
			if err == nil {
				t.Errorf("%s: %q succeeded", who.name, stmt)
			}
		}
		_ = conn.Close(ctx)
	}
	// The owner may not call the retention marker path by hand either: the
	// trigger needs the owner and the marker set by apply_retention.
	conn, err := pgx.Connect(ctx, env.PlatformURL.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{`DELETE FROM audit.checkpoints`, `DELETE FROM audit.retention_marks`, `UPDATE audit.retention_marks SET last_hash = ''`} {
		if _, err := conn.Exec(ctx, stmt); err == nil {
			t.Errorf("platform: %q succeeded", stmt)
		}
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, `SELECT set_config('portun.audit_retention', 'on', false)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `DELETE FROM audit.events`); err == nil {
		t.Error("platform delete with the retention marker succeeded")
	}
	if n := storetest.Count(t, env.DB, `SELECT count(*) FROM audit.events`); n != 1 {
		t.Errorf("events = %d, want 1", n)
	}
}

// TestRowLevelSecurity checks that tenants read only their own chain and
// never the platform chain.
//
// Covers: GOV-01
func TestRowLevelSecurity(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	dbtest.AssertRLS(t, env, "audit")
	a := store.NewAppender(nil)
	t1, t2 := storetest.NewTenant(t), storetest.NewTenant(t)
	storetest.Append(t, env.DB, a, storetest.Event(t, t1, "a.created"), storetest.Event(t, t2, "b.created"),
		storetest.Event(t, ids.TenantID{}, "tenant.created"))
	ctx := scope.WithTenant(t.Context(), scope.Tenant{TenantID: t1})
	err := env.DB.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT event_type FROM audit.events`)
		if err != nil {
			return err
		}
		types, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if strings.Join(types, ",") != "a.created" {
			t.Errorf("tenant sees %v", types)
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := sp.Exec(ctx, `INSERT INTO audit.chain_heads VALUES ($1, 0, 'x', now())`, t1.UUID()); err == nil {
			t.Error("tenant wrote a chain head")
		}
		return sp.Rollback(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := storetest.Count(t, env.DB, `SELECT count(*) FROM pg_inherits WHERE inhparent = 'audit.events'::regclass`); n < 2 {
		t.Errorf("partitions = %d, want current and next month", n)
	}
}
