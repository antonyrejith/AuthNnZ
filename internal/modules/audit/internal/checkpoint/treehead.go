// Package checkpoint signs audit checkpoints (ADR-0008, GOV-18): at a
// regular interval, for each chain with new events, an RFC 6962 Merkle root
// over the new event hashes is put into a signed tree head, signed through
// the KMS (ECDSA P-256) and timestamped by an RFC 3161 timestamp authority.
// It also proves the inclusion of single events.
package checkpoint

import (
	"encoding/json"
	"time"

	"github.com/gowebpki/jcs"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// TreeHead is the signed statement of a checkpoint.
type TreeHead struct {
	Chain            string `json:"chain"` // chain UUID
	FromSeq          uint64 `json:"from_seq"`
	ToSeq            uint64 `json:"to_seq"`
	Root             string `json:"root"` // "sha256:<hex>"
	PrevCheckpointID string `json:"prev_checkpoint_id"`
	Time             string `json:"time"` // RFC 3339, UTC, nanoseconds trimmed
}

// TimeFormat is the tree head's time format.
const TimeFormat = time.RFC3339Nano

// HeadOf returns the tree head a stored checkpoint claims.
func HeadOf(c store.Checkpoint) TreeHead {
	prev := ""
	if c.PrevID != nil {
		prev = c.PrevID.String()
	}
	return TreeHead{
		Chain:            c.Chain.String(),
		FromSeq:          c.FromSeq,
		ToSeq:            c.ToSeq,
		Root:             c.Root,
		PrevCheckpointID: prev,
		Time:             c.Time.UTC().Format(TimeFormat),
	}
}

// Canonical returns the bytes that are signed: JCS of the tree head.
func (t TreeHead) Canonical() ([]byte, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "encode the tree head")
	}
	out, err := jcs.Transform(b)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "canonicalize the tree head")
	}
	return out, nil
}

// LeafData returns the Merkle leaf data of event hashes.
func LeafData(hashes []string) ([][]byte, error) {
	out := make([][]byte, len(hashes))
	for i, h := range hashes {
		b, err := store.ParseHash(h)
		if err != nil {
			return nil, err //nolint:wrapcheck // errs-coded by the callee
		}
		out[i] = b
	}
	return out, nil
}

// RootOf returns the formatted Merkle root over event hashes.
func RootOf(hashes []string) (string, error) {
	leaves, err := LeafData(hashes)
	if err != nil {
		return "", err
	}
	return store.FormatHash(RootHash(leaves)), nil
}
