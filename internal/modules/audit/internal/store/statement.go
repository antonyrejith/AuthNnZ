package store

import (
	"encoding/json"
	"time"

	"github.com/gowebpki/jcs"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// RetentionStatement is the signed content of a retention mark.
type RetentionStatement struct {
	Kind       string `json:"kind"` // always "portun.audit.retention.v1"
	Chain      string `json:"chain"`
	FromSeq    uint64 `json:"from_seq"`
	ThroughSeq uint64 `json:"through_seq"`
	LastHash   string `json:"last_hash"`
	RemovedAt  string `json:"removed_at"` // RFC 3339, UTC
}

// RetentionKind identifies retention statements.
const RetentionKind = "portun.audit.retention.v1"

// StatementOf returns the statement a stored mark claims.
func StatementOf(m RetentionMark) RetentionStatement {
	return RetentionStatement{
		Kind:       RetentionKind,
		Chain:      m.Chain.String(),
		FromSeq:    m.FromSeq,
		ThroughSeq: m.ThroughSeq,
		LastHash:   m.LastHash,
		RemovedAt:  m.RemovedAt.UTC().Format(time.RFC3339Nano),
	}
}

// Canonical returns the signed bytes: JCS of the statement.
func (s RetentionStatement) Canonical() ([]byte, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "encode the retention statement")
	}
	out, err := jcs.Transform(b)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "canonicalize the retention statement")
	}
	return out, nil
}
