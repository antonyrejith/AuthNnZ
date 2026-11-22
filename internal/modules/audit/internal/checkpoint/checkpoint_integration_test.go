//go:build integration

package checkpoint_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/checkpoint"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/checkpoint/tsatest"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store/storetest"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/verify"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

// TestCheckpoints signs, timestamps, links and proves checkpoints, and
// verifies the stored chain against them.
//
// Covers: GOV-18
func TestCheckpoints(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	k, ref := storetest.SigningKey(t)
	tsa := tsatest.Start(t)
	cp, err := checkpoint.New(checkpoint.Config{
		Logger: storetest.Logger(),
		DB:     env.DB, KMS: k, Key: ref, TSA: checkpoint.HTTPStamper{URL: tsa.URL, Roots: tsa.Roots}, MaxRange: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	a := store.NewAppender(nil)
	t1, t2 := storetest.NewTenant(t), storetest.NewTenant(t)
	for range 10 {
		storetest.Append(t, env.DB, a, storetest.Event(t, t1, "widget.created"))
	}
	storetest.Append(t, env.DB, a, storetest.Event(t, t2, "widget.created"), storetest.Event(t, ids.TenantID{}, "tenant.created"))
	n, err := cp.RunOnce(t.Context())
	if err != nil || n != 4 { // t1: 1..7, 8..10; t2: 1..1; platform: 1..1
		t.Fatalf("RunOnce = %d, %v; want 4", n, err)
	}
	if n, err := cp.RunOnce(t.Context()); err != nil || n != 0 {
		t.Fatalf("second RunOnce = %d, %v; want 0", n, err)
	}
	storetest.Append(t, env.DB, a, storetest.Event(t, t1, "widget.updated"))
	if n, err := cp.RunOnce(t.Context()); err != nil || n != 1 {
		t.Fatalf("third RunOnce = %d, %v; want 1", n, err)
	}
	err = env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		cps, err := store.Checkpoints(ctx, tx, t1.UUID())
		if err != nil {
			return err
		}
		if len(cps) != 3 || cps[2].FromSeq != 11 || cps[2].ToSeq != 11 || *cps[2].PrevID != cps[1].ID || cps[0].PrevID != nil {
			t.Errorf("checkpoints = %+v", cps)
		}
		rep, err := verify.Chain(ctx, verify.DBSource{Q: tx, Chain: t1.UUID()}, verify.Options{TSARoots: tsa.Roots, RequireTSA: true})
		if err != nil {
			return err
		}
		if !rep.OK() || rep.TSAVerified != 3 || rep.Events != 11 || rep.Unanchored != 0 {
			t.Errorf("verify = %+v", rep)
		}
		for seq := uint64(1); seq <= 11; seq++ {
			p, err := checkpoint.Prove(ctx, tx, t1.UUID(), seq)
			if err != nil {
				return err
			}
			if !p.Verify() {
				t.Errorf("inclusion proof of %d does not verify", seq)
			}
			p.Hash = p.Checkpoint.Root
			if p.Verify() {
				t.Errorf("a proof for another hash verified")
			}
		}
		if _, err := checkpoint.Prove(ctx, tx, t1.UUID(), 12); !errs.Is(err, errs.CodeNotFound) {
			t.Errorf("Prove(12) = %v, want not_found", err)
		}
		return nil
	}, db.RepeatableRead())
	if err != nil {
		t.Fatal(err)
	}
	if n := storetest.Count(t, env.DB, `SELECT count(*) FROM audit.signing_keys`); n != 1 {
		t.Errorf("published keys = %d", n)
	}
}

// TestCheckpointNeedsTSA takes no checkpoint while the timestamp authority
// fails, and catches up afterwards.
//
// Covers: GOV-18
func TestCheckpointNeedsTSA(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	k, ref := storetest.SigningKey(t)
	tsa := tsatest.Start(t)
	cp, err := checkpoint.New(checkpoint.Config{Logger: storetest.Logger(), DB: env.DB, KMS: k, Key: ref, TSA: checkpoint.HTTPStamper{URL: tsa.URL}})
	if err != nil {
		t.Fatal(err)
	}
	tn := storetest.NewTenant(t)
	storetest.Append(t, env.DB, store.NewAppender(nil), storetest.Event(t, tn, "widget.created"))
	tsa.Fail.Store(true)
	if _, err := cp.RunOnce(t.Context()); err == nil {
		t.Fatal("RunOnce succeeded without a timestamp")
	}
	if n := storetest.Count(t, env.DB, `SELECT count(*) FROM audit.checkpoints`); n != 0 {
		t.Fatalf("checkpoints = %d", n)
	}
	tsa.Fail.Store(false)
	if n, err := cp.RunOnce(t.Context()); err != nil || n != 1 {
		t.Fatalf("RunOnce = %d, %v", n, err)
	}
}
