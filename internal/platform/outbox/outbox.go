// Package outbox is the transactional outbox (ADR-0007): messages for the
// bus are inserted into outbox.messages in the same PostgreSQL transaction as
// the change they describe, and the Relay publishes them once that
// transaction has committed.
//
// # Table and grants
//
// outbox.messages has the primary key (tenant_id, id); platform-level
// messages use the nil UUID as tenant_id. Row-level security restricts the
// application role (portun_app) to its tenant's rows, and it may only INSERT:
// it can neither read, update nor delete outbox rows. Because a tenant scope
// never holds the nil UUID, platform-level messages can only be inserted
// through db.InPlatformTx. The relay and the audit writer run as the platform
// role (portun_platform, BYPASSRLS), which may SELECT, INSERT, DELETE and
// UPDATE only the published_at column. Each row records the inserting
// transaction's ID (xid, pg_current_xact_id()), which links audit records to
// the write-ahead log for reconciliation.
//
// # Delivery
//
// The relay publishes each row with the JetStream message ID set to the row
// ID, so a row published twice is stored once. EVENTS and CONFIG rows are
// deleted once the bus acknowledges them. AUDIT rows are deleted by the audit
// writer (Delete) only after the event is stored; the relay marks them
// published and republishes them if they are still present after
// RelayConfig.RepublishAfter.
package outbox

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

// Schema is the PostgreSQL schema of the outbox.
const Schema = "outbox"

// Table is the schema-qualified outbox table.
const Table = "outbox.messages"

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations returns the outbox schema's migration set.
func Migrations() migrate.Set {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		// The embedded directory is part of the binary; this cannot fail.
		panic("outbox: embedded migrations missing: " + err.Error())
	}
	return migrate.Set{Schema: Schema, FS: sub}
}

// Message is one outbox row.
type Message struct {
	// TenantID owns the message; the zero value means platform level.
	TenantID ids.TenantID
	// ID is the row ID and the bus message ID. Required.
	ID uuid.UUID
	// Stream is bus.StreamAudit, bus.StreamEvents or bus.StreamConfig.
	Stream string
	// Subject is the bus subject; it must belong to Stream and, for AUDIT
	// and CONFIG, to TenantID (see Validate).
	Subject string
	// Payload is the JSON message body.
	Payload []byte
}

// Validate checks that the message can be published: a non-nil ID, a JSON
// payload, and a subject of the stream that matches the tenant:
// audit.<tenant> (audit.platform for the platform), evt.<module>.<type>, or
// config.<tenant>.<environment>. Checking here keeps unpublishable rows out
// of the table, where they would block nothing but never leave.
func (m Message) Validate() error {
	if m.ID == uuid.Nil {
		return errs.New(errs.CodeInternal, "outbox: message ID is required")
	}
	if !json.Valid(m.Payload) {
		return errs.New(errs.CodeInternal, "outbox: payload is not valid JSON")
	}
	switch m.Stream {
	case bus.StreamAudit:
		return m.validateAudit()
	case bus.StreamEvents:
		parts := strings.Split(m.Subject, ".")
		if len(parts) != 3 || parts[0] != "evt" {
			return errs.New(errs.CodeInternal, "outbox: EVENTS subjects are evt.<module>.<type>")
		}
		if _, err := bus.EventSubject(parts[1], parts[2]); err != nil {
			return errs.Wrap(errs.CodeInternal, err, "outbox: invalid event subject")
		}
		return nil
	case bus.StreamConfig:
		return m.validateConfig()
	default:
		return errs.New(errs.CodeInternal, "outbox: unknown stream "+m.Stream)
	}
}

func (m Message) validateAudit() error {
	want := bus.AuditPlatformSubject
	if !m.TenantID.IsZero() {
		s, err := bus.AuditSubject(m.TenantID)
		if err != nil {
			return errs.Wrap(errs.CodeInternal, err, "outbox: audit subject")
		}
		want = s
	}
	if m.Subject != want {
		return errs.New(errs.CodeInternal, "outbox: an AUDIT message's subject must be its tenant's audit subject")
	}
	return nil
}

func (m Message) validateConfig() error {
	parts := strings.Split(m.Subject, ".")
	if m.TenantID.IsZero() || len(parts) != 3 || parts[0] != "config" || parts[1] != m.TenantID.String() {
		return errs.New(errs.CodeInternal, "outbox: CONFIG subjects are config.<tenant>.<environment> of the message's tenant")
	}
	env, err := ids.Parse[ids.EnvironmentKind](parts[2])
	if err != nil || env.IsZero() {
		return errs.New(errs.CodeInternal, "outbox: CONFIG subject has an invalid environment")
	}
	return nil
}

// DBTX is the part of a transaction the outbox needs: Exec. Both pgx.Tx and
// the audit module's *audit.Tx satisfy it, so a module can insert outbox
// messages in its audited transaction.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

const insertSQL = `INSERT INTO outbox.messages (tenant_id, id, stream, subject, payload) VALUES ($1, $2, $3, $4, $5)`

// Insert adds m to the outbox within tx, the transaction of the change it
// describes. Tenant messages are inserted through db.InTenantTx (row-level
// security checks the tenant); platform-level messages through
// db.InPlatformTx.
func Insert(ctx context.Context, tx DBTX, m Message) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, insertSQL, m.TenantID.UUID(), m.ID, m.Stream, m.Subject, string(m.Payload)); err != nil {
		return mapErr(err)
	}
	return nil
}

const deleteSQL = `DELETE FROM outbox.messages WHERE tenant_id = $1 AND id = $2`

// Delete removes one message, for the audit writer once the event is stored.
// tx must run as the platform role (db.InPlatformTx). Deleting a message that
// is already gone succeeds, so the writer stays idempotent.
func Delete(ctx context.Context, tx DBTX, tenantID ids.TenantID, id uuid.UUID) error {
	if _, err := tx.Exec(ctx, deleteSQL, tenantID.UUID(), id); err != nil {
		return mapErr(err)
	}
	return nil
}

// mapErr maps a database error to an errs code (db.MapError).
func mapErr(err error) error {
	return db.MapError(err) //nolint:wrapcheck // db.MapError wraps in an *errs.Error
}
