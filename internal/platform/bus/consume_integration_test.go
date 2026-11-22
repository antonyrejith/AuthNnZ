//go:build integration

package bus_test

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

var fastBackoff = []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}

// TestRedeliveryAfterHandlerError proves a failed message is delivered again
// and acknowledged once the handler succeeds, in both orderings.
//
// Covers: GOV-11
func TestRedeliveryAfterHandlerError(t *testing.T) {
	t.Parallel()
	for _, o := range orderings {
		t.Run(o.name, func(t *testing.T) {
			t.Parallel()
			srv, b := newEnv(t)
			var calls atomic.Int32
			var attempts []uint64
			var mu sync.Mutex
			done := make(chan struct{})
			c, err := b.Consume(t.Context(), bus.ConsumerSpec{Stream: bus.StreamEvents, Durable: "retry", Ordering: o.ordering, Backoff: fastBackoff},
				func(_ context.Context, d bus.Delivery) error {
					mu.Lock()
					attempts = append(attempts, d.NumDelivered)
					mu.Unlock()
					if calls.Add(1) < 3 {
						return errs.New(errs.CodeUnavailable, "database down")
					}
					close(done)
					return nil
				})
			if err != nil {
				t.Fatal(err)
			}
			publish(t, b, eventSubject(t, "m", "t"), "e1", nil)
			select {
			case <-done:
			case <-time.After(waitTimeout):
				t.Fatal("message not redelivered to success")
			}
			c.Stop()
			mu.Lock()
			defer mu.Unlock()
			if len(attempts) != 3 || attempts[0] != 1 || attempts[1] != 2 || attempts[2] != 3 {
				t.Fatalf("attempts = %v; want [1 2 3]", attempts)
			}
			info := consumerInfo(t, rawJS(t, srv), bus.StreamEvents, "retry")
			if info.NumAckPending != 0 || info.AckFloor.Stream != 1 {
				t.Fatalf("after success: ack pending %d, ack floor %d", info.NumAckPending, info.AckFloor.Stream)
			}
		})
	}
}

// TestPoisonAndMaxDeliver checks that ErrPoison terminates a message at
// once, that CodeInvalidRequest does not (it is retried), that MaxDeliver terminates a message that keeps failing, and that
// later messages are processed in both cases.
func TestPoisonAndMaxDeliver(t *testing.T) {
	t.Parallel()
	for _, o := range orderings {
		t.Run(o.name, func(t *testing.T) {
			t.Parallel()
			srv, b := newEnv(t)
			subject := eventSubject(t, "m", "t")
			rec := newRecorder()
			c, err := b.Consume(t.Context(), bus.ConsumerSpec{
				Stream: bus.StreamEvents, Durable: "poison", Ordering: o.ordering, MaxDeliver: 3, Backoff: fastBackoff,
			}, func(_ context.Context, d bus.Delivery) error {
				rec.add(d)
				switch d.ID {
				case "poison":
					return fmt.Errorf("decode: %w", bus.ErrPoison)
				case "invalid":
					return errs.New(errs.CodeInvalidRequest, "rejected by a dependency")
				case "failing":
					return errs.New(errs.CodeInternal, "always fails")
				default:
					return nil
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Stop()
			publish(t, b, subject, "poison", nil)
			publish(t, b, subject, "failing", nil)
			publish(t, b, subject, "invalid", nil)
			publish(t, b, subject, "ok", nil)
			js := rawJS(t, srv)
			waitFor(t, "all settled", func() bool {
				info := consumerInfo(t, js, bus.StreamEvents, "poison")
				return rec.count("ok") == 1 && info.NumAckPending == 0 && info.AckFloor.Stream == 4
			})
			if n := rec.count("invalid"); n != 3 {
				t.Errorf("invalid_request message handled %d times; want MaxDeliver=3 (not poison)", n)
			}
			if n := rec.count("poison"); n != 1 {
				t.Errorf("poison message handled %d times; want 1", n)
			}
			if n := rec.count("failing"); n != 3 {
				t.Errorf("failing message handled %d times; want MaxDeliver=3", n)
			}
		})
	}
}

// TestPanicRecovered checks a panicking handler does not crash the process
// and the message is retried.
func TestPanicRecovered(t *testing.T) {
	t.Parallel()
	for _, o := range orderings {
		t.Run(o.name, func(t *testing.T) {
			t.Parallel()
			_, b := newEnv(t)
			var calls atomic.Int32
			done := make(chan struct{})
			c, err := b.Consume(t.Context(), bus.ConsumerSpec{Stream: bus.StreamEvents, Durable: "panic", Ordering: o.ordering, Backoff: fastBackoff},
				func(context.Context, bus.Delivery) error {
					if calls.Add(1) == 1 {
						panic("handler bug")
					}
					close(done)
					return nil
				})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Stop()
			publish(t, b, eventSubject(t, "m", "t"), "e1", nil)
			select {
			case <-done:
			case <-time.After(waitTimeout):
				t.Fatal("message not retried after panic")
			}
		})
	}
}

// TestDurableResume proves a durable consumer stopped part-way resumes where
// it left off: no message is lost, and messages acknowledged before Stop
// are not handled again (unordered) or only as duplicates above the
// acknowledgement floor (ordered).
//
// Covers: GOV-11
func TestDurableResume(t *testing.T) {
	t.Parallel()
	for _, o := range orderings {
		t.Run(o.name, func(t *testing.T) {
			t.Parallel()
			_, b := newEnv(t)
			subject := eventSubject(t, "m", "t")
			const total = 40
			for i := range total / 2 {
				publish(t, b, subject, msgID("r", i), nil)
			}
			spec := bus.ConsumerSpec{Stream: bus.StreamEvents, Durable: "resume", Ordering: o.ordering, Concurrency: 2}
			first := newRecorder()
			c1, err := b.Consume(t.Context(), spec, func(_ context.Context, d bus.Delivery) error {
				first.add(d)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "some messages handled", func() bool { return first.distinct() >= 5 })
			c1.Stop()
			stoppedAt := first.distinct()
			for i := total / 2; i < total; i++ {
				publish(t, b, subject, msgID("r", i), nil)
			}
			if first.distinct() != stoppedAt {
				t.Fatal("handler ran after Stop returned")
			}

			second := newRecorder()
			c2, err := b.Consume(t.Context(), spec, func(_ context.Context, d bus.Delivery) error {
				second.add(d)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer c2.Stop()
			waitFor(t, "every message handled", func() bool {
				for i := range total {
					if first.count(msgID("r", i))+second.count(msgID("r", i)) == 0 {
						return false
					}
				}
				return true
			})
			if o.ordering == bus.Unordered {
				for i := range total {
					if first.count(msgID("r", i)) > 0 && second.count(msgID("r", i)) > 0 {
						t.Errorf("acknowledged message %d handled again after resume", i)
					}
				}
			}
		})
	}
}

// TestOrderedPerSubject publishes many messages for many tenants, fails a
// share of first attempts, and checks that each tenant's messages are first
// handled in publish order, one at a time, with no loss, while tenants run
// in parallel.
//
// Covers: GOV-11
func TestOrderedPerSubject(t *testing.T) {
	t.Parallel()
	_, b := newEnv(t)
	const tenants, perTenant = 12, 40
	subjects := make([]string, tenants)
	for i := range subjects {
		s, err := bus.AuditSubject(mustTenant(t))
		if err != nil {
			t.Fatal(err)
		}
		subjects[i] = s
	}
	// Interleave tenants; each tenant's messages are published in order.
	for j := range perTenant {
		for i, s := range subjects {
			publish(t, b, s, msgID(s, j), []byte{byte(i), byte(j)})
		}
	}

	var (
		mu         sync.Mutex
		order      = map[string][]int{} // first successful handling per subject
		inFlight   = map[string]int{}   // handlers running per subject
		failedOnce = map[string]bool{}  // ids that already failed once
		running    int                  // handlers running overall
		maxRunning int
		violation  string
	)
	c, err := b.Consume(t.Context(), bus.ConsumerSpec{
		Stream: bus.StreamAudit, Durable: "audit-writer", Ordering: bus.OrderedBySubject,
		Concurrency: 8, Backoff: fastBackoff,
	}, func(_ context.Context, d bus.Delivery) error {
		mu.Lock()
		inFlight[d.Subject]++
		running++
		maxRunning = max(maxRunning, running)
		if inFlight[d.Subject] > 1 && violation == "" {
			violation = "two handlers ran at once for " + d.Subject
		}
		fail := !failedOnce[d.ID] && rand.IntN(10) == 0
		failedOnce[d.ID] = true
		mu.Unlock()

		time.Sleep(time.Duration(rand.IntN(300)) * time.Microsecond)

		mu.Lock()
		defer mu.Unlock()
		inFlight[d.Subject]--
		running--
		if fail {
			return errs.New(errs.CodeUnavailable, "transient")
		}
		j := int(d.Data[1])
		seen := order[d.Subject]
		if len(seen) > 0 && j <= seen[len(seen)-1] {
			return nil // a duplicate of an already handled message
		}
		order[d.Subject] = append(seen, j)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()

	waitFor(t, "all messages handled", func() bool {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, s := range order {
			n += len(s)
		}
		return n == tenants*perTenant
	})
	mu.Lock()
	defer mu.Unlock()
	if violation != "" {
		t.Fatal(violation)
	}
	for _, s := range subjects {
		got := order[s]
		if len(got) != perTenant {
			t.Fatalf("%s: handled %d messages; want %d", s, len(got), perTenant)
		}
		for j, v := range got {
			if v != j {
				t.Fatalf("%s: order %v; want 0..%d in sequence", s, got, perTenant-1)
			}
		}
	}
	if maxRunning < 2 {
		t.Errorf("tenants never ran in parallel (max %d handlers at once)", maxRunning)
	}
}

// TestOrderedRewindOnStart simulates a crashed instance that received the
// first message of a tenant without acknowledging it. A new ordered consumer
// must handle that message before the tenant's later ones, without waiting
// for the acknowledgement timeout.
//
// Covers: GOV-11
func TestOrderedRewindOnStart(t *testing.T) {
	t.Parallel()
	srv, b := newEnv(t)
	subject, err := bus.AuditSubject(mustTenant(t))
	if err != nil {
		t.Fatal(err)
	}
	spec := bus.ConsumerSpec{Stream: bus.StreamAudit, Durable: "crashy", Ordering: bus.OrderedBySubject}
	// Create the durable with the same configuration, then take message 1
	// as the "crashed" instance would.
	c0, err := b.Consume(t.Context(), spec, func(context.Context, bus.Delivery) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	c0.Stop()
	publish(t, b, subject, "a1", nil)
	js := rawJS(t, srv)
	cons, err := js.Consumer(t.Context(), bus.StreamAudit, "crashy")
	if err != nil {
		t.Fatal(err)
	}
	batch, err := cons.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	stolen := 0
	for range batch.Messages() {
		stolen++ // never acknowledged
	}
	if stolen != 1 {
		t.Fatalf("crashed instance took %d messages; want 1", stolen)
	}
	publish(t, b, subject, "a2", nil)
	publish(t, b, subject, "a3", nil)

	got := make(chan string, 10)
	start := time.Now()
	c, err := b.Consume(t.Context(), spec, func(_ context.Context, d bus.Delivery) error {
		got <- d.ID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	for _, want := range []string{"a1", "a2", "a3"} {
		select {
		case id := <-got:
			if id != want {
				t.Fatalf("handled %s; want %s first (per-tenant order)", id, want)
			}
		case <-time.After(waitTimeout):
			t.Fatalf("waiting for %s", want)
		}
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("took %v: the rewind did not happen, the ack timeout did", elapsed)
	}
}

// TestOrderedRewindOnGap lets another client take one delivery from a
// running ordered consumer's durable (as a lost delivery would). The
// consumer must notice the gap in delivery sequence, rewind, and still
// handle the tenant's messages in order.
//
// Covers: GOV-11
func TestOrderedRewindOnGap(t *testing.T) {
	t.Parallel()
	srv := startServer(t, t.TempDir())
	logs := &logCapture{}
	b, err := bus.Connect(t.Context(), bus.Config{AllowInsecure: true, URL: srv.ClientURL(), Credentials: srv.Token()}, slog.New(logs))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	if err := b.EnsureStreams(t.Context()); err != nil {
		t.Fatal(err)
	}
	subject, err := bus.AuditSubject(mustTenant(t))
	if err != nil {
		t.Fatal(err)
	}
	spec := bus.ConsumerSpec{Stream: bus.StreamAudit, Durable: "gappy", Ordering: bus.OrderedBySubject}
	c0, err := b.Consume(t.Context(), spec, func(context.Context, bus.Delivery) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	c0.Stop()

	// The thief's pull request is queued first, so it receives message 1.
	js := rawJS(t, srv)
	cons, err := js.Consumer(t.Context(), bus.StreamAudit, "gappy")
	if err != nil {
		t.Fatal(err)
	}
	batch, err := cons.Fetch(1, jetstream.FetchMaxWait(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := js.Conn().Flush(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "thief's pull request registered", func() bool {
		return consumerInfo(t, js, bus.StreamAudit, "gappy").NumWaiting >= 1
	})

	got := make(chan string, 10)
	c, err := b.Consume(t.Context(), spec, func(_ context.Context, d bus.Delivery) error {
		got <- d.ID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	// Consume rewinds on start, which does not remove the thief's request.
	for _, id := range []string{"g1", "g2", "g3"} {
		publish(t, b, subject, id, nil)
	}
	stolen := 0
	for range batch.Messages() {
		stolen++
	}
	if stolen != 1 {
		t.Fatalf("thief took %d messages; want 1", stolen)
	}
	for _, want := range []string{"g1", "g2", "g3"} {
		select {
		case id := <-got:
			if id != want {
				t.Fatalf("handled %s; want %s (per-tenant order after a lost delivery)", id, want)
			}
		case <-time.After(waitTimeout):
			t.Fatalf("waiting for %s", want)
		}
	}
	if !logs.contains("delivery gap") {
		t.Fatal("the consumer did not detect the delivery gap")
	}
}

// TestOrderedRejectsSecondConsumer checks a second ordered Consume on the
// same durable in the same process is refused.
func TestOrderedRejectsSecondConsumer(t *testing.T) {
	t.Parallel()
	_, b := newEnv(t)
	spec := bus.ConsumerSpec{Stream: bus.StreamAudit, Durable: "only", Ordering: bus.OrderedBySubject}
	c, err := b.Consume(t.Context(), spec, func(context.Context, bus.Delivery) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Consume(t.Context(), spec, func(context.Context, bus.Delivery) error { return nil }); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("second ordered Consume = %v; want conflict", err)
	}
	c.Stop()
	c2, err := b.Consume(t.Context(), spec, func(context.Context, bus.Delivery) error { return nil })
	if err != nil {
		t.Fatalf("Consume after Stop: %v", err)
	}
	c2.Stop()
}

// TestCloseDrainsInFlight proves Close waits for a running handler and that
// its acknowledgement reaches the server before Close returns.
//
// Covers: GOV-11
func TestCloseDrainsInFlight(t *testing.T) {
	t.Parallel()
	for _, o := range orderings {
		t.Run(o.name, func(t *testing.T) {
			t.Parallel()
			srv := startServer(t, t.TempDir())
			b, err := bus.Connect(t.Context(), bus.Config{AllowInsecure: true, URL: srv.ClientURL(), Credentials: srv.Token()}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.EnsureStreams(t.Context()); err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			var handlerDone atomic.Bool
			if _, err := b.Consume(t.Context(), bus.ConsumerSpec{Stream: bus.StreamEvents, Durable: "drain", Ordering: o.ordering},
				func(context.Context, bus.Delivery) error {
					close(entered)
					<-release
					handlerDone.Store(true)
					return nil
				}); err != nil {
				t.Fatal(err)
			}
			publish(t, b, eventSubject(t, "m", "t"), "slow", nil)
			<-entered

			closeStarted := make(chan struct{})
			go func() {
				<-closeStarted
				close(release)
			}()
			close(closeStarted)
			if err := b.Close(t.Context()); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if !handlerDone.Load() {
				t.Fatal("Close returned before the in-flight handler finished")
			}
			info := consumerInfo(t, rawJS(t, srv), bus.StreamEvents, "drain")
			if info.NumAckPending != 0 || info.AckFloor.Stream != 1 {
				t.Fatalf("after Close: ack pending %d, ack floor %d; want the message acknowledged", info.NumAckPending, info.AckFloor.Stream)
			}
		})
	}
}

// TestCloseDeadlineCancelsHandlers checks that when Close's deadline passes,
// handler contexts are canceled and Close reports the deadline.
func TestCloseDeadlineCancelsHandlers(t *testing.T) {
	t.Parallel()
	srv := startServer(t, t.TempDir())
	b, err := bus.Connect(t.Context(), bus.Config{AllowInsecure: true, URL: srv.ClientURL(), Credentials: srv.Token()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureStreams(t.Context()); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	canceled := make(chan struct{})
	if _, err := b.Consume(t.Context(), bus.ConsumerSpec{Stream: bus.StreamEvents, Durable: "stuck"},
		func(ctx context.Context, _ bus.Delivery) error {
			close(entered)
			<-ctx.Done()
			close(canceled)
			return ctx.Err()
		}); err != nil {
		t.Fatal(err)
	}
	publish(t, b, eventSubject(t, "m", "t"), "stuck", nil)
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := b.Close(ctx); !errs.Is(err, errs.CodeDeadlineExceeded) {
		t.Fatalf("Close = %v; want deadline_exceeded", err)
	}
	select {
	case <-canceled:
	case <-time.After(waitTimeout):
		t.Fatal("handler context was not canceled")
	}
}

// TestConsumeContextCancelStops checks canceling the Consume context stops
// the consumer gracefully.
func TestConsumeContextCancelStops(t *testing.T) {
	t.Parallel()
	_, b := newEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	c, err := b.Consume(ctx, bus.ConsumerSpec{Stream: bus.StreamEvents, Durable: "ctx"}, func(context.Context, bus.Delivery) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	stopped := make(chan struct{})
	go func() {
		c.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(waitTimeout):
		t.Fatal("consumer did not stop after its context was canceled")
	}
	// The durable is free again.
	c2, err := b.Consume(t.Context(), bus.ConsumerSpec{Stream: bus.StreamEvents, Durable: "ctx", Ordering: bus.OrderedBySubject}, func(context.Context, bus.Delivery) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	c2.Stop()
}

// TestConsumeRejectsInvalidSpec checks configuration errors surface from Consume.
func TestConsumeRejectsInvalidSpec(t *testing.T) {
	t.Parallel()
	_, b := newEnv(t)
	h := func(context.Context, bus.Delivery) error { return nil }
	if _, err := b.Consume(t.Context(), bus.ConsumerSpec{Stream: bus.StreamAudit, Durable: "x.y"}, h); !errs.Is(err, errs.CodeInvalidRequest) {
		t.Fatalf("bad durable = %v", err)
	}
	if _, err := b.Consume(t.Context(), bus.ConsumerSpec{Stream: bus.StreamAudit, Durable: "x"}, nil); !errs.Is(err, errs.CodeInvalidRequest) {
		t.Fatalf("nil handler = %v", err)
	}
	c, err := b.Consume(t.Context(), bus.ConsumerSpec{Stream: bus.StreamAudit, Durable: "fixed"}, h)
	if err != nil {
		t.Fatal(err)
	}
	c.Stop()
	// The deliver policy of an existing durable cannot change.
	if _, err := b.Consume(t.Context(), bus.ConsumerSpec{Stream: bus.StreamAudit, Durable: "fixed", DeliverPolicy: bus.DeliverNew}, h); err == nil {
		t.Fatal("changing the deliver policy of an existing durable succeeded")
	}
}

// TestFilterSubjects checks a consumer only receives its filtered subjects.
func TestFilterSubjects(t *testing.T) {
	t.Parallel()
	_, b := newEnv(t)
	tenant := mustTenant(t)
	other := mustTenant(t)
	mine, err := bus.AuditSubject(tenant)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := bus.AuditSubject(other)
	if err != nil {
		t.Fatal(err)
	}
	rec := newRecorder()
	c, err := b.Consume(t.Context(), bus.ConsumerSpec{Stream: bus.StreamAudit, Durable: "filtered", FilterSubjects: []string{mine, bus.AuditPlatformSubject}},
		func(_ context.Context, d bus.Delivery) error {
			rec.add(d)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	publish(t, b, theirs, "theirs", nil)
	publish(t, b, mine, "mine", nil)
	publish(t, b, bus.AuditPlatformSubject, "platform", nil)
	waitFor(t, "filtered messages", func() bool { return rec.count("mine") == 1 && rec.count("platform") == 1 })
	if rec.count("theirs") != 0 {
		t.Fatal("received another tenant's subject")
	}
}
