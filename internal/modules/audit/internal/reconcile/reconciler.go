package reconcile

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// Config configures the production Reconciler.
type Config struct {
	// ReplicationURL connects to the application database as a role with
	// the REPLICATION attribute (replication=database is added).
	ReplicationURL secret.String
	// Slot is the persistent logical replication slot (created if missing).
	Slot string
	// Publication is the publication over the module schemas, maintained
	// by the migration owner with EnsurePublication.
	Publication string
	Exclude     Exclusions
	// OnUnaudited is called for every finding (for example to write a
	// platform audit event). Optional.
	OnUnaudited   func(ctx context.Context, u Unaudited)
	MeterProvider metric.MeterProvider
	Logger        *slog.Logger
	RetryDelay    time.Duration // after a connection failure (default 5s)
}

// Reconciler follows the database's change stream and raises an
// unaudited-change alert (metric portun.audit.unaudited_changes, error log,
// callback) for every row change that no audit record declares.
type Reconciler struct {
	cfg     Config
	counter metric.Int64Counter
}

// New validates cfg.
func New(cfg Config) (*Reconciler, error) {
	if cfg.ReplicationURL.IsEmpty() || cfg.Slot == "" || cfg.Publication == "" {
		return nil, errs.New(errs.CodeInvalidRequest, "reconcile: replication URL, slot and publication are required")
	}
	if cfg.Logger == nil {
		return nil, errs.New(errs.CodeInvalidRequest, "reconcile: a logger is required")
	}
	if cfg.MeterProvider == nil {
		cfg.MeterProvider = metricnoop.NewMeterProvider()
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = 5 * time.Second
	}
	counter, err := cfg.MeterProvider.Meter("github.com/antonyrejith/portun/internal/modules/audit/reconcile").
		Int64Counter("portun.audit.unaudited_changes", metric.WithDescription("Row changes without an audit record"))
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "reconcile: create the metric")
	}
	return &Reconciler{cfg: cfg, counter: counter}, nil
}

// Run streams until ctx ends, reconnecting after failures.
func (r *Reconciler) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		if err := r.runOnce(ctx); err != nil && ctx.Err() == nil {
			r.cfg.Logger.ErrorContext(ctx, "audit reconciler stopped; retrying", slog.String("error", err.Error()))
		}
		t := time.NewTimer(r.cfg.RetryDelay)
		select {
		case <-ctx.Done():
			t.Stop()
		case <-t.C:
		}
	}
	return nil
}

func (r *Reconciler) runOnce(ctx context.Context) error {
	rconn, err := connectReplication(ctx, r.cfg.ReplicationURL)
	if err != nil {
		return err
	}
	defer func() { _ = rconn.Close(context.WithoutCancel(ctx)) }()
	kconn, err := pgx.Connect(ctx, r.cfg.ReplicationURL.Reveal())
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "reconcile: connect")
	}
	defer func() { _ = kconn.Close(context.WithoutCancel(ctx)) }()
	var exists bool
	if err := kconn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_replication_slots WHERE slot_name = $1)`, r.cfg.Slot).Scan(&exists); err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "reconcile: look up the slot")
	}
	if !exists {
		if _, err := pglogrepl.CreateReplicationSlot(ctx, rconn, r.cfg.Slot, "pgoutput",
			pglogrepl.CreateReplicationSlotOptions{Mode: pglogrepl.LogicalReplication, SnapshotAction: "NOEXPORT_SNAPSHOT"}); err != nil {
			return errs.Wrap(errs.CodeUnavailable, err, "reconcile: create the slot")
		}
	}
	keys := &keyCache{conn: kconn, cache: map[string][]string{}}
	s := &session{conn: rconn, dec: newDecoder(r.cfg.Exclude, keys.resolve(ctx))}
	if err := s.start(ctx, r.cfg.Slot, r.cfg.Publication, 0); err != nil {
		return err
	}
	for {
		ev, err := s.next(ctx)
		if err != nil {
			return err
		}
		for _, u := range ev.unaudited {
			r.alert(ctx, u)
		}
		if ev.commit {
			// The transaction is fully reconciled: let the slot move on.
			if err := s.status(ctx); err != nil {
				return err
			}
		}
	}
}

func (r *Reconciler) alert(ctx context.Context, u Unaudited) {
	r.counter.Add(ctx, 1, metric.WithAttributes(attribute.String("table", u.Table), attribute.String("op", u.Op)))
	r.cfg.Logger.ErrorContext(ctx, "unaudited database change",
		slog.Uint64("xid", uint64(u.XID)), slog.String("table", u.Table), slog.String("op", u.Op), slog.String("reason", u.Reason))
	if r.cfg.OnUnaudited != nil {
		r.cfg.OnUnaudited(ctx, u)
	}
}
