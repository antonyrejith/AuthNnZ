//go:build integration

package outbox_test

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/outbox"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(m, dbtest.Options{Sets: []migrate.Set{outbox.Migrations()}}))
}

const waitTimeout = 20 * time.Second

func newTenant(t *testing.T) ids.TenantID {
	t.Helper()
	id, err := ids.New[ids.TenantKind]()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func tenantCtx(t *testing.T, id ids.TenantID) context.Context {
	t.Helper()
	return scope.WithTenant(t.Context(), scope.Tenant{TenantID: id})
}

func auditMsg(t *testing.T, tenant ids.TenantID) outbox.Message {
	t.Helper()
	subject := bus.AuditPlatformSubject
	if !tenant.IsZero() {
		s, err := bus.AuditSubject(tenant)
		if err != nil {
			t.Fatal(err)
		}
		subject = s
	}
	return outbox.Message{TenantID: tenant, ID: uuid.Must(uuid.NewV7()), Stream: bus.StreamAudit, Subject: subject, Payload: []byte(`{"k":"v"}`)}
}

func eventMsg(t *testing.T, tenant ids.TenantID) outbox.Message {
	t.Helper()
	return outbox.Message{TenantID: tenant, ID: uuid.Must(uuid.NewV7()), Stream: bus.StreamEvents, Subject: "evt.test.happened", Payload: []byte(`{"n":1}`)}
}

func insertTenant(t *testing.T, d *db.DB, tenant ids.TenantID, msgs ...outbox.Message) {
	t.Helper()
	err := d.InTenantTx(tenantCtx(t, tenant), func(ctx context.Context, tx pgx.Tx) error {
		for _, m := range msgs {
			if err := outbox.Insert(ctx, tx, m); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func count(t *testing.T, d *db.DB, where string, args ...any) int {
	t.Helper()
	var n int
	err := d.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM outbox.messages WHERE `+where, args...).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestOutboxIsolation: row-level security follows the conventions; the
// application role may only insert its own tenant's rows and never read,
// update or delete them, nor insert platform messages.
//
// Covers: TEN-01, GOV-11
func TestOutboxIsolation(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	dbtest.AssertRLS(t, env, outbox.Schema)
	a, b := newTenant(t), newTenant(t)
	insertTenant(t, env.DB, a, auditMsg(t, a))

	cases := []struct {
		name string
		fn   func(ctx context.Context, tx pgx.Tx) error
	}{
		{"insert for another tenant", func(ctx context.Context, tx pgx.Tx) error { return outbox.Insert(ctx, tx, auditMsg(t, b)) }},
		{"insert platform message", func(ctx context.Context, tx pgx.Tx) error {
			return outbox.Insert(ctx, tx, auditMsg(t, ids.TenantID{}))
		}},
		{"select", func(ctx context.Context, tx pgx.Tx) error {
			var n int
			return tx.QueryRow(ctx, `SELECT count(*) FROM outbox.messages`).Scan(&n)
		}},
		{"update", func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE outbox.messages SET published_at = now()`)
			return err
		}},
		{"delete", func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM outbox.messages`)
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if err := env.DB.InTenantTx(tenantCtx(t, a), c.fn); err == nil {
				t.Errorf("%s as the application role succeeded", c.name)
			}
		})
	}
	if got := count(t, env.DB, "tenant_id = $1", a.UUID()); got != 1 {
		t.Errorf("tenant A rows = %d, want 1", got)
	}
	if got := count(t, env.DB, "tenant_id <> $1", a.UUID()); got != 0 {
		t.Errorf("foreign rows = %d, want 0", got)
	}
}

// TestInsertRecordsXID: each row carries its transaction's ID, and messages
// are validated before insert.
//
// Covers: GOV-11
func TestInsertRecordsXID(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	a := newTenant(t)
	m := auditMsg(t, a)
	var txXID, rowXID string
	err := env.DB.InTenantTx(tenantCtx(t, a), func(ctx context.Context, tx pgx.Tx) error {
		if err := outbox.Insert(ctx, tx, m); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT pg_current_xact_id()::text`).Scan(&txXID)
	})
	if err != nil {
		t.Fatal(err)
	}
	err = env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT xid::text FROM outbox.messages WHERE id = $1`, m.ID).Scan(&rowXID)
	})
	if err != nil || rowXID != txXID {
		t.Errorf("row xid = %q, transaction xid = %q, err %v", rowXID, txXID, err)
	}

	bad := []outbox.Message{
		{TenantID: a, Stream: bus.StreamAudit, Subject: "audit.platform", Payload: []byte(`{}`)},
		{TenantID: a, ID: uuid.New(), Stream: bus.StreamAudit, Subject: bus.AuditPlatformSubject, Payload: []byte(`{}`)},
		{TenantID: a, ID: uuid.New(), Stream: bus.StreamEvents, Subject: "evt.x", Payload: []byte(`{}`)},
		{TenantID: a, ID: uuid.New(), Stream: bus.StreamConfig, Subject: "config." + a.String() + ".nope", Payload: []byte(`{}`)},
		{TenantID: a, ID: uuid.New(), Stream: "OTHER", Subject: "x", Payload: []byte(`{}`)},
		{TenantID: a, ID: uuid.New(), Stream: bus.StreamEvents, Subject: "evt.a.b", Payload: []byte(`{`)},
	}
	for i, m := range bad {
		if err := m.Validate(); err == nil {
			t.Errorf("bad message %d validated", i)
		}
	}
	envID, _ := ids.New[ids.EnvironmentKind]()
	good := outbox.Message{TenantID: a, ID: uuid.New(), Stream: bus.StreamConfig, Subject: "config." + a.String() + "." + envID.String(), Payload: []byte(`{}`)}
	if err := good.Validate(); err != nil {
		t.Errorf("config message: %v", err)
	}
}

// natsEnv is an embedded NATS server the test can stop and restart.
type natsEnv struct {
	dir  string
	port int
	srv  *bus.EmbeddedServer
	cfg  bus.EmbeddedConfig
}

func startNATS(t *testing.T) *natsEnv {
	t.Helper()
	n := &natsEnv{dir: t.TempDir()}
	n.cfg = bus.EmbeddedConfig{StoreDir: n.dir}
	n.start(t)
	u, err := url.Parse(n.srv.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	n.port, _ = strconv.Atoi(u.Port())
	n.cfg.Port = n.port
	n.cfg.Token = n.srv.Token()
	return n
}

func (n *natsEnv) start(t *testing.T) {
	t.Helper()
	srv, err := bus.StartEmbedded(t.Context(), n.cfg, nil)
	if err != nil {
		t.Fatalf("StartEmbedded: %v", err)
	}
	n.srv = srv
	t.Cleanup(srv.Shutdown)
}

func (n *natsEnv) connect(t *testing.T) *bus.Bus {
	t.Helper()
	b, err := bus.Connect(t.Context(), bus.Config{URL: n.srv.ClientURL(), AllowInsecure: true, Credentials: n.srv.Token(), ReconnectWait: 50 * time.Millisecond}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		defer cancel()
		_ = b.Close(ctx)
	})
	if err := b.EnsureStreams(t.Context()); err != nil {
		t.Fatal(err)
	}
	return b
}

func (n *natsEnv) streamMsgs(t *testing.T, stream string) uint64 {
	t.Helper()
	nc, err := nats.Connect(n.srv.ClientURL(), nats.Token(n.srv.Token().Reveal()))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	s, err := js.Stream(t.Context(), stream)
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return info.State.Msgs
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRelay: EVENTS rows are published and deleted; AUDIT rows are marked
// published, kept for the writer, republished after the timeout and
// deduplicated by the bus; Delete removes them.
//
// Covers: GOV-11
func TestRelay(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	n := startNATS(t)
	b := n.connect(t)
	a := newTenant(t)
	ev, au := eventMsg(t, a), auditMsg(t, a)
	insertTenant(t, env.DB, a, ev, au)
	platform := auditMsg(t, ids.TenantID{})
	if err := env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		return outbox.Insert(ctx, tx, platform)
	}); err != nil {
		t.Fatal(err)
	}

	r := outbox.NewRelay(env.DB, b, outbox.RelayConfig{RepublishAfter: time.Second}, nil)
	got, err := r.RunOnce(t.Context())
	if err != nil || got != 3 {
		t.Fatalf("RunOnce = %d, %v; want 3", got, err)
	}
	if c := count(t, env.DB, "id = $1", ev.ID); c != 0 {
		t.Error("EVENTS row not deleted after publish")
	}
	if c := count(t, env.DB, "stream = 'AUDIT' AND published_at IS NOT NULL"); c != 2 {
		t.Errorf("published AUDIT rows = %d, want 2", c)
	}
	if got, _ := r.RunOnce(t.Context()); got != 0 {
		t.Errorf("AUDIT rows republished before the timeout: %d", got)
	}
	time.Sleep(1100 * time.Millisecond)
	if got, err := r.RunOnce(t.Context()); err != nil || got != 2 {
		t.Errorf("republish after timeout = %d, %v; want 2", got, err)
	}
	if m := n.streamMsgs(t, bus.StreamAudit); m != 2 {
		t.Errorf("AUDIT stream holds %d messages, want 2 (duplicates removed)", m)
	}
	if m := n.streamMsgs(t, bus.StreamEvents); m != 1 {
		t.Errorf("EVENTS stream holds %d messages, want 1", m)
	}
	err = env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		if err := outbox.Delete(ctx, tx, a, au.ID); err != nil {
			return err
		}
		if err := outbox.Delete(ctx, tx, ids.TenantID{}, platform.ID); err != nil {
			return err
		}
		return outbox.Delete(ctx, tx, a, au.ID) // idempotent
	})
	if err != nil {
		t.Fatal(err)
	}
	if c := count(t, env.DB, "true"); c != 0 {
		t.Errorf("%d rows left after the writer's deletes", c)
	}
}

// TestRelayBusDown: while the bus is down rows stay in the outbox; Run
// delivers them once it is back.
//
// Covers: GOV-11
func TestRelayBusDown(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	n := startNATS(t)
	b := n.connect(t)
	a := newTenant(t)
	n.srv.Shutdown()
	msgs := []outbox.Message{eventMsg(t, a), eventMsg(t, a), auditMsg(t, a)}
	insertTenant(t, env.DB, a, msgs...)

	r := outbox.NewRelay(env.DB, b, outbox.RelayConfig{PollInterval: 20 * time.Millisecond, ErrorBackoff: 50 * time.Millisecond}, nil)
	if _, err := r.RunOnce(t.Context()); err == nil {
		t.Fatal("RunOnce with the bus down succeeded")
	} else if c := errs.CodeOf(err); c != errs.CodeUnavailable && c != errs.CodeDeadlineExceeded {
		t.Errorf("error code %s", c)
	}
	if c := count(t, env.DB, "published_at IS NULL"); c != 3 {
		t.Fatalf("rows waiting = %d, want 3", c)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	n.start(t)
	waitFor(t, "delivery after the bus returned", func() bool { return count(t, env.DB, "published_at IS NULL") == 0 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if c := count(t, env.DB, "stream = 'EVENTS'"); c != 0 {
		t.Errorf("EVENTS rows left: %d", c)
	}
	if m := n.streamMsgs(t, bus.StreamEvents); m != 2 {
		t.Errorf("EVENTS stream holds %d, want 2", m)
	}
}
