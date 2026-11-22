package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/spool"
	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// SpoolConfig configures the spool: directory, capacity (by default sized for
// 24 hours of bus outage at the expected rate, see DefaultSpoolMaxBytes),
// segment size and shipping intervals.
type SpoolConfig = spool.Config

// Publisher publishes to the bus and waits for the acknowledgement;
// *bus.Bus implements it.
type Publisher = spool.Publisher

// DefaultSpoolMaxBytes is the default capacity: rate (events per second) ×
// avgBytes × 86 400 s × 2 (headroom).
func DefaultSpoolMaxBytes(rate, avgBytes int64) int64 { return spool.DefaultMaxBytes(rate, avgBytes) }

// Spool is the durable journal for audit events outside a database
// transaction (ADR-0008). Events are on disk (fsync) before Emit returns and
// are shipped to the AUDIT stream in order, with the event ID as the message
// ID; a full or unwritable spool refuses events (strict mode, always on).
type Spool struct {
	j *spool.Journal
}

// OpenSpool opens (and recovers) the spool in cfg.Dir. Call Run to ship.
func OpenSpool(cfg SpoolConfig, pub Publisher, logger *slog.Logger) (*Spool, error) {
	j, err := spool.Open(cfg, pub, logger)
	if err != nil {
		return nil, err //nolint:wrapcheck // already an *errs.Error
	}
	return &Spool{j: j}, nil
}

// Run ships spooled events until ctx is done.
func (s *Spool) Run(ctx context.Context) error {
	return s.j.Run(ctx) //nolint:wrapcheck // internal package of this module
}

// ShipOnce ships every pending event once and returns how many it shipped.
func (s *Spool) ShipOnce(ctx context.Context) (int, error) {
	return s.j.ShipOnce(ctx) //nolint:wrapcheck // internal package of this module
}

// Pending reports whether events await shipping.
func (s *Spool) Pending() bool { return s.j.Pending() }

// Used returns the spool's size in bytes.
func (s *Spool) Used() int64 { return s.j.Used() }

// Close closes the spool after Run has returned.
func (s *Spool) Close() error {
	return s.j.Close() //nolint:wrapcheck // internal package of this module
}

// append spools a protected event.
func (s *Spool) append(ev *Event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "audit: encode event")
	}
	subject, err := auditSubject(ev)
	if err != nil {
		return err
	}
	return s.j.Append(spool.Entry{ID: ev.ID.UUID().String(), Subject: subject, Data: data}) //nolint:wrapcheck // already an *errs.Error
}

func auditSubject(ev *Event) (string, error) {
	if ev.TenantID.IsZero() {
		return bus.AuditPlatformSubject, nil
	}
	s, err := bus.AuditSubject(ev.TenantID)
	if err != nil {
		return "", errs.Wrap(errs.CodeInternal, err, "audit: subject")
	}
	return s, nil
}
