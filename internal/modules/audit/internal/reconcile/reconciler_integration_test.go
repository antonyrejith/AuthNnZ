//go:build integration

package reconcile_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/reconcile"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store/storetest"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
)

// TestReconcilerAlerts runs the production reconciler on a persistent slot
// and receives the unaudited-change alert.
//
// Covers: GOV-12
func TestReconcilerAlerts(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	if err := reconcile.EnsurePublication(t.Context(), env.OwnerURL, "portun_audit", []string{"audittest_app", "outbox"}); err != nil {
		t.Fatal(err)
	}
	// A second call updates the existing publication.
	if err := reconcile.EnsurePublication(t.Context(), env.OwnerURL, "portun_audit", []string{"audittest_app", "outbox"}); err != nil {
		t.Fatal(err)
	}
	found := make(chan reconcile.Unaudited, 10)
	slot := "portun_rec_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	r, err := reconcile.New(reconcile.Config{
		ReplicationURL: env.AdminURL, Slot: slot, Publication: "portun_audit", RetryDelay: 100 * time.Millisecond,
		Logger:      storetest.Logger(),
		OnUnaudited: func(_ context.Context, u reconcile.Unaudited) { found <- u },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconcile.New(reconcile.Config{Slot: slot}); err == nil {
		t.Error("New accepted a config without a URL")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		dropSlot(t, env, slot)
	})
	storetest.WaitFor(t, "slot", func() bool {
		return storetest.Count(t, env.DB, `SELECT count(*) FROM pg_replication_slots WHERE slot_name = $1 AND active`, slot) == 1
	})
	tn := storetest.NewTenant(t)
	w1, w2 := uuid.New(), uuid.New()
	change(t, env, tn, []audit.RowRef{widgetRow(tn, w1)}, insertWidget(tn, w1))
	change(t, env, tn, nil, insertWidget(tn, w2))
	select {
	case u := <-found:
		if u.Table != "audittest_app.widgets" || len(u.Key) != 2 || u.Key[1] != w2.String() {
			t.Errorf("alert = %+v", u)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no alert")
	}
	select {
	case u := <-found:
		t.Errorf("unexpected alert %+v", u)
	case <-time.After(300 * time.Millisecond):
	}
}

func dropSlot(t *testing.T, env *dbtest.Env, slot string) {
	t.Helper()
	admin, err := pgx.Connect(context.Background(), env.AdminURL.Reveal())
	if err != nil {
		t.Error(err)
		return
	}
	defer func() { _ = admin.Close(context.Background()) }()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := admin.Exec(context.Background(), `SELECT pg_drop_replication_slot($1)`, slot); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("could not drop the replication slot")
}
