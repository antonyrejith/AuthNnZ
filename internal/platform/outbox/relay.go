package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// Publisher publishes one message and waits for the acknowledgement.
// *bus.Bus implements it.
type Publisher interface {
	Publish(ctx context.Context, msg bus.Message) (bus.PubAck, error)
}

// RelayConfig configures the relay. Zero values take the defaults.
type RelayConfig struct {
	// BatchSize is the number of rows one relay transaction claims (default 100).
	BatchSize int `env:"BATCH_SIZE" default:"100"`
	// PollInterval is the pause after a batch that was not full (default 250ms).
	PollInterval time.Duration `env:"POLL_INTERVAL" default:"250ms"`
	// ErrorBackoff is the pause after a failed batch (default 1s).
	ErrorBackoff time.Duration `env:"ERROR_BACKOFF" default:"1s"`
	// RepublishAfter is how long a published AUDIT row may stay in the
	// outbox (the audit writer has not stored it yet) before the relay
	// publishes it again (default 30s). Keep it below the bus's duplicate
	// window only if duplicate storage must be avoided; the writer ignores
	// duplicate event IDs either way.
	RepublishAfter time.Duration `env:"REPUBLISH_AFTER" default:"30s"`
}

const (
	defaultBatchSize      = 100
	defaultPollInterval   = 250 * time.Millisecond
	defaultErrorBackoff   = time.Second
	defaultRepublishAfter = 30 * time.Second
)

func (c RelayConfig) withDefaults() RelayConfig {
	if c.BatchSize <= 0 {
		c.BatchSize = defaultBatchSize
	}
	if c.PollInterval <= 0 {
		c.PollInterval = defaultPollInterval
	}
	if c.ErrorBackoff <= 0 {
		c.ErrorBackoff = defaultErrorBackoff
	}
	if c.RepublishAfter <= 0 {
		c.RepublishAfter = defaultRepublishAfter
	}
	return c
}

// Relay publishes committed outbox rows to the bus.
type Relay struct {
	db     *db.DB
	pub    Publisher
	cfg    RelayConfig
	logger *slog.Logger
}

// NewRelay returns a relay that reads d's outbox as the platform role and
// publishes to p (normally a *bus.Bus).
func NewRelay(d *db.DB, p Publisher, cfg RelayConfig, logger *slog.Logger) *Relay {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Relay{db: d, pub: p, cfg: cfg.withDefaults(), logger: logger.With(slog.String("component", "outbox_relay"))}
}

// Run relays until ctx is done, then returns nil. Failed batches are logged
// and retried after ErrorBackoff; rows stay in the outbox until published.
// Several relays may run at once: each claims rows with FOR UPDATE SKIP
// LOCKED.
func (r *Relay) Run(ctx context.Context) error {
	for {
		n, err := r.RunOnce(ctx)
		wait := r.cfg.PollInterval
		if ctx.Err() != nil {
			return nil //nolint:nilerr // stopping on cancellation is the normal end of Run
		}
		switch {
		case err != nil:
			r.logger.WarnContext(ctx, "outbox relay batch failed", slog.String("error", err.Error()))
			wait = r.cfg.ErrorBackoff
		case n >= r.cfg.BatchSize:
			wait = 0
		}
		if wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil
			case <-t.C:
			}
		}
	}
}

// claimSQL selects due rows in creation order: never published, or AUDIT
// rows published more than RepublishAfter ago and not yet removed by the
// audit writer. Rows locked by another relay are skipped.
const claimSQL = `
SELECT tenant_id, id, stream, subject, payload
FROM outbox.messages
WHERE published_at IS NULL
   OR (stream = 'AUDIT' AND published_at < now() - make_interval(secs => $1))
ORDER BY created_at, id
LIMIT $2
FOR UPDATE SKIP LOCKED`

const markPublishedSQL = `UPDATE outbox.messages SET published_at = now() WHERE tenant_id = $1 AND id = $2`

type row struct {
	tenant  uuid.UUID
	id      uuid.UUID
	stream  string
	subject string
	payload []byte
}

// RunOnce claims one batch, publishes it in order and settles each
// acknowledged row (EVENTS and CONFIG rows deleted, AUDIT rows marked
// published). It stops at the first publish failure, keeping the rows
// settled so far, and returns the number of rows published.
func (r *Relay) RunOnce(ctx context.Context) (int, error) {
	published := 0
	var pubErr error
	err := r.db.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := claim(ctx, tx, r.cfg)
		if err != nil {
			return err
		}
		for _, m := range rows {
			if _, err := r.pub.Publish(ctx, bus.Message{Subject: m.subject, ID: m.id.String(), Data: m.payload}); err != nil {
				if c := errs.CodeOf(err); c == errs.CodeInvalidRequest || c == errs.CodePayloadTooLarge {
					// The bus will never accept this row; keep it (it is
					// never dropped) and do not let it block the rows after it.
					r.logger.ErrorContext(ctx, "outbox message rejected by the bus; it stays in the outbox",
						slog.String("message_id", m.id.String()), slog.String("stream", m.stream), slog.String("error", err.Error()))
					continue
				}
				pubErr = err
				return nil // commit what was settled
			}
			if err := settle(ctx, tx, m); err != nil {
				return err
			}
			published++
		}
		return nil
	})
	if err != nil {
		return 0, err //nolint:wrapcheck // db.MapError already produced an *errs.Error
	}
	if pubErr != nil {
		return published, errs.Wrap(errs.CodeOf(pubErr), pubErr, "outbox relay: publish")
	}
	return published, nil
}

func claim(ctx context.Context, tx pgx.Tx, cfg RelayConfig) ([]row, error) {
	rs, err := tx.Query(ctx, claimSQL, cfg.RepublishAfter.Seconds(), cfg.BatchSize)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rs.Close()
	var out []row
	for rs.Next() {
		var m row
		if err := rs.Scan(&m.tenant, &m.id, &m.stream, &m.subject, &m.payload); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, m)
	}
	if err := rs.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

func settle(ctx context.Context, tx pgx.Tx, m row) error {
	q := deleteSQL
	if m.stream == bus.StreamAudit {
		q = markPublishedSQL
	}
	if _, err := tx.Exec(ctx, q, m.tenant, m.id); err != nil {
		return mapErr(err)
	}
	return nil
}
