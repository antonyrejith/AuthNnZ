package audit

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/classify"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/diff"
	"github.com/antonyrejith/portun/internal/platform/crypto"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/keystore"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// AnonymousSubject keys the personal data (client IP, user agent) of events
// whose actor is unknown, for example failed sign-ins.
const AnonymousSubject = "anonymous"

const fingerprintInfo = "portun.audit.secret-fingerprint.v1/"

// input is everything an event is built from besides the context scope.
type input struct {
	rec           Record
	before, after any // normalized
	outcome       Outcome
	errorCode     errs.Code
	tenant        ids.TenantID
	env           ids.EnvironmentID
	dbxid         string
	withChange    bool // the event carries a change set (successful changes)
	dropPersonal  bool // omit the channel's personal fields (keys unavailable)
}

type keyFunc func(subject string) (keystore.Key, error)

// newEvent builds the unprotected event: identity, time, scope, actor,
// channel, request, target and diff.
func (a *Auditor) newEvent(ctx context.Context, in input) (*Event, error) {
	reg, err := a.reg.mustLookup(in.rec.Type)
	if err != nil {
		return nil, err
	}
	for k := range in.rec.Details {
		if _, ok := reg.details[k]; !ok {
			return nil, errs.New(errs.CodeInternal, "audit: detail "+quote(k)+" is not declared by event type "+quote(reg.Name))
		}
	}
	id, err := ids.New[ids.AuditEventKind]()
	if err != nil {
		return nil, err
	}
	req, _ := scope.RequestFrom(ctx)
	ev := &Event{
		ID:             id,
		Time:           a.clock().UTC().Truncate(time.Millisecond),
		TenantID:       in.tenant,
		EnvironmentID:  in.env,
		OrganizationID: in.rec.OrganizationID,
		Category:       reg.Category,
		Type:           reg.Name,
		Outcome:        in.outcome,
		ErrorCode:      in.errorCode,
		Actor:          a.actor(ctx, req),
		Channel:        Channel{Type: req.Channel, IP: req.IP, UserAgent: req.UserAgent},
		Origin:         a.origin,
		Request: Request{
			RequestID: req.RequestID,
			Operation: req.Operation,
			DBXID:     in.dbxid,
		},
	}
	if ev.Channel.Type == "" {
		ev.Channel.Type = "system"
	}
	if in.dropPersonal {
		ev.Channel.IP, ev.Channel.UserAgent = "", ""
	}
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		ev.Request.TraceID = sc.TraceID().String()
	}
	if req.ChangeReason != "" || req.ChangeTicket != "" {
		ev.Reason = &Reason{Text: req.ChangeReason, Ticket: req.ChangeTicket}
	}
	if in.rec.Target != nil {
		t := *in.rec.Target
		ev.Target = &t
	}
	if in.withChange {
		cs := &ChangeSet{BeforeVersion: in.rec.BeforeVersion, AfterVersion: in.rec.AfterVersion, Diff: []DiffEntry{}, Rows: []RowRef{}}
		for _, e := range diff.Compute(in.before, in.after) {
			cs.Diff = append(cs.Diff, DiffEntry{Op: e.Op, Path: e.Path, Before: e.Before, After: e.After})
		}
		for _, r := range in.rec.Rows {
			cs.Rows = append(cs.Rows, RowRef{Table: r.Table, Key: append([]string(nil), r.Key...)})
		}
		ev.Change = cs
	}
	return ev, nil
}

func (a *Auditor) actor(ctx context.Context, req scope.Request) Actor {
	act, ok := scope.ActorFrom(ctx)
	if !ok {
		return Actor{Type: string(scope.ActorSystem), ID: a.origin.Service, ClientID: req.ClientID}
	}
	return Actor{
		Type:           string(act.Type),
		ID:             act.ID,
		SessionID:      act.SessionID,
		ACR:            act.AuthACR,
		AMR:            append([]string(nil), act.AuthAMR...),
		ClientID:       req.ClientID,
		OnBehalfOf:     act.OnBehalfOf,
		ImpersonatedBy: act.ImpersonatedBy,
	}
}

// addKeyRows declares rows written by the key store in ev.
func addKeyRows(ev *Event, rows []keystore.RowRef) {
	if len(rows) == 0 {
		return
	}
	if ev.Change == nil {
		ev.Change = &ChangeSet{Diff: []DiffEntry{}, Rows: []RowRef{}}
	}
	for _, r := range rows {
		ev.Change.Rows = append(ev.Change.Rows, RowRef{Table: r.Table, Key: append([]string(nil), r.Key...)})
	}
}

// protect applies the classification: diff values and details by the
// type's declarations, the channel's IP and user agent as personal data of
// the actor.
func (a *Auditor) protect(ev *Event, in input, keys keyFunc) error {
	reg, err := a.reg.mustLookup(ev.Type)
	if err != nil {
		return err
	}
	fpKey, err := crypto.DeriveKey(a.fpRoot.Reveal(), nil, fingerprintInfo+tenantLabel(ev.TenantID), crypto.MinMACKeySize)
	if err != nil {
		return err //nolint:wrapcheck // already an *errs.Error
	}
	defer clear(fpKey)
	fingerprint := func(v []byte) (string, error) {
		mac, err := crypto.HMACSHA256(fpKey, v)
		if err != nil {
			return "", err //nolint:wrapcheck // already an *errs.Error
		}
		return classify.FormatFingerprint(mac), nil
	}
	protector := func(subject string) classify.Protector {
		return classify.Protector{
			Fingerprint: fingerprint,
			Encrypt: func(pt []byte, ptr string) (string, error) {
				k, err := keys(subject)
				if err != nil {
					return "", err
				}
				ct, err := k.DEK.Seal(pt, classify.PIIAAD(ev.ID.String(), ptr))
				if err != nil {
					return "", errs.Wrap(errs.CodeInternal, err, "audit: encrypt personal data")
				}
				return classify.FormatPII(k.ID.String(), ct), nil
			},
		}
	}
	actorSubject := ev.Actor.ID
	if actorSubject == "" {
		actorSubject = AnonymousSubject
	}
	dataSubject := in.rec.Subject
	if dataSubject == "" {
		dataSubject = actorSubject
	}
	if err := protectChannel(ev, protector(actorSubject)); err != nil {
		return err
	}
	dp := protector(dataSubject)
	if err := protectDiff(ev, reg, dp); err != nil {
		return err
	}
	return a.protectDetails(ev, in, reg, dp)
}

func protectDiff(ev *Event, reg *registered, p classify.Protector) error {
	if ev.Change == nil {
		return nil
	}
	var err error
	for i := range ev.Change.Diff {
		e := &ev.Change.Diff[i]
		base := "/change/diff/" + strconv.Itoa(i)
		if e.Before, err = reg.fields.Apply(e.Before, e.Path, base+"/before", p); err != nil {
			return protectErr(err)
		}
		if e.After, err = reg.fields.Apply(e.After, e.Path, base+"/after", p); err != nil {
			return protectErr(err)
		}
	}
	return nil
}

func protectChannel(ev *Event, p classify.Protector) error {
	for _, f := range []struct {
		ptr string
		v   *string
	}{{"/channel/ip", &ev.Channel.IP}, {"/channel/user_agent", &ev.Channel.UserAgent}} {
		if *f.v == "" {
			continue
		}
		out, err := classify.ProtectPersonal(*f.v, f.ptr, p)
		if err != nil {
			return protectErr(err)
		}
		s, _ := out.(string)
		*f.v = s
	}
	return nil
}

func (a *Auditor) protectDetails(ev *Event, in input, reg *registered, p classify.Protector) error {
	if len(in.rec.Details) == 0 {
		return nil
	}
	ev.Details = make(map[string]any, len(in.rec.Details))
	for k, v := range in.rec.Details {
		nv, err := diff.Normalize(v)
		if err != nil {
			return errs.Wrap(errs.CodeInternal, err, "audit: detail "+quote(k))
		}
		ptr := "/details/" + diff.Escape(k)
		switch reg.details[k] {
		case FieldSecret:
			nv, err = classify.ProtectSecret(nv, p)
		case FieldPersonal:
			nv, err = classify.ProtectPersonal(nv, ptr, p)
		case FieldPlain:
		}
		if err != nil {
			return protectErr(err)
		}
		ev.Details[k] = nv
	}
	return nil
}

func protectErr(err error) error {
	if errs.CodeOf(err) != errs.CodeInternal {
		return err
	}
	return errs.Wrap(errs.CodeInternal, err, "audit: protect record values")
}

func jsonMarshal(ev *Event) ([]byte, error) {
	b, err := json.Marshal(ev)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "audit: encode event")
	}
	return b, nil
}
