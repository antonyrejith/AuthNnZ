// Package audit is the audit module's facade (ADR-0008): the record format,
// the event registry, audited writes (Change, PlatformChange), the spool for
// events outside transactions, and verification.
package audit

import (
	"time"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

// Category groups event types. Each category maps to an OCSF class.
type Category string

// Event categories.
const (
	CategoryConfiguration  Category = "configuration"   // configuration objects, publications
	CategoryData           Category = "data"            // tenant data (users, groups, apps, ...)
	CategoryAuthentication Category = "authentication"  // logins, MFA, sessions, tokens
	CategoryAuthorization  Category = "authorization"   // allow and deny decisions
	CategorySensitiveRead  Category = "sensitive_read"  // revealing PII, exports, impersonation
	CategoryAuditAccess    Category = "audit_access"    // search, view, export of audit records
	CategoryKeyManagement  Category = "key_management"  // key creation, rotation, destruction
	CategoryPlatform       Category = "platform"        // partners, tenants, platform operations
)

// Outcome is the result of the audited action.
type Outcome string

// Outcomes.
const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
)

// Event is one audit record. Its JSON form is the canonical record: the hash
// is SHA-256(prev_hash || JCS(event with Integrity == nil)), RFC 8785.
// Fields are never removed or renamed; new optional fields may be added.
type Event struct {
	ID             ids.AuditEventID   `json:"id"`
	Sequence       uint64             `json:"sequence"` // assigned by the writer; 0 until written; hashed
	Time           time.Time          `json:"time"`     // UTC, set at capture, millisecond precision
	TenantID       ids.TenantID       `json:"tenant_id"` // zero => platform chain
	EnvironmentID  ids.EnvironmentID  `json:"environment_id"`
	OrganizationID ids.OrganizationID `json:"organization_id"`
	Category       Category           `json:"category"`
	Type           string             `json:"type"` // registered, e.g. "tenant.created"
	Outcome        Outcome            `json:"outcome"`
	ErrorCode      errs.Code          `json:"error_code,omitempty"`
	Actor          Actor              `json:"actor"`
	Channel        Channel            `json:"channel"`
	Target         *Target            `json:"target,omitempty"`
	Change         *ChangeSet         `json:"change,omitempty"`
	Reason         *Reason            `json:"reason,omitempty"`
	Request        Request            `json:"request"`
	Origin         Origin             `json:"origin"`
	Details        map[string]any     `json:"details,omitempty"` // only keys declared in the registry
	Integrity      *Integrity         `json:"integrity,omitempty"`
}

// Actor is who performed the action (from scope.Actor).
type Actor struct {
	Type           string   `json:"type"`
	ID             string   `json:"id"`
	SessionID      string   `json:"session_id,omitempty"`
	ACR            string   `json:"acr,omitempty"`
	AMR            []string `json:"amr,omitempty"`
	ClientID       string   `json:"client_id,omitempty"`
	OnBehalfOf     string   `json:"on_behalf_of,omitempty"`
	ImpersonatedBy string   `json:"impersonated_by,omitempty"`
}

// Channel is how the request arrived.
type Channel struct {
	Type      string `json:"type"` // console, api, cli, terraform, gitops, scim, system, ...
	IP        string `json:"ip,omitempty"`        // personal data: encrypted
	UserAgent string `json:"user_agent,omitempty"` // personal data: encrypted
}

// Target is the object acted on.
type Target struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// ChangeSet is the field-level difference of a change (RFC 6902 paths with
// both values, so any version can be reconstructed).
type ChangeSet struct {
	BeforeVersion int64       `json:"before_version,omitempty"`
	AfterVersion  int64       `json:"after_version,omitempty"`
	Diff          []DiffEntry `json:"diff"`
	Rows          []RowRef    `json:"rows"` // every database row this record accounts for (reconciliation)
}

// RowRef identifies one written row: schema-qualified table and primary key
// values in key-column order, as text.
type RowRef struct {
	Table string   `json:"table"`
	Key   []string `json:"key"`
}

// DiffEntry is one changed field. Secret values appear as "[REDACTED]" plus a
// fingerprint; personal data appears encrypted under the subject's data key.
type DiffEntry struct {
	Op     string `json:"op"` // add, remove, replace
	Path   string `json:"path"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

// Reason records why a change was made.
type Reason struct {
	Text       string   `json:"text,omitempty"`
	Ticket     string   `json:"ticket,omitempty"`
	ApprovedBy []string `json:"approved_by,omitempty"`
}

// Request links the event to the request, trace and database transaction.
type Request struct {
	RequestID      string `json:"request_id,omitempty"`
	TraceID        string `json:"trace_id,omitempty"`
	Operation      string `json:"operation,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	DBXID          string `json:"db_xid,omitempty"` // pg_current_xact_id() of the change transaction
}

// Origin is where the event was produced.
type Origin struct {
	Region  string `json:"region,omitempty"`
	Cell    string `json:"cell,omitempty"`
	Service string `json:"service,omitempty"`
}

// Integrity is set by the writer and excluded from the hash input.
type Integrity struct {
	PrevHash     string `json:"prev_hash"` // "sha256:<64 hex>"; genesis is 64 zeros
	Hash         string `json:"hash"`
	CheckpointID string `json:"checkpoint,omitempty"`
}
