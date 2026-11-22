//go:build integration

package bus_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// TestEnsureStreams checks that the three streams exist with their subjects,
// storage and duplicate window, and that a second call is a no-op.
func TestEnsureStreams(t *testing.T) {
	t.Parallel()
	srv, b := newEnv(t)
	if err := b.EnsureStreams(t.Context()); err != nil {
		t.Fatalf("second EnsureStreams: %v", err)
	}
	js := rawJS(t, srv)
	want := map[string]struct {
		subject string
		maxAge  time.Duration
	}{
		bus.StreamAudit:  {bus.AuditSubjects, 0},
		bus.StreamEvents: {bus.EventSubjects, 0},
		bus.StreamConfig: {bus.ConfigSubjects, 0},
	}
	for name, w := range want {
		s, err := js.Stream(t.Context(), name)
		if err != nil {
			t.Fatalf("stream %s: %v", name, err)
		}
		cfg := s.CachedInfo().Config
		if len(cfg.Subjects) != 1 || cfg.Subjects[0] != w.subject || cfg.Storage != jetstream.FileStorage ||
			cfg.Retention != jetstream.LimitsPolicy || cfg.Duplicates != 5*time.Minute || cfg.MaxAge != w.maxAge {
			t.Errorf("stream %s config = %+v", name, cfg)
		}
	}
}

// TestEnsureStreamsUpdates checks that changed settings are applied to
// existing streams.
func TestEnsureStreamsUpdates(t *testing.T) {
	t.Parallel()
	srv, _ := newEnv(t)
	b2, err := bus.Connect(t.Context(), bus.Config{URL: srv.ClientURL(), Credentials: srv.Token(), EventsMaxAge: time.Hour}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b2.Close(context.Background()) })
	if err := b2.EnsureStreams(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := rawJS(t, srv).Stream(t.Context(), bus.StreamEvents)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.CachedInfo().Config.MaxAge; got != time.Hour {
		t.Fatalf("EVENTS max age = %v; want 1h", got)
	}
}

// TestPublishDeduplicates proves broker-side deduplication by message ID:
// a relay retry after an unknown outcome stores nothing new.
//
// Covers: GOV-11
func TestPublishDeduplicates(t *testing.T) {
	t.Parallel()
	_, b := newEnv(t)
	subject, err := bus.AuditSubject(mustTenant(t))
	if err != nil {
		t.Fatal(err)
	}
	first := publish(t, b, subject, "row-1", []byte(`{"a":1}`))
	if first.Stream != bus.StreamAudit || first.Duplicate || first.Sequence == 0 {
		t.Fatalf("first ack = %+v", first)
	}
	again := publish(t, b, subject, "row-1", []byte(`{"a":1}`))
	if !again.Duplicate || again.Sequence != first.Sequence {
		t.Fatalf("retry ack = %+v; want duplicate of seq %d", again, first.Sequence)
	}
	other := publish(t, b, subject, "row-2", []byte(`{"a":2}`))
	if other.Duplicate || other.Sequence != first.Sequence+1 {
		t.Fatalf("new message ack = %+v", other)
	}
}

// TestPublishRejectsInvalidMessages checks validation happens before the
// network and carries CodeInvalidRequest.
func TestPublishRejectsInvalidMessages(t *testing.T) {
	t.Parallel()
	_, b := newEnv(t)
	bad := []bus.Message{
		{Subject: "audit.*", ID: "x"},
		{Subject: "elsewhere.a.b", ID: "x"},
		{Subject: bus.AuditPlatformSubject},
		{Subject: bus.AuditPlatformSubject, ID: "x", Header: map[string]string{"Nats-Expected-Stream": "EVENTS"}},
	}
	for i, m := range bad {
		if _, err := b.Publish(t.Context(), m); !errs.Is(err, errs.CodeInvalidRequest) {
			t.Errorf("case %d: Publish error = %v; want invalid_request", i, err)
		}
	}
}

// TestPublishHeadersRoundTrip checks user headers reach the handler and the
// payload is delivered unchanged.
func TestPublishHeadersRoundTrip(t *testing.T) {
	t.Parallel()
	_, b := newEnv(t)
	got := make(chan bus.Delivery, 1)
	c, err := b.Consume(t.Context(), bus.ConsumerSpec{Stream: bus.StreamEvents, Durable: "hdr"}, func(_ context.Context, d bus.Delivery) error {
		got <- d
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	subject := eventSubject(t, "identity", "user_created")
	if _, err := b.Publish(t.Context(), bus.Message{Subject: subject, ID: "e1", Data: []byte("payload"), Header: map[string]string{"Traceparent": "00-abc-def-01"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-got:
		if d.Subject != subject || d.ID != "e1" || string(d.Data) != "payload" || d.Header["Traceparent"] != "00-abc-def-01" ||
			d.Stream != bus.StreamEvents || d.StreamSeq != 1 || d.NumDelivered != 1 || d.Published.IsZero() {
			t.Fatalf("delivery = %+v", d)
		}
		if _, ok := d.Header["Nats-Msg-Id"]; ok {
			t.Fatal("reserved header leaked to the handler")
		}
	case <-time.After(waitTimeout):
		t.Fatal("no delivery")
	}
}

// TestReady checks readiness while connected and after Close.
func TestReady(t *testing.T) {
	t.Parallel()
	srv := startServer(t, t.TempDir())
	b, err := bus.Connect(t.Context(), bus.Config{URL: srv.ClientURL(), Credentials: srv.Token()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Ready(t.Context()); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if err := b.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := b.Ready(t.Context()); !errs.Is(err, errs.CodeUnavailable) {
		t.Fatalf("Ready after Close = %v; want unavailable", err)
	}
	if err := b.Close(t.Context()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := b.Publish(t.Context(), bus.Message{Subject: bus.AuditPlatformSubject, ID: "x"}); !errs.Is(err, errs.CodeUnavailable) {
		t.Fatalf("Publish after Close = %v; want unavailable", err)
	}
	if _, err := b.Consume(t.Context(), bus.ConsumerSpec{Stream: bus.StreamAudit, Durable: "late"}, func(context.Context, bus.Delivery) error { return nil }); !errs.Is(err, errs.CodeUnavailable) {
		t.Fatalf("Consume after Close = %v; want unavailable", err)
	}
}

// TestEmbeddedRequiresToken checks the embedded server rejects clients
// without its token.
func TestEmbeddedRequiresToken(t *testing.T) {
	t.Parallel()
	srv := startServer(t, t.TempDir())
	nc, err := nats.Connect(srv.ClientURL(), nats.MaxReconnects(0))
	if err == nil {
		nc.Close()
		t.Fatal("connected without the token")
	}
	if !errors.Is(err, nats.ErrAuthorization) {
		t.Fatalf("connect error = %v; want authorization violation", err)
	}
	if _, err := bus.Connect(t.Context(), bus.Config{URL: srv.ClientURL()}, nil); !errs.Is(err, errs.CodeUnavailable) {
		t.Fatalf("bus.Connect without token = %v; want unavailable", err)
	}
}

// TestStartEmbeddedRequiresStoreDir checks the store directory is mandatory.
func TestStartEmbeddedRequiresStoreDir(t *testing.T) {
	t.Parallel()
	if _, err := bus.StartEmbedded(t.Context(), bus.EmbeddedConfig{}, nil); !errs.Is(err, errs.CodeInvalidRequest) {
		t.Fatalf("StartEmbedded without store dir = %v", err)
	}
}

// TestEmbeddedPersistence restarts the embedded server on the same store
// directory and checks the stored messages and the durable consumer's
// position survive: nothing acknowledged is redelivered, nothing
// unacknowledged is lost.
//
// Covers: GOV-11
func TestEmbeddedPersistence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	subject := eventSubject(t, "tenancy", "tenant_created")

	srv1, err := bus.StartEmbedded(t.Context(), bus.EmbeddedConfig{StoreDir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b1, err := bus.Connect(t.Context(), bus.Config{URL: srv1.ClientURL(), Credentials: srv1.Token()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := b1.EnsureStreams(t.Context()); err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		publish(t, b1, subject, msgID("p", i), []byte{byte(i)})
	}
	first := newRecorder()
	c1, err := b1.Consume(t.Context(), bus.ConsumerSpec{Stream: bus.StreamEvents, Durable: "persist", Concurrency: 1}, func(_ context.Context, d bus.Delivery) error {
		if d.StreamSeq > 4 {
			return errs.New(errs.CodeUnavailable, "not yet") // leave 5..10 unacknowledged
		}
		first.add(d)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first four acknowledged", func() bool { return first.distinct() == 4 })
	c1.Stop()
	if err := b1.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	srv1.Shutdown()

	srv2 := startServer(t, dir)
	b2 := connect(t, srv2)
	s, err := rawJS(t, srv2).Stream(t.Context(), bus.StreamEvents)
	if err != nil {
		t.Fatal(err)
	}
	if n := s.CachedInfo().State.Msgs; n != 10 {
		t.Fatalf("stream holds %d messages after restart; want 10", n)
	}
	second := newRecorder()
	c2, err := b2.Consume(t.Context(), bus.ConsumerSpec{Stream: bus.StreamEvents, Durable: "persist"}, func(_ context.Context, d bus.Delivery) error {
		second.add(d)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Stop()
	waitFor(t, "remaining six delivered", func() bool { return second.distinct() == 6 })
	for i := range 4 {
		if second.count(msgID("p", i)) != 0 {
			t.Errorf("acknowledged message %d redelivered after restart", i)
		}
	}
	// A retried publish after the restart is still deduplicated.
	if ack := publish(t, b2, subject, msgID("p", 9), []byte{9}); !ack.Duplicate {
		t.Fatalf("publish after restart = %+v; want duplicate", ack)
	}
}

// TestConnectOwnsEmbeddedServer checks Connect without a URL starts an
// embedded server, works end to end and shuts the server down on Close,
// leaving the store reusable.
func TestConnectOwnsEmbeddedServer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	b, err := bus.Connect(t.Context(), bus.Config{Embedded: bus.EmbeddedConfig{StoreDir: dir}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureStreams(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := b.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	publish(t, b, bus.AuditPlatformSubject, "op-1", []byte("x"))
	got := make(chan bus.Delivery, 1)
	if _, err := b.Consume(t.Context(), bus.ConsumerSpec{Stream: bus.StreamAudit, Durable: "owner", Ordering: bus.OrderedBySubject}, func(_ context.Context, d bus.Delivery) error {
		got <- d
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-got:
		if d.ID != "op-1" {
			t.Fatalf("delivery = %+v", d)
		}
	case <-time.After(waitTimeout):
		t.Fatal("no delivery")
	}
	if err := b.Close(t.Context()); err != nil { // also stops the consumer
		t.Fatal(err)
	}
	srv := startServer(t, dir)
	s, err := rawJS(t, srv).Stream(t.Context(), bus.StreamAudit)
	if err != nil {
		t.Fatal(err)
	}
	if n := s.CachedInfo().State.Msgs; n != 1 {
		t.Fatalf("AUDIT holds %d messages; want 1", n)
	}
}
