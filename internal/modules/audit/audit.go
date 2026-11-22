package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/diff"
	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/crypto"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/keystore"
	"github.com/antonyrejith/portun/internal/platform/outbox"
	"github.com/antonyrejith/portun/internal/platform/scope"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// Deps are the Auditor's dependencies.
type Deps struct {
	// DB runs audited transactions. Required.
	DB *db.DB
	// Registry holds the event types. Required.
	Registry *Registry
	// Keystore holds the data subjects' keys for personal data. Required.
	Keystore *keystore.Store
	// Spool takes events outside transactions and failure events. Required.
	Spool *Spool
	// FingerprintRoot is the root secret (at least 32 bytes) from which each
	// chain's secret-fingerprint key is derived:
	// HKDF-SHA256(root, salt = none, info = "portun.audit.secret-fingerprint.v1/<tenant TypeID or platform>", 32 bytes).
	// Fingerprints are HMAC-SHA256 under that key, so they match within a
	// tenant and cannot be linked across tenants or brute-forced without the
	// root. Required.
	FingerprintRoot secret.Bytes
	// Clock returns the current time (default time.Now).
	Clock func() time.Time
	// Origin is recorded in every event.
	Origin Origin
	// Logger logs failures to record (never event contents).
	Logger *slog.Logger
}

// Auditor writes audit records: in the same transaction as the change they
// describe (Change, PlatformChange) or through the spool (Emit).
type Auditor struct {
	db     *db.DB
	reg    *Registry
	keys   *keystore.Store
	spool  *Spool
	fpRoot secret.Bytes
	clock  func() time.Time
	origin Origin
	logger *slog.Logger
}

// New returns an Auditor. It fails with errs.CodeInternal when a required
// dependency is missing.
func New(d Deps) (*Auditor, error) {
	switch {
	case d.DB == nil, d.Registry == nil, d.Keystore == nil, d.Spool == nil:
		return nil, errs.New(errs.CodeInternal, "audit: DB, Registry, Keystore and Spool are required")
	case len(d.FingerprintRoot.Reveal()) < crypto.MinMACKeySize:
		return nil, errs.New(errs.CodeInternal, "audit: the fingerprint root secret must be at least 32 bytes")
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	return &Auditor{
		db: d.DB, reg: d.Registry, keys: d.Keystore, spool: d.Spool, fpRoot: d.FingerprintRoot,
		clock: d.Clock, origin: d.Origin, logger: d.Logger.With(slog.String("component", "audit")),
	}, nil
}

// Registry returns the Auditor's registry.
func (a *Auditor) Registry() *Registry { return a.reg }

// Record describes one logical change made in a transaction.
type Record struct {
	// Type is a registered event type. Required.
	Type string
	// Target is the object changed.
	Target *Target
	// Before and After are the object's states (any JSON-marshalable
	// value, nil for none); the field-level diff is computed from them and
	// classified by the type's Fields.
	Before, After any
	// BeforeVersion and AfterVersion are configuration versions, if any.
	BeforeVersion, AfterVersion int64
	// Rows lists every database row the change wrote (reconciliation).
	Rows []RowRef
	// Subject is the data subject whose key encrypts the record's personal
	// fields (for example the user's ID); empty means the actor.
	Subject string
	// Details are extra facts; keys must be declared by the type.
	Details map[string]any
	// OrganizationID is the organization the change belongs to, if any.
	OrganizationID ids.OrganizationID
	// TenantID puts a record of a PlatformChange on that tenant's chain;
	// zero keeps it on the platform chain. In a Change it must be zero or
	// the scope's tenant.
	TenantID ids.TenantID
}

// recorded is a Record with its values snapshotted.
type recorded struct {
	Record
	before, after any
}

// Tx is an audited transaction: the only way to write to the database
// (ADR-0008). It offers the pgx query methods, so generated repository code
// can take it, and Record, which every transaction must call at least once.
// A Tx must not be used after its function returns.
type Tx struct {
	tx       pgx.Tx
	reg      *Registry
	platform bool
	tenant   scope.Tenant
	records  []recorded
	err      error
}

// Exec runs a statement in the transaction.
func (t *Tx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return t.tx.Exec(ctx, sql, args...) //nolint:wrapcheck // pass-through DBTX method; callers map errors with db.MapError
}

// Query runs a query in the transaction.
func (t *Tx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return t.tx.Query(ctx, sql, args...) //nolint:wrapcheck // pass-through DBTX method
}

// QueryRow runs a query that returns at most one row.
func (t *Tx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return t.tx.QueryRow(ctx, sql, args...)
}

// CopyFrom bulk-inserts rows with the COPY protocol.
func (t *Tx) CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource) (int64, error) {
	return t.tx.CopyFrom(ctx, table, columns, src) //nolint:wrapcheck // pass-through DBTX method
}

// SendBatch sends a batch of queries.
func (t *Tx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	return t.tx.SendBatch(ctx, b)
}

// Record registers one logical change. Errors (an unregistered type, a
// foreign tenant, values that are not JSON) are returned and also fail the
// transaction at commit, even if the caller ignores them.
func (t *Tx) Record(r Record) error {
	err := t.record(r)
	if err != nil && t.err == nil {
		t.err = err
	}
	return err
}

func (t *Tx) record(r Record) error {
	if _, err := t.reg.mustLookup(r.Type); err != nil {
		return err
	}
	if !t.platform && !r.TenantID.IsZero() && r.TenantID != t.tenant.TenantID {
		return errs.New(errs.CodeInternal, "audit: a tenant change cannot record events for another tenant")
	}
	before, err := diff.Normalize(r.Before)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "audit: before value")
	}
	after, err := diff.Normalize(r.After)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "audit: after value")
	}
	r.Rows = append([]RowRef(nil), r.Rows...)
	t.records = append(t.records, recorded{Record: r, before: before, after: after})
	return nil
}

// Change runs fn in a transaction bound to the context's tenant
// (db.InTenantTx). Every record fn makes is written to the outbox in the
// same transaction; a transaction without a record, or with a failed one, is
// rolled back with errs.CodeInternal (fail closed). When the transaction
// fails, a failure event is written through the spool and the original
// error is returned.
func (a *Auditor) Change(ctx context.Context, fn func(ctx context.Context, tx *Tx) error) error {
	t, err := scope.TenantFrom(ctx)
	if err != nil {
		return err //nolint:wrapcheck // already an *errs.Error
	}
	return a.run(ctx, false, t, fn)
}

// PlatformChange is Change for platform-level work (partners, tenants): it
// runs fn through db.InPlatformTx, and records go to the platform chain
// unless they set a TenantID.
func (a *Auditor) PlatformChange(ctx context.Context, fn func(ctx context.Context, tx *Tx) error) error {
	return a.run(ctx, true, scope.Tenant{}, fn)
}

func (a *Auditor) run(ctx context.Context, platform bool, t scope.Tenant, fn func(ctx context.Context, tx *Tx) error) error {
	var atx *Tx
	body := func(ctx context.Context, ptx pgx.Tx) error {
		atx = &Tx{tx: ptx, reg: a.reg, platform: platform, tenant: t}
		if err := fn(ctx, atx); err != nil {
			return err
		}
		return a.commit(ctx, atx)
	}
	var err error
	if platform {
		err = a.db.InPlatformTx(ctx, body)
	} else {
		err = a.db.InTenantTx(ctx, body)
	}
	if err != nil {
		a.recordFailure(ctx, platform, t, atx, err)
	}
	return err //nolint:wrapcheck // db.MapError already produced an *errs.Error
}

// commit builds the events of tx's records and inserts them into the outbox.
func (a *Auditor) commit(ctx context.Context, tx *Tx) error {
	if tx.err != nil {
		return tx.err
	}
	if len(tx.records) == 0 {
		return errs.New(errs.CodeInternal, "audit: the transaction recorded no audit event; rolled back")
	}
	var xid string
	if err := tx.tx.QueryRow(ctx, `SELECT pg_current_xact_id()::text`).Scan(&xid); err != nil {
		return db.MapError(err) //nolint:wrapcheck // db.MapError wraps in an *errs.Error
	}
	memo := map[string]keystore.Key{}
	for _, r := range tx.records {
		tenant, env := tx.tenant.TenantID, tx.tenant.EnvironmentID
		if tx.platform {
			tenant, env = r.TenantID, ids.EnvironmentID{}
		}
		in := input{
			rec: r.Record, before: r.before, after: r.after, outcome: OutcomeSuccess,
			tenant: tenant, env: env, dbxid: xid, withChange: true,
		}
		var ev *Event
		keyFn := func(subject string) (keystore.Key, error) {
			mk := tenantLabel(tenant) + "\x00" + subject
			if k, ok := memo[mk]; ok {
				return k, nil
			}
			k, err := a.keys.SubjectKey(ctx, tx, tenant, subject)
			if err != nil {
				return keystore.Key{}, err //nolint:wrapcheck // already an *errs.Error
			}
			addKeyRows(ev, k.Rows)
			k.Rows = nil
			memo[mk] = k
			return k, nil
		}
		ev, err := a.newEvent(ctx, in)
		if err != nil {
			return err
		}
		if err := a.protect(ev, in, keyFn); err != nil {
			return err
		}
		if err := a.insertOutbox(ctx, tx.tx, ev); err != nil {
			return err
		}
	}
	return nil
}

func (a *Auditor) insertOutbox(ctx context.Context, tx pgx.Tx, ev *Event) error {
	payload, err := jsonMarshal(ev)
	if err != nil {
		return err
	}
	subject, err := auditSubject(ev)
	if err != nil {
		return err
	}
	return outbox.Insert(ctx, tx, outbox.Message{ //nolint:wrapcheck // already an *errs.Error
		TenantID: ev.TenantID, ID: ev.ID.UUID(), Stream: bus.StreamAudit, Subject: subject, Payload: payload,
	})
}

func tenantLabel(t ids.TenantID) string {
	if t.IsZero() {
		return "platform"
	}
	return t.String()
}
