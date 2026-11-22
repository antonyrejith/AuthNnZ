// Package retention removes expired audit records (GOV-01, plan 13 §6).
// Per chain, a policy sets the retention period in days and a legal hold.
// The job removes the oldest contiguous range of records that are all older
// than the period and that ends at a checkpoint boundary, through the only
// removal path the store allows (audit.apply_retention), which writes a
// signed retention mark in the same transaction. Verification continues
// from the mark: it proves the range, the last removed hash and when. A
// legal hold blocks removal. Every policy change and every removal is
// itself audited through the Recorder before it happens (fail closed).
package retention

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/checkpoint"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/kms"
)

// Recorder records retention actions in the platform audit trail (through
// the audit spool). An error makes the action fail before it happens.
type Recorder interface {
	RecordRetention(ctx context.Context, action string, details any) error
}

// Actions passed to the Recorder.
const (
	ActionPolicyChanged = "audit.retention_policy_changed"
	ActionApplied       = "audit.retention_applied"
)

// Config configures a Service.
type Config struct {
	DB       *db.DB
	KMS      kms.KMS
	Key      kms.KeyRef // signing key for retention marks
	Recorder Recorder
	Logger   *slog.Logger
	Now      func() time.Time
}

// Service applies retention.
type Service struct {
	cfg Config
}

// New validates cfg.
func New(cfg Config) (*Service, error) {
	if cfg.DB == nil || cfg.KMS == nil || cfg.Key == "" || cfg.Recorder == nil {
		return nil, errs.New(errs.CodeInvalidRequest, "retention: DB, KMS, key and recorder are required")
	}
	if cfg.Logger == nil {
		return nil, errs.New(errs.CodeInvalidRequest, "retention: a logger is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg}, nil
}

// Policy is a chain's retention policy.
type Policy struct {
	Chain         uuid.UUID `json:"chain"`
	RetentionDays int       `json:"retention_days"` // 0: keep forever
	LegalHold     bool      `json:"legal_hold"`
}

// SetPolicy stores a chain's policy (recorded first).
func (s *Service) SetPolicy(ctx context.Context, p Policy) error {
	if p.RetentionDays < 0 {
		return errs.New(errs.CodeValidationFailed, "retention days must not be negative")
	}
	if err := s.cfg.Recorder.RecordRetention(ctx, ActionPolicyChanged, p); err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "retention: the policy change could not be recorded")
	}
	var days *int
	if p.RetentionDays > 0 {
		days = &p.RetentionDays
	}
	return s.cfg.DB.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error { //nolint:wrapcheck // errs-coded
		_, err := tx.Exec(ctx, `INSERT INTO audit.retention_policies (chain_id, retention_days, legal_hold, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (chain_id) DO UPDATE SET retention_days = EXCLUDED.retention_days, legal_hold = EXCLUDED.legal_hold, updated_at = now()`,
			p.Chain, days, p.LegalHold)
		return err //nolint:wrapcheck // mapped by InPlatformTx
	})
}

// legalHoldMessage describes a removal blocked by a legal hold.
const legalHoldMessage = "retention: the chain is under legal hold"

// plan is a removal about to happen.
type plan struct {
	from, through uint64
	lastHash      string
}

// ApplyAll applies retention to every chain with a policy and returns the
// marks written. Chains under legal hold are skipped (logged).
func (s *Service) ApplyAll(ctx context.Context) ([]store.RetentionMark, error) {
	var chains []uuid.UUID
	err := s.cfg.DB.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT chain_id FROM audit.retention_policies WHERE retention_days IS NOT NULL ORDER BY chain_id`)
		if err != nil {
			return err //nolint:wrapcheck // mapped by InPlatformTx
		}
		chains, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		return err //nolint:wrapcheck // mapped by InPlatformTx
	}, db.ReadOnly())
	if err != nil {
		return nil, err //nolint:wrapcheck // errs-coded
	}
	var out []store.RetentionMark
	for _, c := range chains {
		m, err := s.Apply(ctx, c)
		switch {
		case errs.Is(err, errs.CodePreconditionFailed):
			s.cfg.Logger.InfoContext(ctx, "audit retention skipped: legal hold", slog.String("chain", store.ChainName(c)))
		case err != nil:
			return out, err
		case m != nil:
			out = append(out, *m)
		}
	}
	return out, nil
}

// Apply removes chain's expired range and returns the mark, or nil when
// nothing is due. A legal hold fails with errs.CodePreconditionFailed.
func (s *Service) Apply(ctx context.Context, chain uuid.UUID) (*store.RetentionMark, error) {
	p, err := s.plan(ctx, chain)
	if err != nil || p == nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "retention: new ID")
	}
	m := store.RetentionMark{
		Chain: chain, ID: id, FromSeq: p.from, ThroughSeq: p.through, LastHash: p.lastHash,
		RemovedAt: s.cfg.Now().UTC().Truncate(time.Microsecond), KeyRef: string(s.cfg.Key),
	}
	if m.Payload, err = store.StatementOf(m).Canonical(); err != nil {
		return nil, err //nolint:wrapcheck // errs-coded
	}
	sig, err := s.cfg.KMS.Sign(ctx, s.cfg.Key, m.Payload)
	if err != nil {
		return nil, errs.Wrap(errs.CodeOf(err), err, "retention: sign the mark")
	}
	m.KeyVersion, m.Signature = sig.KeyVersion, sig.DER
	if err := checkpoint.PublishKey(ctx, s.cfg.DB, s.cfg.KMS, s.cfg.Key, sig.KeyVersion); err != nil {
		return nil, err //nolint:wrapcheck // errs-coded
	}
	if err := s.cfg.Recorder.RecordRetention(ctx, ActionApplied, store.StatementOf(m)); err != nil {
		return nil, errs.Wrap(errs.CodeUnavailable, err, "retention: the removal could not be recorded")
	}
	err = s.cfg.DB.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var removed int64
		err := tx.QueryRow(ctx, `SELECT audit.apply_retention($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			chain, m.ID, int64(m.FromSeq), int64(m.ThroughSeq), m.LastHash, m.RemovedAt, m.Payload, m.KeyRef, m.KeyVersion, m.Signature, //nolint:gosec // G115: sequences <= MaxSequence
		).Scan(&removed)
		if err != nil {
			return holdOrMap(err)
		}
		s.cfg.Logger.InfoContext(ctx, "audit retention applied", slog.String("chain", store.ChainName(chain)),
			slog.Uint64("from_seq", m.FromSeq), slog.Uint64("through_seq", m.ThroughSeq), slog.Int64("removed", removed))
		return nil
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // errs-coded
	}
	return &m, nil
}

// plan finds the range to remove: from after the last mark through the
// last checkpoint whose events are all older than the retention period.
func (s *Service) plan(ctx context.Context, chain uuid.UUID) (*plan, error) {
	var out *plan
	err := s.cfg.DB.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var days *int
		var hold bool
		err := tx.QueryRow(ctx, `SELECT retention_days, legal_hold FROM audit.retention_policies WHERE chain_id = $1`, chain).Scan(&days, &hold)
		if err != nil {
			if errs.Is(db.MapError(err), errs.CodeNotFound) {
				return nil
			}
			return err //nolint:wrapcheck // mapped by InPlatformTx
		}
		if hold {
			return errs.New(errs.CodePreconditionFailed, legalHoldMessage)
		}
		if days == nil {
			return nil
		}
		cutoff := s.cfg.Now().UTC().AddDate(0, 0, -*days)
		var base int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(through_seq), 0) FROM audit.retention_marks WHERE tenant_id = $1`, chain).Scan(&base); err != nil {
			return err //nolint:wrapcheck // mapped by InPlatformTx
		}
		var through *int64
		err = tx.QueryRow(ctx, `
SELECT max(c.to_seq) FROM audit.checkpoints c
WHERE c.tenant_id = $1 AND c.to_seq > $2
  AND c.to_seq < coalesce((SELECT min(e.sequence) FROM audit.events e WHERE e.tenant_id = $1 AND e.recorded_at >= $3),
                          9223372036854775807)`, chain, base, cutoff).Scan(&through)
		if err != nil || through == nil {
			return err //nolint:wrapcheck // mapped by InPlatformTx
		}
		var last string
		if err := tx.QueryRow(ctx, `SELECT hash FROM audit.events WHERE tenant_id = $1 AND sequence = $2`, chain, *through).Scan(&last); err != nil {
			return err //nolint:wrapcheck // mapped by InPlatformTx
		}
		out = &plan{from: uint64(base) + 1, through: uint64(*through), lastHash: last} //nolint:gosec // G115: non-negative sequences
		return nil
	}, db.ReadOnly(), db.RepeatableRead())
	return out, err //nolint:wrapcheck // errs-coded
}

// holdOrMap maps the store's legal-hold refusal to CodePreconditionFailed.
func holdOrMap(err error) error {
	mapped := db.MapError(err)
	if errs.Is(mapped, errs.CodeInternal) && strings.Contains(err.Error(), "legal hold") {
		return errs.Wrap(errs.CodePreconditionFailed, err, legalHoldMessage)
	}
	return mapped //nolint:wrapcheck // errs-coded
}
