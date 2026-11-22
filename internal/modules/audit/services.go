package audit

import (
	"context"
	"encoding/json"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/checkpoint"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/reconcile"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/retention"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/search"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/writer"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// The Auditor records accesses to audit records and retention actions.
var (
	_ search.AccessRecorder = (*Auditor)(nil)
	_ retention.Recorder    = (*Auditor)(nil)
)

// Types of the audit services, exposed to other modules and cmd/*.
type (
	// Writer appends events from the bus to the store.
	Writer = writer.Writer
	// WriterConfig configures a Writer.
	WriterConfig = writer.Config
	// Checkpointer signs and timestamps checkpoints.
	Checkpointer = checkpoint.Checkpointer
	// CheckpointConfig configures a Checkpointer.
	CheckpointConfig = checkpoint.Config
	// Search finds, shows and exports audit records.
	Search = search.Service
	// SearchQuery filters records.
	SearchQuery = search.Query
	// SearchResult is a page of records.
	SearchResult = search.Result
	// SearchRecord is one search result.
	SearchRecord = search.Record
	// ExportRange selects the records to export.
	ExportRange = search.ExportRange
	// Retention applies retention policies.
	Retention = retention.Service
	// RetentionConfig configures Retention.
	RetentionConfig = retention.Config
	// RetentionPolicy is a chain's retention policy.
	RetentionPolicy = retention.Policy
	// Reconciler compares the store with the bus.
	Reconciler = reconcile.Reconciler
	// ReconcilerConfig configures a Reconciler.
	ReconcilerConfig = reconcile.Config
)

// NewWriter validates cfg and returns a Writer.
func NewWriter(cfg WriterConfig) (*Writer, error) { return writer.New(cfg) } //nolint:wrapcheck // errs-coded

// NewCheckpointer validates cfg and returns a Checkpointer.
func NewCheckpointer(cfg CheckpointConfig) (*Checkpointer, error) { return checkpoint.New(cfg) } //nolint:wrapcheck // errs-coded

// NewSearch returns a Search service that records every access through a.
func NewSearch(d *db.DB, a *Auditor) *Search { return &search.Service{DB: d, Recorder: a} }

// NewRetention validates cfg and returns a Retention service that records
// every action through a.
func NewRetention(cfg RetentionConfig, a *Auditor) (*Retention, error) {
	cfg.Recorder = a
	return retention.New(cfg) //nolint:wrapcheck // errs-coded
}

// NewReconciler validates cfg and returns a Reconciler.
func NewReconciler(cfg ReconcilerConfig) (*Reconciler, error) { return reconcile.New(cfg) } //nolint:wrapcheck // errs-coded

// RecordAccess records a search, view or export of audit records through
// the spool. It fails closed: an error means the access must be refused.
func (a *Auditor) RecordAccess(ctx context.Context, kind string, query any) error {
	var typ string
	switch kind {
	case search.AccessSearch:
		typ = TypeAuditSearched
	case search.AccessView:
		typ = TypeAuditViewed
	case search.AccessExport:
		typ = TypeAuditExported
	default:
		return errs.New(errs.CodeInternal, "audit: unknown access kind")
	}
	v, err := jsonValue(query)
	if err != nil {
		return err
	}
	m, _ := v.(map[string]any)
	details := map[string]any{}
	switch kind {
	case search.AccessView:
		if id, ok := m["id"]; ok {
			details["event_id"] = id
		} else {
			details["event_id"] = v
		}
	case search.AccessExport:
		if chain, ok := m["chain"]; ok {
			details["chain"] = chain
			if r, ok := m["range"]; ok {
				details["range"] = r
			}
		} else {
			details["query"] = v
		}
	default:
		details["query"] = v
	}
	return a.Emit(ctx, Emitted{Type: typ, Details: details})
}

// RecordRetention records a retention action in the platform audit trail
// through the spool. It fails closed.
func (a *Auditor) RecordRetention(ctx context.Context, action string, details any) error {
	switch action {
	case retention.ActionPolicyChanged, retention.ActionApplied:
	default:
		return errs.New(errs.CodeInternal, "audit: unknown retention action")
	}
	v, err := jsonValue(details)
	if err != nil {
		return err
	}
	return a.Emit(ctx, Emitted{Type: action, Details: map[string]any{"details": v}})
}

// jsonValue converts v to plain JSON values (maps, slices, strings,
// numbers) so that it can be a detail.
func jsonValue(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "audit: encode details")
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "audit: encode details")
	}
	return out, nil
}
