package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/gowebpki/jcs"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

// HashPrefix prefixes every hash in the chain.
const HashPrefix = "sha256:"

// GenesisHash is the prev_hash of a chain's first event.
const GenesisHash = HashPrefix + "0000000000000000000000000000000000000000000000000000000000000000"

// MaxSequence is the largest sequence number: JCS represents numbers as
// IEEE 754 doubles, which are exact up to 2^53.
const MaxSequence = 1<<53 - 1

// MaxRecordSize bounds one record's JSON form.
const MaxRecordSize = 1 << 20

// Header holds the record fields the store indexes and checks. It mirrors
// field names of audit.Event (a test keeps them aligned).
type Header struct {
	ID       ids.AuditEventID `json:"id"`
	Sequence uint64           `json:"sequence"`
	Time     time.Time        `json:"time"`
	TenantID ids.TenantID     `json:"tenant_id"`
	Category string           `json:"category"`
	Type     string           `json:"type"`
	Outcome  string           `json:"outcome"`
	Actor    struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"actor"`
	Channel struct {
		Type string `json:"type"`
	} `json:"channel"`
	Target *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"target"`
	Request struct {
		RequestID string `json:"request_id"`
		TraceID   string `json:"trace_id"`
		DBXID     string `json:"db_xid"`
	} `json:"request"`
	Change *struct {
		Rows []RowRef `json:"rows"`
	} `json:"change"`
	Integrity *Integrity `json:"integrity"`
}

// RowRef is one database row a record accounts for (audit.RowRef).
type RowRef struct {
	Table string   `json:"table"`
	Key   []string `json:"key"`
}

// Integrity is the writer-set part of a stored record (audit.Integrity).
type Integrity struct {
	PrevHash     string `json:"prev_hash"`
	Hash         string `json:"hash"`
	CheckpointID string `json:"checkpoint,omitempty"`
}

// TargetType returns the target type or "".
func (h Header) TargetType() string {
	if h.Target == nil {
		return ""
	}
	return h.Target.Type
}

// TargetID returns the target ID or "".
func (h Header) TargetID() string {
	if h.Target == nil {
		return ""
	}
	return h.Target.ID
}

// Rows returns the declared rows.
func (h Header) Rows() []RowRef {
	if h.Change == nil {
		return nil
	}
	return h.Change.Rows
}

// DecodeHeader parses and validates a record: a JSON object within
// MaxRecordSize, with an ID, time, category, type and outcome, that
// canonicalizes (no duplicate keys, valid UTF-8 and escapes) and that a
// jsonb column can hold (no NUL characters). Errors carry
// errs.CodeInvalidRequest.
func DecodeHeader(raw []byte) (Header, error) {
	var h Header
	if len(raw) > MaxRecordSize {
		return h, invalid("record exceeds " + strconv.Itoa(MaxRecordSize) + " bytes")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return h, invalid("record is not a JSON object")
	}
	if bytes.Contains(raw, []byte(`\u0000`)) {
		return h, invalid("record contains a NUL character")
	}
	if _, err := jcs.Transform(raw); err != nil {
		return h, errs.Wrap(errs.CodeInvalidRequest, err, "record cannot be canonicalized")
	}
	if err := json.Unmarshal(raw, &h); err != nil {
		return h, errs.Wrap(errs.CodeInvalidRequest, err, "record fields have invalid types")
	}
	switch {
	case h.ID.IsZero():
		return h, invalid("record has no id")
	case h.Time.IsZero():
		return h, invalid("record has no time")
	case h.Category == "" || h.Type == "" || h.Outcome == "":
		return h, invalid("record needs category, type and outcome")
	}
	return h, nil
}

// Canonical returns JCS(record with "sequence" set to seq and "integrity"
// removed): the bytes the chain hashes. raw must be a JSON object.
func Canonical(raw []byte, seq uint64) ([]byte, error) {
	if seq == 0 || seq > MaxSequence {
		return nil, invalid("sequence out of range")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, errs.Wrap(errs.CodeInvalidRequest, err, "record is not a JSON object")
	}
	delete(m, "integrity")
	m["sequence"] = json.RawMessage(strconv.FormatUint(seq, 10))
	b, err := json.Marshal(m)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInvalidRequest, err, "re-encode the record")
	}
	out, err := jcs.Transform(b)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInvalidRequest, err, "canonicalize the record")
	}
	return out, nil
}

// ChainHash returns "sha256:" + hex(SHA-256(raw(prevHash) || canonical)).
func ChainHash(prevHash string, canonical []byte) (string, error) {
	prev, err := ParseHash(prevHash)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(prev)
	h.Write(canonical)
	return FormatHash(h.Sum(nil)), nil
}

// FormatHash renders a SHA-256 digest as "sha256:<64 hex>".
func FormatHash(sum []byte) string { return HashPrefix + hex.EncodeToString(sum) }

// ParseHash returns the 32 digest bytes of "sha256:<64 lowercase hex>".
func ParseHash(s string) ([]byte, error) {
	hexPart, ok := strings.CutPrefix(s, HashPrefix)
	if !ok || len(hexPart) != 2*sha256.Size || strings.ToLower(hexPart) != hexPart {
		return nil, invalid("hash must be \"sha256:\" and 64 lowercase hex digits")
	}
	b, err := hex.DecodeString(hexPart)
	if err != nil {
		return nil, invalid("hash must be \"sha256:\" and 64 lowercase hex digits")
	}
	return b, nil
}

// Sealed is a record placed in its chain.
type Sealed struct {
	Sequence uint64
	PrevHash string
	Hash     string
	// Stored is the record with sequence and integrity set, as stored.
	Stored []byte
}

// Seal places raw at seq after prevHash.
func Seal(raw []byte, seq uint64, prevHash string) (Sealed, error) {
	canonical, err := Canonical(raw, seq)
	if err != nil {
		return Sealed{}, err
	}
	hash, err := ChainHash(prevHash, canonical)
	if err != nil {
		return Sealed{}, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &m); err != nil {
		return Sealed{}, errs.Wrap(errs.CodeInternal, err, "decode the canonical record")
	}
	integrity, err := json.Marshal(Integrity{PrevHash: prevHash, Hash: hash})
	if err != nil {
		return Sealed{}, errs.Wrap(errs.CodeInternal, err, "encode integrity")
	}
	m["integrity"] = integrity
	stored, err := json.Marshal(m)
	if err != nil {
		return Sealed{}, errs.Wrap(errs.CodeInternal, err, "encode the stored record")
	}
	return Sealed{Sequence: seq, PrevHash: prevHash, Hash: hash, Stored: stored}, nil
}

// Recompute checks a stored record: it returns the hash recomputed from the
// record's content and its claimed sequence and prev_hash.
func Recompute(stored []byte) (Header, string, error) {
	var h Header
	if err := json.Unmarshal(stored, &h); err != nil {
		return h, "", errs.Wrap(errs.CodeInvalidRequest, err, "stored record is malformed")
	}
	if h.Integrity == nil {
		return h, "", invalid("stored record has no integrity")
	}
	canonical, err := Canonical(stored, h.Sequence)
	if err != nil {
		return h, "", err
	}
	hash, err := ChainHash(h.Integrity.PrevHash, canonical)
	if err != nil {
		return h, "", err
	}
	return h, hash, nil
}

func invalid(msg string) error { return errs.New(errs.CodeInvalidRequest, msg) }
