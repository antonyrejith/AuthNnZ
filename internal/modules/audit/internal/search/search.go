// Package search finds, shows and exports audit records (GOV-01, GOV-16).
// Tenants search their own chain inside db.InTenantTx, so row-level
// security limits every query to that chain; platform operators search any
// chain inside db.InPlatformTx. Every search, view and export is itself
// audited: the AccessRecorder is called before any data is read, and the
// call fails closed when the record cannot be written.
package search

import (
	"context"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/verify"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/page"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// Access kinds passed to the AccessRecorder.
const (
	AccessSearch = "search"
	AccessView   = "view"
	AccessExport = "export"
)

// AccessRecorder records an access to audit records (audit of audit,
// GOV-16), through the audit spool. It must return only once the record is
// durable; an error makes the access fail.
type AccessRecorder interface {
	RecordAccess(ctx context.Context, kind string, query any) error
}

// Query filters records. Empty fields do not filter.
type Query struct {
	ActorID    string    `json:"actor_id,omitempty"`
	TargetType string    `json:"target_type,omitempty"`
	TargetID   string    `json:"target_id,omitempty"`
	Type       string    `json:"type,omitempty"`
	Category   string    `json:"category,omitempty"`
	Channel    string    `json:"channel,omitempty"`
	Outcome    string    `json:"outcome,omitempty"`
	From       time.Time `json:"from,omitzero"` // occurred at or after
	To         time.Time `json:"to,omitzero"`   // occurred before
	RequestID  string    `json:"request_id,omitempty"`
	TraceID    string    `json:"trace_id,omitempty"`
}

// MaxFilterLength bounds each text filter.
const MaxFilterLength = 256

// Validate checks the filter values.
func (q Query) Validate() error {
	var fields []errs.FieldError
	for name, v := range map[string]string{
		"actor_id": q.ActorID, "target_type": q.TargetType, "target_id": q.TargetID, "type": q.Type,
		"category": q.Category, "channel": q.Channel, "outcome": q.Outcome, "request_id": q.RequestID, "trace_id": q.TraceID,
	} {
		if len(v) > MaxFilterLength {
			fields = append(fields, errs.FieldError{Pointer: "/" + name, Code: "too_long", Detail: "at most " + strconv.Itoa(MaxFilterLength) + " bytes"})
		}
	}
	if !q.From.IsZero() && !q.To.IsZero() && !q.To.After(q.From) {
		fields = append(fields, errs.FieldError{Pointer: "/to", Code: "out_of_range", Detail: "to must be after from"})
	}
	if len(fields) > 0 {
		return errs.New(errs.CodeValidationFailed, "invalid audit search").WithFields(fields...)
	}
	return nil
}

// binding identifies the query for page tokens.
func (q Query) binding(chain uuid.UUID) string {
	return strings.Join([]string{
		"audit.search", chain.String(), q.ActorID, q.TargetType, q.TargetID, q.Type, q.Category,
		q.Channel, q.Outcome, q.From.UTC().Format(time.RFC3339Nano), q.To.UTC().Format(time.RFC3339Nano), q.RequestID, q.TraceID,
	}, "|")
}

// Record is one search result: the stored record (with sequence and
// integrity) and its position.
type Record struct {
	Sequence   uint64    `json:"sequence"`
	RecordedAt time.Time `json:"recorded_at"`
	Event      []byte    `json:"-"`
}

// Result is a page of records, newest first.
type Result struct {
	Records       []Record
	NextPageToken string
}

// Service searches audit records.
type Service struct {
	DB       *db.DB
	Recorder AccessRecorder
}

// accessQuery is what the access record says about a search.
type accessQuery struct {
	Chain string `json:"chain"`
	Query Query  `json:"query"`
	Page  string `json:"page,omitempty"`
}

// Search returns a page of the context tenant's records matching q.
func (s *Service) Search(ctx context.Context, q Query, p page.Request) (Result, error) {
	t, err := scope.TenantFrom(ctx)
	if err != nil {
		return Result{}, err //nolint:wrapcheck // errs-coded
	}
	var res Result
	err = s.DB.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		res, err = s.search(ctx, tx, store.ChainOf(t.TenantID), q, p)
		return err
	}, db.ReadOnly())
	return res, err //nolint:wrapcheck // errs-coded
}

// SearchChain returns a page of any chain's records (platform operators).
func (s *Service) SearchChain(ctx context.Context, chain uuid.UUID, q Query, p page.Request) (Result, error) {
	var res Result
	err := s.DB.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		res, err = s.search(ctx, tx, chain, q, p)
		return err
	}, db.ReadOnly())
	return res, err //nolint:wrapcheck // errs-coded
}

func (s *Service) search(ctx context.Context, tx pgx.Tx, chain uuid.UUID, q Query, p page.Request) (Result, error) {
	if err := q.Validate(); err != nil {
		return Result{}, err
	}
	binding := q.binding(chain)
	pg, err := page.Parse(p, binding, 1)
	if err != nil {
		return Result{}, err //nolint:wrapcheck // errs-coded
	}
	where := []string{"tenant_id = $1"}
	args := []any{chain}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	for col, v := range map[string]string{
		"actor_id": q.ActorID, "target_type": q.TargetType, "target_id": q.TargetID, "event_type": q.Type,
		"category": q.Category, "channel": q.Channel, "outcome": q.Outcome, "request_id": q.RequestID, "trace_id": q.TraceID,
	} {
		if v != "" {
			add(col+" = ?", v)
		}
	}
	if !q.From.IsZero() {
		add("occurred_at >= ?", q.From)
	}
	if !q.To.IsZero() {
		add("occurred_at < ?", q.To)
	}
	if !pg.First() {
		after, err := strconv.ParseInt(pg.After[0], 10, 64)
		if err != nil {
			return Result{}, errs.New(errs.CodeInvalidRequest, "invalid page_token")
		}
		add("sequence < ?", after)
	}
	// Audit of audit: record the access before reading anything.
	if err := s.record(ctx, AccessSearch, accessQuery{Chain: store.ChainName(chain), Query: q, Page: p.Token}); err != nil {
		return Result{}, err
	}
	args = append(args, pg.Fetch())
	sql := `SELECT sequence, recorded_at, event::text FROM audit.events WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY sequence DESC LIMIT $` + strconv.Itoa(len(args))
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return Result{}, errs.Wrap(errs.CodeOf(db.MapError(err)), err, "audit search")
	}
	records, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Record, error) {
		var rec Record
		var seq int64
		var ev string
		err := r.Scan(&seq, &rec.RecordedAt, &ev)
		rec.Sequence, rec.Event = uint64(seq), []byte(ev) //nolint:gosec // G115: CHECK sequence > 0
		return rec, err                                   //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return Result{}, errs.Wrap(errs.CodeOf(db.MapError(err)), err, "audit search")
	}
	records, next := page.Trim(pg, records, binding, func(r Record) []string {
		return []string{strconv.FormatUint(r.Sequence, 10)}
	})
	return Result{Records: records, NextPageToken: next}, nil
}

// Get returns the context tenant's record with the given ID.
func (s *Service) Get(ctx context.Context, id ids.AuditEventID) (Record, error) {
	t, err := scope.TenantFrom(ctx)
	if err != nil {
		return Record{}, err //nolint:wrapcheck // errs-coded
	}
	var rec Record
	err = s.DB.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.record(ctx, AccessView, map[string]string{"chain": store.ChainName(store.ChainOf(t.TenantID)), "id": id.String()}); err != nil {
			return err
		}
		var seq int64
		var ev string
		err := tx.QueryRow(ctx, `SELECT e.sequence, e.recorded_at, e.event::text FROM audit.events e
WHERE e.tenant_id = $1 AND e.id = $2`, t.TenantID.UUID(), id.UUID()).Scan(&seq, &rec.RecordedAt, &ev)
		if err != nil {
			return db.MapError(err)
		}
		rec.Sequence, rec.Event = uint64(seq), []byte(ev) //nolint:gosec // G115: CHECK sequence > 0
		return nil
	}, db.ReadOnly())
	return rec, err //nolint:wrapcheck // errs-coded
}

// ExportRange selects the records to export.
type ExportRange struct {
	FromSeq uint64 `json:"from_seq,omitempty"`
	ToSeq   uint64 `json:"to_seq,omitempty"`
}

// Export writes the context tenant's chain in the verifiable export format
// (package verify), so the tenant can verify it offline.
func (s *Service) Export(ctx context.Context, r ExportRange, w io.Writer) error {
	t, err := scope.TenantFrom(ctx)
	if err != nil {
		return err //nolint:wrapcheck // errs-coded
	}
	chain := store.ChainOf(t.TenantID)
	err = s.DB.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.record(ctx, AccessExport, map[string]any{"chain": store.ChainName(chain), "range": r}); err != nil {
			return err
		}
		_, _, err := verify.Export(ctx, verify.DBSource{Q: tx, Chain: chain}, r.FromSeq, r.ToSeq, time.Now().UTC(), w)
		return err //nolint:wrapcheck // errs-coded
	}, db.ReadOnly(), db.RepeatableRead())
	return err //nolint:wrapcheck // errs-coded
}

// ExportChain exports any chain (platform operators).
func (s *Service) ExportChain(ctx context.Context, chain uuid.UUID, r ExportRange, w io.Writer) error {
	err := s.DB.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.record(ctx, AccessExport, map[string]any{"chain": store.ChainName(chain), "range": r}); err != nil {
			return err
		}
		_, _, err := verify.Export(ctx, verify.DBSource{Q: tx, Chain: chain}, r.FromSeq, r.ToSeq, time.Now().UTC(), w)
		return err //nolint:wrapcheck // errs-coded
	}, db.ReadOnly(), db.RepeatableRead())
	return err //nolint:wrapcheck // errs-coded
}

func (s *Service) record(ctx context.Context, kind string, query any) error {
	if s.Recorder == nil {
		return errs.New(errs.CodeInternal, "audit search: no access recorder configured")
	}
	if err := s.Recorder.RecordAccess(ctx, kind, query); err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "audit search: the access could not be recorded")
	}
	return nil
}
