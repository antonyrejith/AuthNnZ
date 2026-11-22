// Package bus is the platform's event bus on NATS JetStream (ADR-0007).
//
// Three streams carry all messages: AUDIT (audit.<tenant>, audit.platform),
// EVENTS (evt.<module>.<type>) and CONFIG (config.<tenant>.<env>). Publish
// waits for the JetStream acknowledgement and deduplicates by message ID, so
// the outbox relay can publish a row with its row ID, retry freely and delete
// the row once Publish returns without error. Consume runs durable pull
// consumers with explicit acknowledgement: at-least-once delivery, optionally
// ordered per subject (see Ordering).
//
// With an empty Config.URL, Connect starts an embedded server (all-in-one
// binary, tests) and connects to it in process.
package bus

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// Config configures the bus connection. Load it with config.FromEnv, for
// example with the prefix "PORTUN_BUS_". Zero durations and counts take the
// documented defaults, except the max ages, where zero means no age limit.
type Config struct {
	// URL lists the NATS servers (comma-separated). Empty starts an embedded
	// server owned by the Bus.
	URL string `env:"URL"`
	// Name identifies the connection on the server.
	Name string `env:"NAME" default:"portun"`
	// Embedded configures the embedded server (used when URL is empty).
	Embedded EmbeddedConfig `envPrefix:"EMBEDDED_"`
	// Credentials authenticate to an external server: the contents of a NATS
	// .creds file (user JWT and seed) or a token. Optional.
	Credentials secret.String `env:"CREDS"`
	// TLSCAFile is a PEM file with the CAs trusted for tls:// servers
	// (empty: the system roots).
	TLSCAFile string `env:"TLS_CA_FILE"`
	// Replicas is the stream replication factor (1 for the embedded server).
	Replicas int `env:"REPLICAS" default:"1"`
	// ConnectTimeout bounds each connection attempt.
	ConnectTimeout time.Duration `env:"CONNECT_TIMEOUT" default:"5s"`
	// RequestTimeout bounds JetStream API calls and publishes whose context
	// has no deadline.
	RequestTimeout time.Duration `env:"REQUEST_TIMEOUT" default:"5s"`
	// ReconnectWait is the pause between reconnect attempts (reconnects never stop).
	ReconnectWait time.Duration `env:"RECONNECT_WAIT" default:"1s"`
	// DrainTimeout bounds draining the connection on Close.
	DrainTimeout time.Duration `env:"DRAIN_TIMEOUT" default:"30s"`
	// DuplicateWindow is how long the streams remember message IDs for
	// deduplication. It must cover the relay's retries.
	DuplicateWindow time.Duration `env:"DUPLICATE_WINDOW" default:"5m"`
	// AuditMaxAge expires AUDIT messages; zero (the default) never expires
	// them, so an audit record is never lost before the writer stores it.
	AuditMaxAge time.Duration `env:"AUDIT_MAX_AGE"`
	// EventsMaxAge expires EVENTS messages; zero means no age limit.
	EventsMaxAge time.Duration `env:"EVENTS_MAX_AGE" default:"168h"`
	// ConfigMaxAge expires CONFIG messages; zero means no age limit.
	ConfigMaxAge time.Duration `env:"CONFIG_MAX_AGE" default:"24h"`
}

const (
	defaultName            = "portun"
	defaultConnectTimeout  = 5 * time.Second
	defaultRequestTimeout  = 5 * time.Second
	defaultReconnectWait   = time.Second
	defaultDrainTimeout    = 30 * time.Second
	defaultDuplicateWindow = 5 * time.Minute
	credsMarker            = "-----BEGIN NATS USER JWT-----" //nolint:gosec // G101: a PEM-style marker used to detect .creds contents, not a credential
	reservedHeaderPrefix   = "nats-"
)

func (c Config) withDefaults() Config {
	if c.Name == "" {
		c.Name = defaultName
	}
	if c.Replicas == 0 {
		c.Replicas = 1
	}
	c.ConnectTimeout = orDuration(c.ConnectTimeout, defaultConnectTimeout)
	c.RequestTimeout = orDuration(c.RequestTimeout, defaultRequestTimeout)
	c.ReconnectWait = orDuration(c.ReconnectWait, defaultReconnectWait)
	c.DrainTimeout = orDuration(c.DrainTimeout, defaultDrainTimeout)
	c.DuplicateWindow = orDuration(c.DuplicateWindow, defaultDuplicateWindow)
	return c
}

func (c Config) validate() error {
	switch {
	case c.Replicas < 1 || c.Replicas > 5:
		return errs.New(errs.CodeInvalidRequest, "bus: replicas must be between 1 and 5")
	case c.URL == "" && c.Replicas != 1:
		return errs.New(errs.CodeInvalidRequest, "bus: the embedded server supports one replica only")
	case c.AuditMaxAge < 0 || c.EventsMaxAge < 0 || c.ConfigMaxAge < 0:
		return errs.New(errs.CodeInvalidRequest, "bus: max ages must not be negative")
	case c.ConnectTimeout < 0 || c.RequestTimeout < 0 || c.ReconnectWait < 0 || c.DrainTimeout < 0 || c.DuplicateWindow < 0:
		return errs.New(errs.CodeInvalidRequest, "bus: durations must not be negative")
	}
	return nil
}

func orDuration(d, def time.Duration) time.Duration {
	if d == 0 {
		return def
	}
	return d
}

// Bus is a connection to the event bus. It is safe for concurrent use.
type Bus struct {
	cfg      Config
	logger   *slog.Logger
	nc       *nats.Conn
	js       jetstream.JetStream
	embedded *EmbeddedServer // owned; nil when connected to an external server
	closedCh chan struct{}   // closed by the connection's closed handler

	mu        sync.Mutex
	closed    bool
	consumers map[*Consumer]struct{}
}

// Connect connects to the servers in cfg.URL or, when it is empty, starts an
// embedded server, connects to it in process and shuts it down on Close.
// The connection reconnects indefinitely; publishes fail fast while it is
// disconnected instead of being buffered.
func Connect(ctx context.Context, cfg Config, logger *slog.Logger) (*Bus, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	logger = orDiscard(logger).With(slog.String("component", "bus"))
	b := &Bus{cfg: cfg, logger: logger, closedCh: make(chan struct{}), consumers: map[*Consumer]struct{}{}}

	opts := b.options()
	if cfg.URL == "" {
		emb, err := StartEmbedded(ctx, cfg.Embedded, logger)
		if err != nil {
			return nil, err
		}
		b.embedded = emb
		opts = append(opts, nats.InProcessServer(emb.ns), nats.Token(emb.token.Reveal()))
	} else {
		auth, err := authOptions(cfg)
		if err != nil {
			return nil, err
		}
		opts = append(opts, auth...)
	}
	if err := ctx.Err(); err != nil {
		b.shutdownEmbedded()
		return nil, errs.Wrap(errs.CodeOf(err), err, "bus: connect canceled")
	}
	nc, err := nats.Connect(cfg.URL, opts...)
	if err != nil {
		b.shutdownEmbedded()
		return nil, errs.Wrap(errs.CodeUnavailable, err, "bus: connect")
	}
	js, err := jetstream.New(nc, jetstream.WithDefaultTimeout(cfg.RequestTimeout))
	if err != nil {
		nc.Close()
		b.shutdownEmbedded()
		return nil, errs.Wrap(errs.CodeInternal, err, "bus: create JetStream context")
	}
	b.nc, b.js = nc, js
	logger.Info("bus connected", slog.String("server_name", nc.ConnectedServerName()), slog.Bool("embedded", b.embedded != nil))
	return b, nil
}

func (b *Bus) options() []nats.Option {
	return []nats.Option{
		nats.Name(b.cfg.Name),
		nats.Timeout(b.cfg.ConnectTimeout),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(b.cfg.ReconnectWait),
		nats.ReconnectBufSize(-1),
		nats.DrainTimeout(b.cfg.DrainTimeout),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				b.logger.Warn("bus disconnected", slog.String("error", err.Error()))
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			b.logger.Info("bus reconnected", slog.String("server_name", nc.ConnectedServerName()))
		}),
		nats.ClosedHandler(func(*nats.Conn) { close(b.closedCh) }),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			b.logger.Error("bus asynchronous error", slog.String("error", err.Error()))
		}),
	}
}

// authOptions turns the configured credentials into connection options.
func authOptions(cfg Config) ([]nats.Option, error) {
	var opts []nats.Option
	if cfg.TLSCAFile != "" {
		opts = append(opts, nats.RootCAs(cfg.TLSCAFile))
	}
	if cfg.Credentials.IsEmpty() {
		return opts, nil
	}
	raw := cfg.Credentials.Reveal()
	if !strings.Contains(raw, credsMarker) {
		return append(opts, nats.Token(raw)), nil
	}
	jwt, err := nkeys.ParseDecoratedJWT([]byte(raw))
	if err != nil {
		return nil, errs.Wrap(errs.CodeInvalidRequest, err, "bus: invalid credentials JWT")
	}
	kp, err := nkeys.ParseDecoratedUserNKey([]byte(raw))
	if err != nil {
		return nil, errs.Wrap(errs.CodeInvalidRequest, err, "bus: invalid credentials seed")
	}
	seed, err := kp.Seed()
	if err != nil {
		return nil, errs.Wrap(errs.CodeInvalidRequest, err, "bus: invalid credentials seed")
	}
	return append(opts, nats.UserJWTAndSeed(jwt, string(seed))), nil
}

// Close stops every consumer started on this Bus (waiting for in-flight
// handlers), drains the connection so pending acknowledgements reach the
// server, and shuts down an owned embedded server. If ctx ends first, handler
// contexts are canceled, the connection is closed without waiting and the
// context error is returned. Close is idempotent.
func (b *Bus) Close(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	consumers := make([]*Consumer, 0, len(b.consumers))
	for c := range b.consumers {
		consumers = append(consumers, c)
	}
	b.mu.Unlock()

	var problems []error
	for _, c := range consumers {
		if err := c.stop(ctx); err != nil {
			problems = append(problems, err)
		}
	}
	if err := b.nc.Drain(); err != nil && !errors.Is(err, nats.ErrConnectionClosed) {
		problems = append(problems, err)
		b.nc.Close()
	}
	select {
	case <-b.closedCh:
	case <-ctx.Done():
		b.nc.Close()
		problems = append(problems, ctx.Err())
	}
	b.shutdownEmbedded()
	b.logger.Info("bus closed")
	if len(problems) > 0 {
		err := errors.Join(problems...)
		return errs.Wrap(errs.CodeOf(problems[0]), err, "bus: close")
	}
	return nil
}

func (b *Bus) shutdownEmbedded() {
	if b.embedded != nil {
		b.embedded.Shutdown()
	}
}

// Ready reports whether the bus is connected and JetStream answers, for
// readiness checks. Failures carry errs.CodeUnavailable.
func (b *Bus) Ready(ctx context.Context) error {
	if status := b.nc.Status(); status != nats.CONNECTED {
		return errs.New(errs.CodeUnavailable, "bus: not connected ("+status.String()+")")
	}
	ctx, cancel := b.withTimeout(ctx)
	defer cancel()
	if _, err := b.js.AccountInfo(ctx); err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "bus: JetStream unavailable")
	}
	return nil
}

func (b *Bus) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, b.cfg.RequestTimeout)
}

// streamConfigs returns the definitions of the three streams. All use file
// storage and limits retention: acknowledging a message never deletes it, so
// terminated messages remain inspectable until they age out. DiscardNew makes
// a full stream reject publishes (the outbox keeps the rows) instead of
// dropping unconsumed messages, should size limits ever be added.
func (b *Bus) streamConfigs() []jetstream.StreamConfig {
	def := func(name, subjects, desc string, maxAge time.Duration) jetstream.StreamConfig {
		return jetstream.StreamConfig{
			Name:        name,
			Description: desc,
			Subjects:    []string{subjects},
			Retention:   jetstream.LimitsPolicy,
			Storage:     jetstream.FileStorage,
			Discard:     jetstream.DiscardNew,
			MaxAge:      maxAge,
			MaxMsgs:     -1,
			MaxBytes:    -1,
			Replicas:    b.cfg.Replicas,
			Duplicates:  b.cfg.DuplicateWindow,
		}
	}
	return []jetstream.StreamConfig{
		def(StreamAudit, AuditSubjects, "Portun audit records, one subject per tenant chain", b.cfg.AuditMaxAge),
		def(StreamEvents, EventSubjects, "Portun domain events", b.cfg.EventsMaxAge),
		def(StreamConfig, ConfigSubjects, "Portun configuration change notifications", b.cfg.ConfigMaxAge),
	}
}

// EnsureStreams creates the AUDIT, EVENTS and CONFIG streams or updates them
// to the configured settings. It is idempotent.
func (b *Bus) EnsureStreams(ctx context.Context) error {
	for _, sc := range b.streamConfigs() {
		cctx, cancel := b.withTimeout(ctx)
		_, err := b.js.CreateOrUpdateStream(cctx, sc)
		cancel()
		if err != nil {
			return errs.Wrap(apiCode(err), err, "bus: ensure stream "+sc.Name)
		}
	}
	return nil
}

// Message is a message to publish.
type Message struct {
	// Subject is built with AuditSubject, EventSubject or ConfigSubject.
	Subject string
	// ID deduplicates the message within the duplicate window (the outbox
	// row ID for the relay). Required.
	ID string
	// Data is the payload.
	Data []byte
	// Header holds optional headers (for example trace context). Keys
	// starting with "Nats-" are reserved and rejected.
	Header map[string]string
}

// PubAck is the server's acknowledgement of a stored message.
type PubAck struct {
	Stream    string
	Sequence  uint64
	Duplicate bool // the ID was already stored within the window; nothing new was stored
}

// Publish stores msg in its stream and returns once JetStream acknowledges
// it. A message whose ID was already stored within the duplicate window is
// acknowledged with Duplicate set. On error the outcome is unknown and the
// caller retries with the same ID. Errors carry errs codes:
// CodeInvalidRequest (bad message), CodeDeadlineExceeded (no ack in time),
// CodeUnavailable (bus unreachable), CodePayloadTooLarge, CodeInternal.
func (b *Bus) Publish(ctx context.Context, msg Message) (PubAck, error) {
	stream, err := msg.validate()
	if err != nil {
		return PubAck{}, err
	}
	m := nats.NewMsg(msg.Subject)
	m.Data = msg.Data
	for k, v := range msg.Header {
		m.Header.Set(k, v)
	}
	ctx, cancel := b.withTimeout(ctx)
	defer cancel()
	ack, err := b.js.PublishMsg(ctx, m, jetstream.WithMsgID(msg.ID), jetstream.WithExpectStream(stream))
	if err != nil {
		return PubAck{}, publishError(err)
	}
	return PubAck{Stream: ack.Stream, Sequence: ack.Sequence, Duplicate: ack.Duplicate}, nil
}

func (m Message) validate() (string, error) {
	stream, err := streamForSubject(m.Subject)
	if err != nil {
		return "", err
	}
	if m.ID == "" {
		return "", errs.New(errs.CodeInvalidRequest, "publish: message ID is required for deduplication")
	}
	if strings.ContainsAny(m.ID, "\r\n") {
		return "", errs.New(errs.CodeInvalidRequest, "publish: message ID must not contain line breaks")
	}
	for k, v := range m.Header {
		if err := validateHeader(k, v); err != nil {
			return "", err
		}
	}
	return stream, nil
}

func validateHeader(key, value string) error {
	if key == "" || strings.ContainsAny(key, ": \t\r\n") {
		return errs.New(errs.CodeInvalidRequest, "publish: invalid header name")
	}
	if strings.HasPrefix(strings.ToLower(key), reservedHeaderPrefix) {
		return errs.New(errs.CodeInvalidRequest, "publish: header names starting with Nats- are reserved")
	}
	if strings.ContainsAny(value, "\r\n") {
		return errs.New(errs.CodeInvalidRequest, "publish: header values must not contain line breaks")
	}
	return nil
}

// publishError maps a publish failure to an errs code.
func publishError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, nats.ErrTimeout):
		return errs.Wrap(errs.CodeDeadlineExceeded, err, "publish: not acknowledged in time")
	case errors.Is(err, nats.ErrMaxPayload), errors.Is(err, jetstream.ErrStreamMessageExceedsMaximum):
		return errs.Wrap(errs.CodePayloadTooLarge, err, "publish: message too large")
	case errors.Is(err, context.Canceled),
		errors.Is(err, jetstream.ErrNoStreamResponse),
		errors.Is(err, nats.ErrNoResponders),
		errors.Is(err, nats.ErrConnectionClosed),
		errors.Is(err, nats.ErrConnectionDraining),
		errors.Is(err, nats.ErrConnectionReconnecting),
		errors.Is(err, nats.ErrReconnectBufExceeded),
		errors.Is(err, nats.ErrDisconnected):
		return errs.Wrap(errs.CodeUnavailable, err, "publish: bus unavailable")
	default:
		return errs.Wrap(apiCode(err), err, "publish: failed")
	}
}

// apiCode maps JetStream API errors: 503 responses are retryable
// (unavailable); anything else is an internal (configuration) error.
func apiCode(err error) errs.Code {
	var apiErr *jetstream.APIError
	if errors.As(err, &apiErr) && apiErr.Code == 503 {
		return errs.CodeUnavailable
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout) {
		return errs.CodeDeadlineExceeded
	}
	if errors.Is(err, nats.ErrConnectionClosed) || errors.Is(err, nats.ErrNoResponders) || errors.Is(err, context.Canceled) {
		return errs.CodeUnavailable
	}
	return errs.CodeInternal
}

// userHeaders returns the first value of each header that is not a
// reserved Nats- header.
func userHeaders(h nats.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) == 0 || strings.HasPrefix(strings.ToLower(k), reservedHeaderPrefix) {
			continue
		}
		out[k] = v[0]
	}
	return out
}

func (b *Bus) register(c *Consumer) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errs.New(errs.CodeUnavailable, "bus: closed")
	}
	for other := range b.consumers {
		if other.spec.Stream == c.spec.Stream && other.spec.Durable == c.spec.Durable &&
			(other.spec.Ordering == OrderedBySubject || c.spec.Ordering == OrderedBySubject) {
			return errs.New(errs.CodeConflict, "bus: an ordered consumer needs exclusive use of its durable "+c.spec.Durable)
		}
	}
	b.consumers[c] = struct{}{}
	return nil
}

func (b *Bus) unregister(c *Consumer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.consumers, c)
}
