//go:build integration

package reconcile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
)

// Tracker reconciles one test database from the moment it is created: it
// owns a temporary replication slot and decodes the change stream in the
// background. Integration suites use AssertAllAudited; tests of the
// reconciler itself read Unaudited directly.
type Tracker struct {
	mu        sync.Mutex
	unaudited []Unaudited
	markers   map[string]chan struct{}
	err       error
	admin     *pgx.Conn
	cancel    context.CancelFunc
	done      chan struct{}
}

// trackTimeout bounds waiting for the stream to reach a marker.
const trackTimeout = 30 * time.Second

// AssertAllAudited starts tracking env's database and, when the test ends,
// fails it for every row change no audit record declared (GOV-12). Call it
// before the test writes anything.
func AssertAllAudited(tb testing.TB, env *dbtest.Env, exclude ...string) *Tracker {
	tb.Helper()
	tr := Track(tb, env, exclude...)
	tb.Cleanup(func() {
		for _, u := range tr.Unaudited(tb) {
			tb.Errorf("unaudited change: %s %s key=%v xid=%d: %s", u.Op, u.Table, u.Key, u.XID, u.Reason)
		}
	})
	return tr
}

// Track starts reconciling env's database: it (re)creates the publication
// over every non-system schema except audit (as the owner) and a temporary
// slot (as the superuser, env.AdminURL).
func Track(tb testing.TB, env *dbtest.Env, exclude ...string) *Tracker {
	tb.Helper()
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, env.AdminURL.Reveal())
	if err != nil {
		tb.Fatalf("reconcile: connect: %v", err)
	}
	tb.Cleanup(func() { _ = admin.Close(context.Background()) })
	rows, err := admin.Query(ctx, `SELECT nspname FROM pg_namespace
WHERE nspname NOT LIKE 'pg\_%' AND nspname NOT IN ('information_schema', 'audit') ORDER BY 1`)
	if err != nil {
		tb.Fatalf("reconcile: list schemas: %v", err)
	}
	schemas, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		tb.Fatalf("reconcile: list schemas: %v", err)
	}
	const publication = "portun_reconcile"
	if err := EnsurePublication(ctx, env.OwnerURL, publication, schemas); err != nil {
		tb.Fatalf("reconcile: publication: %v", err)
	}
	rconn, err := connectReplication(ctx, env.AdminURL)
	if err != nil {
		tb.Fatalf("reconcile: %v", err)
	}
	slot := "portun_rec_" + randomHex(tb, 8)
	if _, err := pglogrepl.CreateReplicationSlot(ctx, rconn, slot, "pgoutput", pglogrepl.CreateReplicationSlotOptions{
		Temporary: true, Mode: pglogrepl.LogicalReplication, SnapshotAction: "NOEXPORT_SNAPSHOT",
	}); err != nil {
		_ = rconn.Close(ctx)
		tb.Fatalf("reconcile: create slot: %v", err)
	}
	kconn, err := pgx.Connect(ctx, env.AdminURL.Reveal())
	if err != nil {
		tb.Fatalf("reconcile: connect: %v", err)
	}
	keys := &keyCache{conn: kconn, cache: map[string][]string{}}
	runCtx, cancel := context.WithCancel(ctx)
	s := &session{conn: rconn, dec: newDecoder(Exclusions(exclude), keys.resolve(runCtx))}
	if err := s.start(ctx, slot, publication, 0); err != nil {
		cancel()
		tb.Fatalf("reconcile: %v", err)
	}
	tr := &Tracker{markers: map[string]chan struct{}{}, admin: admin, cancel: cancel, done: make(chan struct{})}
	go tr.loop(runCtx, s)
	tb.Cleanup(func() {
		cancel()
		<-tr.done
		_ = rconn.Close(context.Background())
		_ = kconn.Close(context.Background())
		// The temporary slot goes away with its session; wait for it, so
		// that the test database can be dropped.
		deadline := time.Now().Add(trackTimeout)
		for time.Now().Before(deadline) {
			var n int
			if err := admin.QueryRow(context.Background(), `SELECT count(*) FROM pg_replication_slots WHERE slot_name = $1`, slot).Scan(&n); err != nil || n == 0 {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	return tr
}

func (tr *Tracker) loop(ctx context.Context, s *session) {
	defer close(tr.done)
	for {
		ev, err := s.next(ctx)
		if err != nil {
			tr.mu.Lock()
			if ctx.Err() == nil {
				tr.err = err
			}
			for _, ch := range tr.markers {
				close(ch)
			}
			tr.markers = map[string]chan struct{}{}
			tr.mu.Unlock()
			return
		}
		tr.mu.Lock()
		tr.unaudited = append(tr.unaudited, ev.unaudited...)
		if ch, ok := tr.markers[ev.marker]; ok && ev.marker != "" {
			close(ch)
			delete(tr.markers, ev.marker)
		}
		tr.mu.Unlock()
		if ev.commit {
			_ = s.status(ctx)
		}
	}
}

// Unaudited waits until the stream has passed every change committed so
// far and returns the findings since the previous call.
func (tr *Tracker) Unaudited(tb testing.TB) []Unaudited {
	tb.Helper()
	token := randomHex(tb, 8)
	ch := make(chan struct{})
	tr.mu.Lock()
	if tr.err != nil {
		tr.mu.Unlock()
		tb.Fatalf("reconcile: stream failed: %v", tr.err)
	}
	tr.markers[token] = ch
	tr.mu.Unlock()
	if err := emitMarker(context.Background(), tr.admin, token); err != nil {
		tb.Fatalf("reconcile: %v", err)
	}
	select {
	case <-ch:
	case <-time.After(trackTimeout):
		tb.Fatal("reconcile: timed out waiting for the change stream")
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.err != nil {
		tb.Fatalf("reconcile: stream failed: %v", tr.err)
	}
	out := tr.unaudited
	tr.unaudited = nil
	return out
}

func randomHex(tb testing.TB, n int) string {
	tb.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		tb.Fatal(err)
	}
	return hex.EncodeToString(b)
}
