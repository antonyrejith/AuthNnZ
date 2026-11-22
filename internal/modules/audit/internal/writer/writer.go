// Package writer is the audit writer (ADR-0008): it consumes the AUDIT
// stream ordered per chain and appends each record to its chain in the
// audit store, gapless and hash-chained, then deletes the record's outbox
// row in the same transaction and acknowledges the message.
//
// Exactly one writer is active at a time across all processes: Run holds a
// PostgreSQL session advisory lock on a dedicated connection before it
// starts consuming (the bus allows one ordered consumer per durable), and
// every append transaction checks that this session still holds the lock
// (fencing), so a writer that lost its lock cannot append.
//
// Crash safety: the append, the dedupe record and the outbox delete commit
// together; the message is acknowledged after the commit. A crash before
// the commit redelivers the message; a crash after it redelivers a message
// whose event ID is already stored, which is skipped.
package writer

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/outbox"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// LockID is the advisory lock that elects the active writer ("portunaw").
const LockID int64 = 0x706f7274756e6177

// DefaultDurable is the writer's durable consumer name.
const DefaultDurable = "audit-writer"

// Config configures a Writer.
type Config struct {
	Bus *bus.Bus
	DB  *db.DB
	// LockURL connects as portun_platform for the session holding the
	// writer lock (a dedicated connection, not from the pool).
	LockURL     secret.String
	Durable     string        // default DefaultDurable
	Concurrency int           // chains handled in parallel (default 8)
	AckWait     time.Duration // default 30s
	Backoff     []time.Duration
	// LockRetry is the pause between attempts to take the lock and the
	// interval at which the lock session is checked (default 2s).
	LockRetry time.Duration
	Logger    *slog.Logger
	Now       func() time.Time
}

// Writer is the audit writer.
type Writer struct {
	cfg      Config
	appender *store.Appender
}

const (
	defaultLockRetry = 2 * time.Second
	stopTimeout      = 30 * time.Second
)

// New validates cfg.
func New(cfg Config) (*Writer, error) {
	if cfg.Bus == nil || cfg.DB == nil || cfg.LockURL.IsEmpty() {
		return nil, errs.New(errs.CodeInvalidRequest, "audit writer: bus, database and lock URL are required")
	}
	if cfg.Durable == "" {
		cfg.Durable = DefaultDurable
	}
	if cfg.LockRetry <= 0 {
		cfg.LockRetry = defaultLockRetry
	}
	if cfg.Logger == nil {
		return nil, errs.New(errs.CodeInvalidRequest, "audit writer: a logger is required")
	}
	return &Writer{cfg: cfg, appender: store.NewAppender(cfg.Now)}, nil
}

// Run takes the writer lock, consumes until ctx ends, and starts over when
// the lock session is lost. It returns nil when ctx ends.
func (w *Writer) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		err := w.runOnce(ctx)
		if err != nil && ctx.Err() == nil {
			w.cfg.Logger.ErrorContext(ctx, "audit writer stopped; retrying", slog.String("error", err.Error()))
		}
		if !sleep(ctx, w.cfg.LockRetry) {
			break
		}
	}
	return nil
}

// runOnce holds the lock for one session and consumes while it holds.
func (w *Writer) runOnce(ctx context.Context) error {
	lock, err := acquire(ctx, w.cfg.LockURL, w.cfg.LockRetry, w.cfg.Logger)
	if err != nil {
		return err
	}
	defer lock.release(ctx, w.cfg.Logger)
	w.cfg.Logger.InfoContext(ctx, "audit writer lock acquired", slog.Int64("lock_pid", int64(lock.pid)))
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	consumer, err := w.cfg.Bus.Consume(runCtx, bus.ConsumerSpec{
		Stream:         bus.StreamAudit,
		Durable:        w.cfg.Durable,
		FilterSubjects: []string{bus.AuditSubjects},
		Ordering:       bus.OrderedBySubject,
		Concurrency:    w.cfg.Concurrency,
		AckWait:        w.cfg.AckWait,
		Backoff:        w.cfg.Backoff,
	}, func(ctx context.Context, d bus.Delivery) error { return w.handle(ctx, d, lock.pid) })
	if err != nil {
		return err //nolint:wrapcheck // errs-coded by bus
	}
	defer consumer.Stop()
	err = lock.watch(runCtx, w.cfg.LockRetry)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// handle appends one delivery. Errors other than CodeInvalidRequest are
// retried by the bus; invalid records are kept in audit.rejected_messages
// and acknowledged (never dropped silently, never blocking the chain).
func (w *Writer) handle(ctx context.Context, d bus.Delivery, lockPID uint32) error {
	h, err := store.DecodeHeader(d.Data)
	if err == nil {
		err = checkSubject(d.Subject, h.TenantID)
	}
	if err != nil {
		return w.reject(ctx, d, err)
	}
	err = w.cfg.DB.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := fence(ctx, tx, lockPID); err != nil {
			return err
		}
		res, err := w.appender.Append(ctx, tx, d.Data)
		if err != nil {
			return err //nolint:wrapcheck // errs-coded by store
		}
		if id, perr := uuid.Parse(d.ID); perr == nil {
			// Messages from the outbox carry the outbox row ID; spooled
			// events have no outbox row.
			if err := outbox.Delete(ctx, tx, h.TenantID, id); err != nil {
				return errs.Wrap(errs.CodeInternal, err, "audit writer: delete the outbox row")
			}
		}
		if res.Duplicate {
			w.cfg.Logger.DebugContext(ctx, "audit event already stored", slog.String("chain", store.ChainName(res.Chain)),
				slog.Uint64("sequence", res.Sequence))
		}
		return nil
	})
	if err != nil {
		// A stored record is valid; any failure now is transient or a
		// server-side problem: retry, never terminate.
		if errs.Is(err, errs.CodeInvalidRequest) {
			return errs.Wrap(errs.CodeInternal, err, "audit writer: append failed")
		}
		return err //nolint:wrapcheck // errs-coded
	}
	return nil
}

func (w *Writer) reject(ctx context.Context, d bus.Delivery, cause error) error {
	_, reason, _ := errs.Public(cause)
	w.cfg.Logger.ErrorContext(ctx, "audit record rejected; kept in audit.rejected_messages",
		slog.String("subject", d.Subject), slog.Uint64("stream_seq", d.StreamSeq), slog.String("reason", reason))
	err := w.cfg.DB.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return store.Reject(ctx, tx, d.ID, d.Subject, d.StreamSeq, d.Data, reason)
	})
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "audit writer: keep the rejected record")
	}
	return nil
}

// checkSubject requires the record's tenant to match its subject.
func checkSubject(subject string, tenant ids.TenantID) error {
	if tenant.IsZero() {
		if subject != bus.AuditPlatformSubject {
			return errs.New(errs.CodeInvalidRequest, "platform record on a tenant subject")
		}
		return nil
	}
	want, err := bus.AuditSubject(tenant)
	if err != nil {
		return err //nolint:wrapcheck // errs-coded
	}
	if !strings.EqualFold(subject, want) {
		return errs.New(errs.CodeInvalidRequest, "record tenant does not match its subject")
	}
	return nil
}

// pg_locks shows a bigint advisory key as its high and low 32-bit halves.
const (
	lockKeyHigh = LockID >> 32
	lockKeyLow  = LockID & 0xffffffff
)

// errLockLost reports that the writer session no longer holds the lock.
var errLockLost = errors.New("audit writer lock is not held")

// fence fails unless the session lockPID holds the writer lock.
func fence(ctx context.Context, tx pgx.Tx, lockPID uint32) error {
	var held bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks
WHERE locktype = 'advisory' AND granted AND pid = $1 AND classid = $2 AND objid = $3 AND objsubid = 1)`,
		int64(lockPID), lockKeyHigh, lockKeyLow).Scan(&held)
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "audit writer: check the writer lock")
	}
	if !held {
		return errs.Wrap(errs.CodeUnavailable, errLockLost, "audit writer: lock lost")
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
