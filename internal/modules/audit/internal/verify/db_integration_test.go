//go:build integration

package verify_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store/storetest"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/verify"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

func verifyStore(t *testing.T, env *dbtest.Env, chain store.StoredEvent, opts verify.Options) *verify.Report {
	t.Helper()
	var rep *verify.Report
	err := env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rep, err = verify.Chain(ctx, verify.DBSource{Q: tx, Chain: chain.Chain}, opts)
		return err
	}, db.RepeatableRead())
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestVerifyDatabase verifies a stored chain, by time range, and detects
// a record changed in the database behind the store's back.
//
// Covers: GOV-18
func TestVerifyDatabase(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	tn := storetest.NewTenant(t)
	base := time.Now().UTC().Add(-time.Hour)
	for i := range 6 {
		at := base.Add(time.Duration(i) * time.Minute)
		a := store.NewAppender(func() time.Time { return at })
		storetest.Append(t, env.DB, a, storetest.Event(t, tn, "widget.updated"))
	}
	ref := store.StoredEvent{Chain: tn.UUID()}
	rep := verifyStore(t, env, ref, verify.Options{})
	if !rep.OK() || rep.Events != 6 || rep.Unanchored != 6 {
		t.Fatalf("report = %+v", rep)
	}
	rep = verifyStore(t, env, ref, verify.Options{From: base.Add(90 * time.Second), To: base.Add(4 * time.Minute)})
	if !rep.OK() || rep.FromSeq != 3 || rep.ToSeq != 5 {
		t.Fatalf("time range report = %+v", rep)
	}
	rep = verifyStore(t, env, ref, verify.Options{From: base.Add(24 * time.Hour)})
	if !rep.OK() || rep.Events != 0 {
		t.Fatalf("empty range report = %+v", rep)
	}
	admin, err := pgx.Connect(t.Context(), env.AdminURL.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(context.Background()) }()
	// A superuser can bypass the append-only trigger; verification still
	// catches the change.
	if _, err := admin.Exec(t.Context(), `SET session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `UPDATE audit.events SET event = jsonb_set(event, '{outcome}', '"failure"') WHERE tenant_id = $1 AND sequence = 4`, tn.UUID()); err != nil {
		t.Fatal(err)
	}
	rep = verifyStore(t, env, ref, verify.Options{})
	if f := rep.First(); f == nil || f.Kind != verify.KindHashMismatch || f.Sequence != 4 {
		t.Fatalf("report = %+v", rep)
	}
}
