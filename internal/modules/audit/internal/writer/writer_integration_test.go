//go:build integration

package writer_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store/storetest"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/verify"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/writer"
	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

type harness struct {
	t     *testing.T
	env   *dbtest.Env
	bus   *bus.Bus
	admin *pgx.Conn
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	env := dbtest.New(t)
	admin, err := pgx.Connect(t.Context(), env.AdminURL.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	return &harness{t: t, env: env, bus: storetest.Bus(t), admin: admin}
}

// emit writes ev to the outbox (as the change transaction would) and
// publishes it with the outbox row ID (as the relay would).
func (h *harness) emit(ev audit.Event) {
	h.t.Helper()
	id := uuid.Must(uuid.NewV7())
	payload := storetest.JSON(h.t, ev)
	subject := bus.AuditPlatformSubject
	if !ev.TenantID.IsZero() {
		var err error
		if subject, err = bus.AuditSubject(ev.TenantID); err != nil {
			h.t.Fatal(err)
		}
	}
	if _, err := h.admin.Exec(h.t.Context(), `INSERT INTO outbox.messages (tenant_id, id, stream, subject, payload) VALUES ($1, $2, 'AUDIT', $3, $4)`,
		ev.TenantID.UUID(), id, subject, payload); err != nil {
		h.t.Fatal(err)
	}
	h.publish(subject, id.String(), payload)
}

func (h *harness) publish(subject, id string, data []byte) {
	h.t.Helper()
	if _, err := h.bus.Publish(h.t.Context(), bus.Message{Subject: subject, ID: id, Data: data}); err != nil {
		h.t.Fatal(err)
	}
}

// start runs a writer until the returned stop function is called.
func (h *harness) start() func() {
	h.t.Helper()
	w, err := writer.New(writer.Config{
		Logger: storetest.Logger(),
		Bus:    h.bus, DB: h.env.DB, LockURL: h.env.PlatformURL,
		LockRetry: 100 * time.Millisecond, Backoff: []time.Duration{50 * time.Millisecond},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Run(ctx)
	}()
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			cancel()
			<-done
		}
	}
	h.t.Cleanup(stop)
	return stop
}

func (h *harness) count(sql string, args ...any) int64 {
	h.t.Helper()
	return storetest.Count(h.t, h.env.DB, sql, args...)
}

func (h *harness) waitEvents(tenant ids.TenantID, n int64) {
	h.t.Helper()
	storetest.WaitFor(h.t, "stored events", func() bool {
		return h.count(`SELECT count(*) FROM audit.events WHERE tenant_id = $1`, tenant.UUID()) >= n
	})
}

func (h *harness) verify(tenant ids.TenantID, events uint64) {
	h.t.Helper()
	var rep *verify.Report
	err := h.env.DB.InPlatformTx(h.t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rep, err = verify.Chain(ctx, verify.DBSource{Q: tx, Chain: store.ChainOf(tenant)}, verify.Options{})
		return err
	}, db.RepeatableRead())
	if err != nil {
		h.t.Fatal(err)
	}
	if !rep.OK() || rep.Events != events || rep.ToSeq != events {
		h.t.Errorf("chain %s: %+v", store.ChainName(tenant.UUID()), rep)
	}
}

// TestWriterChainsAndDeletesOutbox stores the records of several chains in
// parallel, gapless and verifiable, and removes their outbox rows.
//
// Covers: GOV-01
func TestWriterChainsAndDeletesOutbox(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.start()
	tenants := []ids.TenantID{{}, storetest.NewTenant(t), storetest.NewTenant(t), storetest.NewTenant(t)}
	const n = 12
	for i := range n {
		for _, tn := range tenants {
			h.emit(storetest.Event(t, tn, "widget.v"+string(rune('a'+i))))
		}
	}
	for _, tn := range tenants {
		h.waitEvents(tn, n)
		h.verify(tn, n)
	}
	storetest.WaitFor(t, "outbox drained", func() bool { return h.count(`SELECT count(*) FROM outbox.messages`) == 0 })
	// Records carry their position in order of the stream.
	var types []string
	err := h.env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT event_type FROM audit.events WHERE tenant_id = $1 ORDER BY sequence`, tenants[1].UUID())
		if err != nil {
			return err
		}
		types, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, typ := range types {
		if typ != "widget.v"+string(rune('a'+i)) {
			t.Fatalf("event %d is %s: stream order not kept", i+1, typ)
		}
	}
}

// TestWriterRestartAndDuplicates kills the writer mid-stream, restarts it,
// and redelivers duplicates: the chain stays gapless with each record once.
//
// Covers: GOV-01
func TestWriterRestartAndDuplicates(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tn := storetest.NewTenant(t)
	const n = 40
	var events []audit.Event
	for range n {
		ev := storetest.Event(t, tn, "widget.updated")
		events = append(events, ev)
		h.emit(ev)
	}
	stop := h.start()
	h.waitEvents(tn, 5)
	stop()
	stored := h.count(`SELECT count(*) FROM audit.events WHERE tenant_id = $1`, tn.UUID())
	// Redeliver some records under new message IDs (a relay retry after
	// the duplicate window, or a crash between commit and ack).
	subject, err := bus.AuditSubject(tn)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events[:10] {
		h.publish(subject, uuid.NewString(), storetest.JSON(t, ev))
	}
	h.start()
	h.waitEvents(tn, n)
	storetest.WaitFor(t, "duplicates consumed", func() bool {
		return h.count(`SELECT count(*) FROM outbox.messages`) == 0
	})
	time.Sleep(300 * time.Millisecond)
	if got := h.count(`SELECT count(*) FROM audit.events WHERE tenant_id = $1`, tn.UUID()); got != n {
		t.Fatalf("events = %d, want %d (stored %d before the restart)", got, n, stored)
	}
	h.verify(tn, n)
}

// TestWriterSingleActive runs two writers: one holds the lock, the other
// waits, and takes over when the first stops.
//
// Covers: GOV-01
func TestWriterSingleActive(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	stopA := h.start()
	h.start()
	lockHolders := func() int64 {
		return h.count(`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND granted AND objid = $1
AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, writer.LockID&0xffffffff)
	}
	storetest.WaitFor(t, "a lock holder", func() bool { return lockHolders() == 1 })
	tn := storetest.NewTenant(t)
	for range 10 {
		h.emit(storetest.Event(t, tn, "widget.created"))
	}
	h.waitEvents(tn, 10)
	stopA()
	for range 10 {
		h.emit(storetest.Event(t, tn, "widget.updated"))
	}
	h.waitEvents(tn, 20)
	if got := lockHolders(); got != 1 {
		t.Errorf("lock holders = %d", got)
	}
	h.verify(tn, 20)
}

// TestWriterRejects keeps records that cannot be chained and continues.
//
// Covers: GOV-01
func TestWriterRejects(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.start()
	tn, other := storetest.NewTenant(t), storetest.NewTenant(t)
	subject, err := bus.AuditSubject(tn)
	if err != nil {
		t.Fatal(err)
	}
	h.publish(subject, uuid.NewString(), []byte(`{"not":"a record"}`))
	h.publish(subject, uuid.NewString(), storetest.JSON(t, storetest.Event(t, other, "widget.created")))
	h.emit(storetest.Event(t, tn, "widget.created"))
	h.waitEvents(tn, 1)
	storetest.WaitFor(t, "rejected records kept", func() bool {
		return h.count(`SELECT count(*) FROM audit.rejected_messages`) == 2
	})
	if got := h.count(`SELECT count(*) FROM audit.events WHERE tenant_id = $1`, other.UUID()); got != 0 {
		t.Errorf("a record was stored on the wrong chain")
	}
	h.verify(tn, 1)
}
