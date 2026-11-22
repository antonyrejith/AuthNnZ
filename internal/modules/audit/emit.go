package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/classify"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/keystore"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// Emitted is an event outside a database transaction: decisions, failed
// sign-ins, stateless token issuance, audit-log reads.
type Emitted struct {
	// Type is a registered event type. Required.
	Type string
	// Outcome defaults to success.
	Outcome Outcome
	// ErrorCode is set for failures.
	ErrorCode errs.Code
	// Target is the object acted on, if any.
	Target *Target
	// Subject keys personal details (default: the actor).
	Subject string
	// Details are extra facts; keys must be declared by the type.
	Details map[string]any
	// OrganizationID is the organization, if any.
	OrganizationID ids.OrganizationID
}

// Emit records e through the spool: the event is on disk before Emit
// returns. The chain is the context's tenant, or the platform chain without
// a tenant scope. Emit fails, and the caller must refuse the action, when
// the event cannot be recorded (strict mode): spool full or unwritable
// (errs.CodeUnavailable), unregistered type, or a data key that cannot be
// obtained.
func (a *Auditor) Emit(ctx context.Context, e Emitted) error {
	var t scope.Tenant
	if st, err := scope.TenantFrom(ctx); err == nil {
		t = st
	}
	if e.Outcome == "" {
		e.Outcome = OutcomeSuccess
	}
	in := input{
		rec:       Record{Type: e.Type, Target: e.Target, Subject: e.Subject, Details: e.Details, OrganizationID: e.OrganizationID},
		outcome:   e.Outcome,
		errorCode: e.ErrorCode,
		tenant:    t.TenantID,
		env:       t.EnvironmentID,
	}
	ev, err := a.newEvent(ctx, in)
	if err != nil {
		return err
	}
	if err := a.protect(ev, in, a.outsideKeys(ctx, t.TenantID)); err != nil {
		return err
	}
	return a.spool.append(ev)
}

// recordFailure writes failure events for a failed transaction through the
// spool: one per record made before the failure, or one
// TypeTransactionFailed event. Personal channel data is omitted when no key
// can be obtained (for example while the database is down), so the failure
// is still recorded. A spool error is logged: the caller returns the
// original error either way.
func (a *Auditor) recordFailure(ctx context.Context, platform bool, t scope.Tenant, tx *Tx, cause error) {
	var recs []Record
	if tx != nil {
		for _, r := range tx.records {
			recs = append(recs, Record{Type: r.Type, Target: r.Target, Subject: r.Subject, OrganizationID: r.OrganizationID, TenantID: r.TenantID})
		}
	}
	if len(recs) == 0 {
		recs = []Record{{Type: TypeTransactionFailed}}
	}
	for _, r := range recs {
		tenant, env := t.TenantID, t.EnvironmentID
		if platform {
			tenant, env = r.TenantID, ids.EnvironmentID{}
		}
		in := input{rec: r, outcome: OutcomeFailure, errorCode: errs.CodeOf(cause), tenant: tenant, env: env}
		ev, err := a.failureEvent(ctx, in)
		if err == nil {
			err = a.spool.append(ev)
		}
		if err != nil {
			a.logger.ErrorContext(ctx, "audit: failed to record a failed change",
				slog.String("event_type", r.Type), slog.String("error_code", string(errs.CodeOf(cause))), slog.String("error", err.Error()))
		}
	}
}

func (a *Auditor) failureEvent(ctx context.Context, in input) (*Event, error) {
	ev, err := a.newEvent(ctx, in)
	if err != nil {
		return nil, err
	}
	if err := a.protect(ev, in, a.outsideKeys(ctx, in.tenant)); err == nil {
		return ev, nil
	}
	in.dropPersonal = true
	ev, err = a.newEvent(ctx, in)
	if err != nil {
		return nil, err
	}
	if err := a.protect(ev, in, a.outsideKeys(ctx, in.tenant)); err != nil {
		return nil, err
	}
	return ev, nil
}

type creatingKeyCtx struct{}

// outsideKeys resolves data keys for events outside a transaction: from the
// cache, else by a read-only lookup, else by creating the key in its own
// audited change (a TypeDataKeyCreated record declares the key row).
func (a *Auditor) outsideKeys(ctx context.Context, tenant ids.TenantID) keyFunc {
	return func(subject string) (keystore.Key, error) {
		if k, ok := a.keys.CachedSubjectKey(tenant, subject); ok {
			return k, nil
		}
		tctx := ctx
		if !tenant.IsZero() {
			tctx = scope.WithTenant(ctx, scope.Tenant{TenantID: tenant})
		}
		k, found, err := a.lookupKey(tctx, tenant, subject)
		if err != nil || found {
			return k, err
		}
		if ctx.Value(creatingKeyCtx{}) != nil {
			return keystore.Key{}, errs.New(errs.CodeUnavailable, "audit: data key unavailable")
		}
		return a.createKey(context.WithValue(tctx, creatingKeyCtx{}, true), tenant, subject)
	}
}

// lookupKey reads the subject's key in a read-only transaction.
func (a *Auditor) lookupKey(ctx context.Context, tenant ids.TenantID, subject string) (keystore.Key, bool, error) {
	var (
		k     keystore.Key
		found bool
	)
	lookup := func(ctx context.Context, tx pgx.Tx) error {
		var err error
		k, found, err = a.keys.LookupSubjectKey(ctx, tx, tenant, subject)
		return err //nolint:wrapcheck // already an *errs.Error
	}
	var err error
	if tenant.IsZero() {
		err = a.db.InPlatformTx(ctx, lookup, db.ReadOnly())
	} else {
		err = a.db.InTenantTx(ctx, lookup, db.ReadOnly())
	}
	return k, found, err //nolint:wrapcheck // db.MapError already produced an *errs.Error
}

// createKey creates the subject's key in its own audited change.
func (a *Auditor) createKey(ctx context.Context, tenant ids.TenantID, subject string) (keystore.Key, error) {
	var k keystore.Key
	create := func(ctx context.Context, tx *Tx) error {
		nk, err := a.keys.SubjectKey(ctx, tx, tenant, subject)
		if err != nil {
			return err //nolint:wrapcheck // already an *errs.Error
		}
		rows := make([]RowRef, 0, len(nk.Rows))
		for _, r := range nk.Rows {
			rows = append(rows, RowRef{Table: r.Table, Key: r.Key})
		}
		k = keystore.Key{ID: nk.ID, DEK: nk.DEK}
		return tx.Record(Record{
			Type:     TypeDataKeyCreated,
			Target:   &Target{Type: "data_key", ID: nk.ID.String()},
			Rows:     rows,
			Subject:  subject,
			TenantID: tenant,
		})
	}
	var err error
	if tenant.IsZero() {
		err = a.PlatformChange(ctx, create)
	} else {
		err = a.Change(ctx, create)
	}
	return k, err
}

// DecryptPersonal decrypts a personal value ("pii:v1:...") found at pointer
// (its location in the event, for example "/channel/ip" or
// "/change/diff/0/after") of the event eventID on tenantID's chain, and
// returns the original JSON value. q is a transaction of that tenant (or a
// platform transaction for the platform chain). After the subject's key is
// shredded it fails with errs.CodeNotFound wrapping keystore.ErrShredded.
// Revealing personal data is a sensitive read: callers record it.
func (a *Auditor) DecryptPersonal(ctx context.Context, q keystore.DBTX, tenantID ids.TenantID, eventID ids.AuditEventID, pointer, value string) (any, error) {
	keyText, ct, err := classify.ParsePII(value)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInvalidRequest, err, "audit: not an encrypted personal value")
	}
	keyID, err := ids.Parse[ids.KeyKind](keyText)
	if err != nil {
		return nil, err
	}
	dek, err := a.keys.DataKey(ctx, q, tenantID, keyID)
	if err != nil {
		return nil, err //nolint:wrapcheck // already an *errs.Error
	}
	pt, err := dek.Open(ct, classify.PIIAAD(eventID.String(), pointer))
	if err != nil {
		return nil, err //nolint:wrapcheck // already an *errs.Error
	}
	dec := json.NewDecoder(bytes.NewReader(pt))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "audit: decode personal value")
	}
	return out, nil
}
