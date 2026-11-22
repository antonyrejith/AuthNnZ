package bus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// DeliverPolicy selects where a new durable consumer starts. It only applies
// when the durable is first created.
type DeliverPolicy int

// Deliver policies.
const (
	DeliverAll DeliverPolicy = iota // from the first message in the stream
	DeliverNew                      // only messages stored after the consumer is created
)

// Ordering selects how a consumer dispatches messages to its handler.
type Ordering int

// Orderings.
const (
	// Unordered runs up to Concurrency handlers at once in any order. A
	// failed message is negatively acknowledged with the backoff delay and
	// redelivered by the server, possibly to another instance.
	Unordered Ordering = iota

	// OrderedBySubject runs handlers for the same subject one at a time in
	// stream order, and handlers for different subjects in parallel (up to
	// Concurrency). A failed message is retried in place after the backoff
	// delay and blocks later messages of its subject until it succeeds or is
	// terminated. See Consume for the exact guarantee and its limits.
	OrderedBySubject
)

// ConsumerSpec describes a durable pull consumer.
type ConsumerSpec struct {
	Stream         string        // StreamAudit, StreamEvents or StreamConfig
	Durable        string        // durable name; the consumer's position survives restarts
	FilterSubjects []string      // optional filters within the stream's namespace ("*" and trailing ">" allowed)
	DeliverPolicy  DeliverPolicy // where a new durable starts (default DeliverAll)
	Ordering       Ordering      // default Unordered
	Concurrency    int           // handlers running at once (default 8)
	MaxAckPending  int           // unacknowledged messages the server hands out (default 1000)
	AckWait        time.Duration // server redelivery timeout, kept alive while a message is held (default 30s, minimum 1s)
	MaxDeliver     int           // attempts before a failing message is terminated; <= 0 means unlimited (default)
	Backoff        []time.Duration
	// Backoff holds the delays before retry n (the last one repeats);
	// default 1s, 5s, 30s, 1m.
}

const (
	defaultConcurrency   = 8
	defaultMaxAckPending = 1000
	defaultAckWait       = 30 * time.Second
	minAckWait           = time.Second
	maxPullBuffer        = 500
	reopenMinDelay       = 100 * time.Millisecond
	reopenMaxDelay       = 5 * time.Second
)

func defaultBackoff() []time.Duration {
	return []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, time.Minute}
}

// normalize validates the spec and fills defaults.
func (s ConsumerSpec) normalize() (ConsumerSpec, error) {
	if _, ok := streamPrefix(s.Stream); !ok {
		return s, errs.New(errs.CodeInvalidRequest, "consumer: unknown stream "+strconv.Quote(s.Stream))
	}
	if err := validateName(s.Durable); err != nil {
		return s, errs.Wrap(errs.CodeInvalidRequest, err, "consumer: invalid durable name")
	}
	for _, f := range s.FilterSubjects {
		if err := validateFilter(s.Stream, f); err != nil {
			return s, err
		}
	}
	if s.DeliverPolicy != DeliverAll && s.DeliverPolicy != DeliverNew {
		return s, errs.New(errs.CodeInvalidRequest, "consumer: unknown deliver policy")
	}
	if s.Ordering != Unordered && s.Ordering != OrderedBySubject {
		return s, errs.New(errs.CodeInvalidRequest, "consumer: unknown ordering")
	}
	if s.Concurrency < 0 || s.MaxAckPending < 0 || s.AckWait < 0 {
		return s, errs.New(errs.CodeInvalidRequest, "consumer: concurrency, max ack pending and ack wait must not be negative")
	}
	if s.Concurrency == 0 {
		s.Concurrency = defaultConcurrency
	}
	if s.MaxAckPending == 0 {
		s.MaxAckPending = defaultMaxAckPending
	}
	if s.AckWait == 0 {
		s.AckWait = defaultAckWait
	}
	if s.AckWait < minAckWait {
		return s, errs.New(errs.CodeInvalidRequest, "consumer: ack wait must be at least 1s")
	}
	if s.MaxDeliver < 0 {
		s.MaxDeliver = 0
	}
	if s.Backoff == nil {
		s.Backoff = defaultBackoff()
	}
	for _, d := range s.Backoff {
		if d < 0 {
			return s, errs.New(errs.CodeInvalidRequest, "consumer: backoff delays must not be negative")
		}
	}
	s.FilterSubjects = slices.Clone(s.FilterSubjects)
	s.Backoff = slices.Clone(s.Backoff)
	return s, nil
}

func (s ConsumerSpec) consumerConfig() jetstream.ConsumerConfig {
	cfg := jetstream.ConsumerConfig{
		Durable:       s.Durable,
		Description:   "Portun bus consumer",
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       s.AckWait,
		// Unlimited on the server: a message is only ever given up by an
		// explicit, logged Term (MaxDeliver is enforced by the client).
		MaxDeliver:    -1,
		MaxAckPending: s.MaxAckPending,
		ReplayPolicy:  jetstream.ReplayInstantPolicy,
	}
	if s.DeliverPolicy == DeliverNew {
		cfg.DeliverPolicy = jetstream.DeliverNewPolicy
	}
	switch len(s.FilterSubjects) {
	case 0:
	case 1:
		cfg.FilterSubject = s.FilterSubjects[0]
	default:
		cfg.FilterSubjects = s.FilterSubjects
	}
	return cfg
}

// Delivery is one message handed to a Handler.
type Delivery struct {
	Subject      string
	ID           string            // the publisher's message ID (Nats-Msg-Id)
	Data         []byte            // a copy; the handler may keep or modify it
	Header       map[string]string // user headers (reserved Nats- headers removed)
	Stream       string
	StreamSeq    uint64
	NumDelivered uint64    // attempt number, starting at 1
	Published    time.Time // when the stream stored the message
}

// Handler processes one delivery. Returning nil acknowledges the message.
// An error carrying errs.CodeInvalidRequest marks the message as poison: it
// is terminated (never redelivered) and logged. Any other error, and a panic
// (which is recovered and logged), retries the message after the backoff
// delay, until MaxDeliver attempts when it is set. Handlers must be
// idempotent on Delivery.ID: delivery is at least once. The context is
// canceled only when a shutdown deadline passes (Consumer stop through
// Bus.Close); handlers must honor it.
type Handler func(ctx context.Context, d Delivery) error

// outcome is how a handler result settles a message.
type outcome int

const (
	outcomeAck   outcome = iota // processed
	outcomeRetry                // retry later
	outcomeTerm                 // give up: poison or attempts exhausted
)

// classify maps a handler result to an outcome; for outcomeTerm it also
// returns the reason sent to the server's advisory.
func classify(err error, attempt uint64, maxDeliver int) (outcome, string) {
	switch {
	case err == nil:
		return outcomeAck, ""
	case errs.Is(err, errs.CodeInvalidRequest):
		return outcomeTerm, "poison: invalid_request"
	case maxDeliver > 0 && attempt >= uint64(maxDeliver):
		return outcomeTerm, "max deliveries reached"
	default:
		return outcomeRetry, ""
	}
}

// backoffDelay returns the delay before retrying after failed attempt n (1-based).
func backoffDelay(backoff []time.Duration, attempt uint64) time.Duration {
	if len(backoff) == 0 {
		return 0
	}
	if attempt == 0 {
		return backoff[0]
	}
	if attempt >= uint64(len(backoff)) {
		return backoff[len(backoff)-1]
	}
	return backoff[attempt-1]
}

// entry is a received message that has not been settled yet.
type entry struct {
	msg       jetstream.Msg // latest delivery handle (guarded by Consumer.mu)
	key       string        // serialization key: the subject (ordered) or the sequence (unordered)
	seq       uint64
	delivered uint64 // server delivery count when received
	retries   uint64 // local retries (ordered mode)
	subject   string
	id        string
	data      []byte
	header    map[string]string
	stream    string
	published time.Time
}

// Consumer is a running durable consumer started by Bus.Consume.
type Consumer struct {
	bus    *Bus
	spec   ConsumerSpec
	cfg    jetstream.ConsumerConfig
	handle Handler
	logger *slog.Logger

	runCtx    context.Context // parent of handler contexts; canceled on hard stop
	cancelRun context.CancelFunc
	stopCh    chan struct{} // closed when a stop is requested
	stopOnce  sync.Once
	done      chan struct{} // closed when fully stopped
	unwatch   func() bool   // releases the context.AfterFunc on the Consume context
	sem       chan struct{}

	keepaliveStop chan struct{}
	keepaliveDone chan struct{}

	mu      sync.Mutex
	iter    jetstream.MessagesContext
	held    map[uint64]*entry   // every unsettled message, by stream sequence
	queues  map[string][]*entry // not yet started, per key, in stream order
	workers sync.WaitGroup
}

// Consume creates or updates the durable consumer described by spec and
// processes its messages with handle until Stop, Bus.Close or the
// cancellation of ctx (which stops gracefully, like Stop). Configuration
// errors are returned before any message is processed.
//
// Delivery is at least once. While a message is held by the consumer
// (queued, running or waiting to retry), its acknowledgement deadline is
// extended every AckWait/3, so the server does not redeliver it.
//
// OrderedBySubject guarantees, for each subject, that the first successful
// handling of every message happens in stream-sequence order, one handler at
// a time, while different subjects run in parallel. Redelivered duplicates
// of messages already handled may arrive later; handlers ignore them by ID.
// To keep this across restarts and lost deliveries, the consumer rewinds the
// durable to its acknowledgement floor (JetStream consumer reset) when it
// starts and whenever it sees a gap in the consumer delivery sequence; the
// server then redelivers everything after the floor in stream order.
// Limits: exactly one ordered Consume may run per durable at a time across
// all processes (a second one in this process is rejected with
// CodeConflict); a subject whose head message keeps failing holds its later
// messages, and once held messages reach MaxAckPending delivery pauses for
// all subjects; a rewind resets the server's delivery counts. The server must
// support consumer reset (NATS 2.14 or later).
func (b *Bus) Consume(ctx context.Context, spec ConsumerSpec, handle Handler) (*Consumer, error) {
	spec, err := spec.normalize()
	if err != nil {
		return nil, err
	}
	if handle == nil {
		return nil, errs.New(errs.CodeInvalidRequest, "consumer: handler is required")
	}
	runCtx, cancelRun := context.WithCancel(context.WithoutCancel(ctx))
	c := &Consumer{
		bus:           b,
		spec:          spec,
		cfg:           spec.consumerConfig(),
		handle:        handle,
		logger:        b.logger.With(slog.String("stream", spec.Stream), slog.String("durable", spec.Durable)),
		runCtx:        runCtx,
		cancelRun:     cancelRun,
		stopCh:        make(chan struct{}),
		done:          make(chan struct{}),
		sem:           make(chan struct{}, spec.Concurrency),
		keepaliveStop: make(chan struct{}),
		keepaliveDone: make(chan struct{}),
		held:          map[uint64]*entry{},
		queues:        map[string][]*entry{},
	}
	if err := b.register(c); err != nil {
		cancelRun()
		return nil, err
	}
	it, err := c.open(ctx)
	if err != nil {
		b.unregister(c)
		cancelRun()
		return nil, err
	}
	c.iter = it
	c.unwatch = context.AfterFunc(ctx, c.requestStop)
	go c.keepalive()
	go c.run(it)
	c.logger.InfoContext(ctx, "bus consumer started", slog.Bool("ordered", spec.Ordering == OrderedBySubject))
	return c, nil
}

// Stop stops pulling messages, waits for running handlers to finish, and
// negatively acknowledges messages that were received but not started, so
// they are redelivered promptly. It is safe to call more than once.
func (c *Consumer) Stop() {
	c.requestStop()
	<-c.done
}

// stop is Stop bounded by ctx: when ctx ends first, handler contexts are
// canceled and the context error is returned without waiting further.
func (c *Consumer) stop(ctx context.Context) error {
	c.requestStop()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		c.cancelRun()
		return errs.Wrap(errs.CodeOf(ctx.Err()), ctx.Err(), "consumer "+c.spec.Durable+": stop deadline passed")
	}
}

func (c *Consumer) requestStop() {
	c.stopOnce.Do(func() {
		c.mu.Lock()
		close(c.stopCh)
		it := c.iter
		c.mu.Unlock()
		if it != nil {
			it.Stop()
		}
	})
}

func (c *Consumer) stopping() bool {
	select {
	case <-c.stopCh:
		return true
	default:
		return false
	}
}

// open creates or updates the durable, rewinds it to the acknowledgement
// floor in ordered mode, and starts pulling.
func (c *Consumer) open(ctx context.Context) (jetstream.MessagesContext, error) {
	ctx, cancel := c.bus.withTimeout(ctx)
	defer cancel()
	cons, err := c.bus.js.CreateOrUpdateConsumer(ctx, c.spec.Stream, c.cfg)
	if err != nil {
		return nil, errs.Wrap(apiCode(err), err, "consumer "+c.spec.Durable+": create")
	}
	if c.spec.Ordering == OrderedBySubject {
		if _, err := c.bus.js.ResetConsumer(ctx, c.spec.Stream, c.spec.Durable); err != nil {
			return nil, errs.Wrap(apiCode(err), err, "consumer "+c.spec.Durable+": rewind to acknowledgement floor")
		}
	}
	it, err := cons.Messages(jetstream.PullMaxMessages(min(c.spec.MaxAckPending, maxPullBuffer)))
	if err != nil {
		return nil, errs.Wrap(apiCode(err), err, "consumer "+c.spec.Durable+": start pulling")
	}
	return it, nil
}

// run is the consumer's owner goroutine: it pulls messages and dispatches
// them, and on exit shuts the consumer down.
func (c *Consumer) run(it jetstream.MessagesContext) {
	defer c.finish()
	var expected uint64 = 1 // next consumer delivery sequence after a rewind (ordered mode)
	for it != nil {
		msg, err := it.Next()
		if err != nil {
			if c.stopping() {
				return
			}
			c.logger.Warn("bus consumer pull failed; reopening", slog.String("error", err.Error()))
			it, expected = c.reopen(it), 1
			continue
		}
		meta, err := msg.Metadata()
		if err != nil {
			c.logger.Error("bus message without JetStream metadata", slog.String("error", err.Error()))
			if c.spec.Ordering == OrderedBySubject {
				it, expected = c.reopen(it), 1
			} else if err := msg.Nak(); err != nil {
				c.logger.Warn("bus nak failed", slog.String("error", err.Error()))
			}
			continue
		}
		if c.spec.Ordering == OrderedBySubject {
			if meta.Sequence.Consumer != expected {
				// A delivery was lost: rewind so the server redelivers in order.
				c.logger.Warn("bus ordered consumer saw a delivery gap; rewinding",
					slog.Uint64("expected_delivery_seq", expected), slog.Uint64("delivery_seq", meta.Sequence.Consumer))
				it, expected = c.reopen(it), 1
				continue
			}
			expected++
		}
		c.dispatch(msg, meta)
	}
}

// reopen stops the current iterator and opens a new one, retrying with
// backoff. It returns nil when the consumer is stopping.
func (c *Consumer) reopen(old jetstream.MessagesContext) jetstream.MessagesContext {
	old.Stop()
	delay := reopenMinDelay
	for !c.stopping() {
		it, err := c.open(c.runCtx)
		if err == nil {
			c.mu.Lock()
			if c.stopping() {
				c.mu.Unlock()
				it.Stop()
				return nil
			}
			c.iter = it
			c.mu.Unlock()
			return it
		}
		c.logger.Warn("bus consumer reopen failed", slog.String("error", err.Error()))
		if !c.sleep(delay) {
			return nil
		}
		delay = min(delay*2, reopenMaxDelay)
	}
	return nil
}

// sleep waits for d; it returns false if a stop was requested meanwhile.
func (c *Consumer) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-c.stopCh:
		return false
	}
}

func (c *Consumer) dispatch(msg jetstream.Msg, meta *jetstream.MsgMetadata) {
	c.mu.Lock()
	defer c.mu.Unlock()
	seq := meta.Sequence.Stream
	if e, ok := c.held[seq]; ok {
		// A redelivery of a message we still hold: keep its position, use
		// the newest handle for settling.
		e.msg = msg
		return
	}
	key := msg.Subject()
	if c.spec.Ordering == Unordered {
		key = strconv.FormatUint(seq, 10)
	}
	e := &entry{
		msg:       msg,
		key:       key,
		seq:       seq,
		delivered: max(meta.NumDelivered, 1),
		subject:   msg.Subject(),
		id:        msg.Headers().Get(jetstream.MsgIDHeader),
		data:      msg.Data(),
		header:    userHeaders(msg.Headers()),
		stream:    meta.Stream,
		published: meta.Timestamp,
	}
	c.held[seq] = e
	q, active := c.queues[key]
	i := sort.Search(len(q), func(i int) bool { return q[i].seq > seq })
	c.queues[key] = slices.Insert(q, i, e)
	if !active {
		c.workers.Add(1)
		go c.work(key)
	}
}

// work processes the queue of one key until it is empty or a stop is requested.
func (c *Consumer) work(key string) {
	defer c.workers.Done()
	for {
		e := c.next(key)
		if e == nil {
			return
		}
		c.process(e)
	}
}

// next pops the head of a key's queue. When the queue is empty, or the
// consumer is stopping, it removes the queue (ending the worker) and returns nil.
func (c *Consumer) next(key string) *entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	q := c.queues[key]
	if len(q) == 0 || c.stopping() {
		delete(c.queues, key)
		return nil
	}
	e := q[0]
	q[0] = nil
	c.queues[key] = q[1:]
	return e
}

// process runs the handler for e until the message is settled or left for
// redelivery because the consumer is stopping.
func (c *Consumer) process(e *entry) {
	for {
		attempt := e.delivered + e.retries
		err := c.invoke(e, attempt)
		if errors.Is(err, errStopping) {
			return
		}
		result, reason := classify(err, attempt, c.spec.MaxDeliver)
		switch result {
		case outcomeAck:
			c.settle(e, func(m jetstream.Msg) error { return m.Ack() })
			return
		case outcomeTerm:
			c.logger.Error("bus message terminated", slog.String("subject", e.subject), slog.Uint64("stream_seq", e.seq),
				slog.Uint64("attempt", attempt), slog.String("reason", reason), slog.String("error", err.Error()))
			c.settle(e, func(m jetstream.Msg) error { return m.TermWithReason(reason) })
			return
		case outcomeRetry:
			delay := backoffDelay(c.spec.Backoff, attempt)
			c.logger.Warn("bus handler failed; retrying", slog.String("subject", e.subject), slog.Uint64("stream_seq", e.seq),
				slog.Uint64("attempt", attempt), slog.Duration("delay", delay), slog.String("error", err.Error()))
			if c.stopping() {
				return // left held; finish negatively acknowledges it
			}
			if c.spec.Ordering == Unordered {
				c.settle(e, func(m jetstream.Msg) error { return m.NakWithDelay(delay) })
				return
			}
			if !c.sleep(delay) {
				return
			}
			e.retries++
		}
	}
}

var errStopping = errors.New("consumer stopping")

// invoke runs the handler once, bounded by the concurrency limit, and turns
// a panic into an error.
func (c *Consumer) invoke(e *entry, attempt uint64) (err error) {
	select {
	case c.sem <- struct{}{}:
	case <-c.stopCh:
		return errStopping
	}
	defer func() { <-c.sem }()
	defer func() {
		if r := recover(); r != nil {
			c.logger.Error("bus handler panicked", slog.String("subject", e.subject), slog.Uint64("stream_seq", e.seq),
				slog.String("panic", fmt.Sprint(r)), slog.String("stack", string(debug.Stack())))
			err = errs.New(errs.CodeInternal, "handler panicked")
		}
	}()
	return c.handle(c.runCtx, Delivery{
		Subject:      e.subject,
		ID:           e.id,
		Data:         bytes.Clone(e.data),
		Header:       maps.Clone(e.header),
		Stream:       e.stream,
		StreamSeq:    e.seq,
		NumDelivered: attempt,
		Published:    e.published,
	})
}

// settle removes e from the held set and sends its acknowledgement. A lost
// acknowledgement only causes a redelivery.
func (c *Consumer) settle(e *entry, ack func(jetstream.Msg) error) {
	c.mu.Lock()
	msg := e.msg
	delete(c.held, e.seq)
	c.mu.Unlock()
	if err := ack(msg); err != nil && !errors.Is(err, jetstream.ErrMsgAlreadyAckd) {
		c.logger.Warn("bus acknowledgement failed; the message will be redelivered",
			slog.Uint64("stream_seq", e.seq), slog.String("error", err.Error()))
	}
}

// keepalive extends the acknowledgement deadline of every held message.
func (c *Consumer) keepalive() {
	defer close(c.keepaliveDone)
	t := time.NewTicker(c.spec.AckWait / 3)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			c.mu.Lock()
			msgs := make([]jetstream.Msg, 0, len(c.held))
			for _, e := range c.held {
				msgs = append(msgs, e.msg)
			}
			c.mu.Unlock()
			for _, m := range msgs {
				if err := m.InProgress(); err != nil && !errors.Is(err, jetstream.ErrMsgAlreadyAckd) {
					c.logger.Debug("bus keepalive failed", slog.String("error", err.Error()))
				}
			}
		case <-c.keepaliveStop:
			return
		}
	}
}

// finish completes a stop: it waits for running handlers, stops the
// keepalive, negatively acknowledges unstarted messages in stream order and
// deregisters the consumer.
func (c *Consumer) finish() {
	c.requestStop()
	c.workers.Wait()
	close(c.keepaliveStop)
	<-c.keepaliveDone

	c.mu.Lock()
	rest := slices.SortedFunc(maps.Values(c.held), func(a, b *entry) int {
		switch {
		case a.seq < b.seq:
			return -1
		case a.seq > b.seq:
			return 1
		default:
			return 0
		}
	})
	c.held = map[uint64]*entry{}
	c.mu.Unlock()
	for _, e := range rest {
		if err := e.msg.Nak(); err != nil && !errors.Is(err, jetstream.ErrMsgAlreadyAckd) {
			c.logger.Debug("bus nak on stop failed", slog.String("error", err.Error()))
		}
	}
	c.cancelRun()
	if c.unwatch != nil {
		c.unwatch()
	}
	c.bus.unregister(c)
	c.logger.Info("bus consumer stopped", slog.Int("returned_unstarted", len(rest)))
	close(c.done)
}
