//go:build integration

package bus_test

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/antonyrejith/portun/internal/platform/bus"
)

const waitTimeout = 20 * time.Second

func startServer(t *testing.T, storeDir string) *bus.EmbeddedServer {
	t.Helper()
	srv, err := bus.StartEmbedded(t.Context(), bus.EmbeddedConfig{StoreDir: storeDir}, nil)
	if err != nil {
		t.Fatalf("StartEmbedded: %v", err)
	}
	t.Cleanup(srv.Shutdown)
	return srv
}

func connect(t *testing.T, srv *bus.EmbeddedServer) *bus.Bus {
	t.Helper()
	b, err := bus.Connect(t.Context(), bus.Config{URL: srv.ClientURL(), Credentials: srv.Token()}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		defer cancel()
		if err := b.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if err := b.EnsureStreams(t.Context()); err != nil {
		t.Fatalf("EnsureStreams: %v", err)
	}
	return b
}

// newEnv starts a server in a temp dir and connects a bus with the streams.
func newEnv(t *testing.T) (*bus.EmbeddedServer, *bus.Bus) {
	t.Helper()
	srv := startServer(t, t.TempDir())
	return srv, connect(t, srv)
}

// rawJS opens an independent client connection for inspection.
func rawJS(t *testing.T, srv *bus.EmbeddedServer) jetstream.JetStream {
	t.Helper()
	nc, err := nats.Connect(srv.ClientURL(), nats.Token(srv.Token().Reveal()))
	if err != nil {
		t.Fatalf("raw connect: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	return js
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func publish(t *testing.T, b *bus.Bus, subject, id string, data []byte) bus.PubAck {
	t.Helper()
	ack, err := b.Publish(t.Context(), bus.Message{Subject: subject, ID: id, Data: data})
	if err != nil {
		t.Fatalf("Publish(%s, %s): %v", subject, id, err)
	}
	return ack
}

func eventSubject(t *testing.T, module, typ string) string {
	t.Helper()
	s, err := bus.EventSubject(module, typ)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// recorder collects deliveries safely.
type recorder struct {
	mu   sync.Mutex
	seen []bus.Delivery
	ids  map[string]int
}

func newRecorder() *recorder { return &recorder{ids: map[string]int{}} }

func (r *recorder) add(d bus.Delivery) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, d)
	r.ids[d.ID]++
}

func (r *recorder) distinct() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ids)
}

func (r *recorder) count(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ids[id]
}

func msgID(prefix string, i int) string { return fmt.Sprintf("%s-%s", prefix, strconv.Itoa(i)) }

func consumerInfo(t *testing.T, js jetstream.JetStream, stream, durable string) *jetstream.ConsumerInfo {
	t.Helper()
	c, err := js.Consumer(t.Context(), stream, durable)
	if err != nil {
		t.Fatalf("consumer %s: %v", durable, err)
	}
	info, err := c.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return info
}

var orderings = []struct {
	name     string
	ordering bus.Ordering
}{
	{"unordered", bus.Unordered},
	{"ordered", bus.OrderedBySubject},
}

// logCapture is a slog handler that keeps record messages.
type logCapture struct {
	mu   sync.Mutex
	msgs []string
}

func (l *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (l *logCapture) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, r.Message)
	return nil
}

func (l *logCapture) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *logCapture) WithGroup(string) slog.Handler      { return l }

func (l *logCapture) contains(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}
