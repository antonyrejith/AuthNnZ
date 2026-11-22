//go:build integration

package audit_test

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/keystore"
	"github.com/antonyrejith/portun/internal/platform/kms/local"
	"github.com/antonyrejith/portun/internal/platform/outbox"
	"github.com/antonyrejith/portun/internal/platform/scope"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

//go:embed testdata/app/*.sql
var appMigrations embed.FS

func TestMain(m *testing.M) {
	sub, err := fs.Sub(appMigrations, "testdata/app")
	if err != nil {
		panic(err)
	}
	os.Exit(dbtest.Main(m, dbtest.Options{Sets: []migrate.Set{
		outbox.Migrations(), keystore.Migrations(), {Schema: "audittest", FS: sub},
	}}))
}

const (
	secretValue = "s3cr3t-value-never-stored"
	emailValue  = "alice@example.com"
	clientIP    = "203.0.113.24"
	userAgent   = "Mozilla/5.0 (TestAgent)"
)

// capture is a spool publisher that keeps what it receives.
type capture struct {
	mu   sync.Mutex
	msgs []bus.Message
}

func (c *capture) Publish(_ context.Context, m bus.Message) (bus.PubAck, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, m)
	return bus.PubAck{Stream: bus.StreamAudit, Sequence: uint64(len(c.msgs))}, nil
}

func (c *capture) events(t *testing.T) []audit.Event {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]audit.Event, 0, len(c.msgs))
	for _, m := range c.msgs {
		var ev audit.Event
		if err := json.Unmarshal(m.Data, &ev); err != nil {
			t.Fatal(err)
		}
		if m.ID != ev.ID.UUID().String() {
			t.Errorf("message ID %s is not the event ID %s", m.ID, ev.ID.UUID())
		}
		out = append(out, ev)
	}
	return out
}

type fixture struct {
	env    *dbtest.Env
	a      *audit.Auditor
	keys   *keystore.Store
	spool  *audit.Spool
	pub    *capture
	tenant ids.TenantID
}

func setup(t *testing.T, pub audit.Publisher) *fixture {
	t.Helper()
	env := dbtest.New(t)
	k, err := local.NewMemory()
	if err != nil {
		t.Fatal(err)
	}
	keys := keystore.New(k, keystore.Config{})
	tn, err := ids.New[ids.TenantKind]()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []ids.TenantID{tn, {}} {
		if err := env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
			_, _, err := keys.CreateTenantKEK(ctx, tx, id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	c := &capture{}
	if pub == nil {
		pub = c
	}
	sp, err := audit.OpenSpool(audit.SpoolConfig{Dir: t.TempDir()}, pub, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sp.Close() })
	reg := audit.NewRegistry()
	for _, et := range []audit.EventType{
		{Name: "widget.created", Category: audit.CategoryData, OCSFClassUID: 3004, Fields: widgetFields()},
		{Name: "widget.updated", Category: audit.CategoryData, OCSFClassUID: 3004, Fields: widgetFields()},
		{Name: "user.erased", Category: audit.CategoryData, OCSFClassUID: 3001},
		{Name: "tenant.created", Category: audit.CategoryPlatform, OCSFClassUID: 3004},
		{
			Name: "login.failed", Category: audit.CategoryAuthentication, OCSFClassUID: 3002,
			Details: map[string]audit.FieldClass{"username": audit.FieldPersonal, "reason": audit.FieldPlain, "otp": audit.FieldSecret},
		},
	} {
		if err := reg.Register(et); err != nil {
			t.Fatal(err)
		}
	}
	root := make([]byte, 32)
	copy(root, "fingerprint-root-for-tests-only!")
	a, err := audit.New(audit.Deps{
		DB: env.DB, Registry: reg, Keystore: keys, Spool: sp,
		FingerprintRoot: secret.NewBytes(root), Origin: audit.Origin{Service: "portun-test", Region: "eu"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{env: env, a: a, keys: keys, spool: sp, pub: c, tenant: tn}
}

func widgetFields() map[string]audit.FieldClass {
	return map[string]audit.FieldClass{
		"/owner_email": audit.FieldPersonal,
		"/api_secret":  audit.FieldSecret,
		"/secrets/*":   audit.FieldSecret,
	}
}

// ctx returns a context with the fixture's tenant, an admin actor and a
// console request.
func (f *fixture) ctx(t *testing.T) context.Context {
	t.Helper()
	ctx := scope.WithTenant(t.Context(), scope.Tenant{TenantID: f.tenant})
	ctx = scope.WithActor(ctx, scope.Actor{Type: scope.ActorAdmin, ID: "usr_admin", SessionID: "ses_1", AuthAMR: []string{"hwk"}})
	return scope.WithRequest(ctx, scope.Request{
		RequestID: "req_1", Channel: "console", IP: clientIP, UserAgent: userAgent,
		Operation: "CreateWidget", ChangeReason: "testing", ChangeTicket: "SEC-1",
	})
}

type widget struct {
	Name       string            `json:"name"`
	OwnerEmail string            `json:"owner_email,omitempty"`
	APISecret  string            `json:"api_secret,omitempty"`
	Secrets    map[string]string `json:"secrets,omitempty"`
}

type outboxRow struct {
	tenant  uuid.UUID
	id      uuid.UUID
	subject string
	payload string
	xid     string
}

// outbox returns the outbox rows except data key creations (written by the
// failure and Emit paths in their own changes).
func (f *fixture) outbox(t *testing.T) []outboxRow {
	t.Helper()
	var out []outboxRow
	err := f.env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT tenant_id, id, subject, payload::text, xid::text FROM outbox.messages
			WHERE payload->>'type' <> $1 ORDER BY created_at, id`, audit.TypeDataKeyCreated)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r outboxRow
			if err := rows.Scan(&r.tenant, &r.id, &r.subject, &r.payload, &r.xid); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func decode(t *testing.T, r outboxRow) audit.Event {
	t.Helper()
	var ev audit.Event
	if err := json.Unmarshal([]byte(r.payload), &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func (f *fixture) widgets(t *testing.T) int {
	t.Helper()
	var n int
	err := f.env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audittest.widgets`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func insertWidget(ctx context.Context, tx *audit.Tx, tenant ids.TenantID, w widget) (audit.RowRef, error) {
	id := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, `INSERT INTO audittest.widgets (tenant_id, id, name) VALUES ($1, $2, $3)`, tenant.UUID(), id, w.Name); err != nil {
		return audit.RowRef{}, err
	}
	return audit.RowRef{Table: "audittest.widgets", Key: []string{tenant.UUID().String(), id.String()}}, nil
}

// TestChangeOutcomes: a change commits with its outbox record; a failing fn
// writes nothing and spools a failure event; a transaction without a record
// or with an unregistered type is rolled back.
//
// Covers: GOV-11, GOV-01
func TestChangeOutcomes(t *testing.T) {
	t.Parallel()
	boom := errs.New(errs.CodeConflict, "widget exists")
	cases := []struct {
		name        string
		fn          func(f *fixture) func(ctx context.Context, tx *audit.Tx) error
		wantCode    errs.Code
		wantRows    int
		failureType string
	}{
		{"success", func(f *fixture) func(context.Context, *audit.Tx) error {
			return func(ctx context.Context, tx *audit.Tx) error {
				row, err := insertWidget(ctx, tx, f.tenant, widget{Name: "w"})
				if err != nil {
					return err
				}
				return tx.Record(audit.Record{Type: "widget.created", Target: &audit.Target{Type: "widget", ID: row.Key[1]}, After: widget{Name: "w"}, Rows: []audit.RowRef{row}})
			}
		}, "", 1, ""},
		{"fn error after record", func(f *fixture) func(context.Context, *audit.Tx) error {
			return func(ctx context.Context, tx *audit.Tx) error {
				row, err := insertWidget(ctx, tx, f.tenant, widget{Name: "w"})
				if err != nil {
					return err
				}
				if err := tx.Record(audit.Record{Type: "widget.created", Target: &audit.Target{Type: "widget", ID: "x"}, Rows: []audit.RowRef{row}}); err != nil {
					return err
				}
				return boom
			}
		}, errs.CodeConflict, 0, "widget.created"},
		{"fn error before record", func(_ *fixture) func(context.Context, *audit.Tx) error {
			return func(context.Context, *audit.Tx) error { return boom }
		}, errs.CodeConflict, 0, audit.TypeTransactionFailed},
		{"no record", func(f *fixture) func(context.Context, *audit.Tx) error {
			return func(ctx context.Context, tx *audit.Tx) error {
				_, err := insertWidget(ctx, tx, f.tenant, widget{Name: "w"})
				return err
			}
		}, errs.CodeInternal, 0, audit.TypeTransactionFailed},
		{"unregistered type ignored by caller", func(f *fixture) func(context.Context, *audit.Tx) error {
			return func(ctx context.Context, tx *audit.Tx) error {
				_, _ = insertWidget(ctx, tx, f.tenant, widget{Name: "w"})
				_ = tx.Record(audit.Record{Type: "widget.vanished"})
				return nil
			}
		}, errs.CodeInternal, 0, audit.TypeTransactionFailed},
		{"undeclared detail", func(_ *fixture) func(context.Context, *audit.Tx) error {
			return func(_ context.Context, tx *audit.Tx) error {
				return tx.Record(audit.Record{Type: "widget.created", Details: map[string]any{"color": "red"}})
			}
		}, errs.CodeInternal, 0, "widget.created"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := setup(t, nil)
			err := f.a.Change(f.ctx(t), c.fn(f))
			if errs.CodeOf(err) != c.wantCode {
				t.Fatalf("Change = %v, want code %q", err, c.wantCode)
			}
			if c.wantCode == errs.CodeConflict && !errors.Is(err, boom) {
				t.Errorf("Change did not return the original error: %v", err)
			}
			if got := f.widgets(t); got != c.wantRows {
				t.Errorf("widgets = %d, want %d", got, c.wantRows)
			}
			rows := f.outbox(t)
			if c.wantCode == "" {
				if len(rows) != 1 || rows[0].tenant != f.tenant.UUID() {
					t.Fatalf("outbox rows = %+v", rows)
				}
				ev := decode(t, rows[0])
				if ev.Outcome != audit.OutcomeSuccess || ev.Type != "widget.created" || len(ev.Change.Rows) < 1 {
					t.Errorf("event = %+v", ev)
				}
			} else if len(rows) != 0 {
				t.Errorf("failed change left %d outbox rows", len(rows))
			}
			if _, err := f.spool.ShipOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			var failures []audit.Event
			for _, ev := range f.pub.events(t) {
				if ev.Outcome == audit.OutcomeFailure {
					failures = append(failures, ev)
				}
			}
			if c.failureType == "" {
				if len(failures) != 0 {
					t.Errorf("unexpected failure events %+v", failures)
				}
				return
			}
			if len(failures) != 1 || failures[0].Type != c.failureType || failures[0].ErrorCode != c.wantCode ||
				failures[0].Actor.ID != "usr_admin" || failures[0].Request.Operation != "CreateWidget" || failures[0].TenantID != f.tenant {
				t.Errorf("failure events = %+v, want one %s with code %s", failures, c.failureType, c.wantCode)
			}
		})
	}
}

// TestRecordContent: the event carries scope, actor, channel, request,
// reason, diff with both values, versions, rows and the transaction ID of
// the outbox row; several records share one transaction.
//
// Covers: GOV-01, GOV-11
func TestRecordContent(t *testing.T) {
	t.Parallel()
	f := setup(t, nil)
	var xid string
	err := f.a.Change(f.ctx(t), func(ctx context.Context, tx *audit.Tx) error {
		r1, err := insertWidget(ctx, tx, f.tenant, widget{Name: "one"})
		if err != nil {
			return err
		}
		r2, err := insertWidget(ctx, tx, f.tenant, widget{Name: "two"})
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT pg_current_xact_id()::text`).Scan(&xid); err != nil {
			return err
		}
		if err := tx.Record(audit.Record{
			Type: "widget.created", Target: &audit.Target{Type: "widget", ID: "1", Name: "one"},
			After: widget{Name: "one"}, AfterVersion: 1, Rows: []audit.RowRef{r1},
		}); err != nil {
			return err
		}
		return tx.Record(audit.Record{
			Type: "widget.updated", Target: &audit.Target{Type: "widget", ID: "2"},
			Before: widget{Name: "two"}, After: widget{Name: "deux"}, BeforeVersion: 1, AfterVersion: 2, Rows: []audit.RowRef{r2},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := f.outbox(t)
	if len(rows) != 2 {
		t.Fatalf("outbox rows = %d, want 2", len(rows))
	}
	subject, _ := bus.AuditSubject(f.tenant)
	for _, r := range rows {
		ev := decode(t, r)
		if ev.Request.DBXID != r.xid || r.xid != xid {
			t.Errorf("db_xid %q, row xid %q, transaction xid %q", ev.Request.DBXID, r.xid, xid)
		}
		if r.subject != subject || r.id != ev.ID.UUID() {
			t.Errorf("row subject %s id %s for event %s", r.subject, r.id, ev.ID)
		}
		if ev.Actor.Type != "admin" || ev.Actor.SessionID != "ses_1" || ev.Channel.Type != "console" ||
			ev.Request.RequestID != "req_1" || ev.Reason == nil || ev.Reason.Ticket != "SEC-1" ||
			ev.Origin.Service != "portun-test" || ev.Category != audit.CategoryData || ev.Time.Location() != time.UTC ||
			ev.Time.Nanosecond()%int(time.Millisecond) != 0 {
			t.Errorf("event envelope = %+v", ev)
		}
		if !strings.HasPrefix(ev.Channel.IP, "pii:v1:key_") || !strings.HasPrefix(ev.Channel.UserAgent, "pii:v1:key_") {
			t.Errorf("channel not encrypted: %+v", ev.Channel)
		}
	}
	upd := decode(t, rows[1])
	if upd.Type == "widget.created" {
		upd = decode(t, rows[0])
	}
	want := audit.DiffEntry{Op: "replace", Path: "/name", Before: "two", After: "deux"}
	if len(upd.Change.Diff) != 1 || upd.Change.Diff[0] != want || upd.Change.BeforeVersion != 1 || upd.Change.AfterVersion != 2 {
		t.Errorf("update change = %+v", upd.Change)
	}
	// The actor's data key was created in this transaction and declared.
	declared := 0
	for _, r := range rows {
		for _, ref := range decode(t, r).Change.Rows {
			if ref.Table == keystore.TableDataKeys {
				declared++
			}
		}
	}
	if declared != 1 {
		t.Errorf("data key rows declared %d times, want once", declared)
	}
}

// TestPlatformChange: platform records go to the platform chain, or to a
// tenant's chain when the record names the tenant.
//
// Covers: GOV-11, TEN-01
func TestPlatformChange(t *testing.T) {
	t.Parallel()
	f := setup(t, nil)
	ctx := scope.WithActor(t.Context(), scope.Actor{Type: scope.ActorPlatformOperator, ID: "op_1"})
	err := f.a.PlatformChange(ctx, func(_ context.Context, tx *audit.Tx) error {
		if err := tx.Record(audit.Record{Type: "tenant.created", Target: &audit.Target{Type: "tenant", ID: f.tenant.String()}}); err != nil {
			return err
		}
		return tx.Record(audit.Record{Type: "widget.created", TenantID: f.tenant})
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := f.outbox(t)
	tenantSubject, _ := bus.AuditSubject(f.tenant)
	got := map[string]uuid.UUID{}
	for _, r := range rows {
		got[r.subject] = r.tenant
	}
	if len(rows) != 2 || got[bus.AuditPlatformSubject] != uuid.Nil || got[tenantSubject] != f.tenant.UUID() {
		t.Errorf("platform change rows = %+v", rows)
	}
	err = f.a.Change(f.ctx(t), func(_ context.Context, tx *audit.Tx) error {
		other, _ := ids.New[ids.TenantKind]()
		return tx.Record(audit.Record{Type: "widget.created", TenantID: other})
	})
	if !errs.Is(err, errs.CodeInternal) {
		t.Errorf("tenant change recording for another tenant = %v", err)
	}
	if err := f.a.Change(t.Context(), func(context.Context, *audit.Tx) error { return nil }); !errs.Is(err, errs.CodeInternal) {
		t.Errorf("Change without tenant scope = %v", err)
	}
}

// TestProtection: secrets never reach the outbox; personal data is
// encrypted under the subject's key and decryptable; after the subject is
// shredded it is unreadable while the rest of the record, including the
// actor's personal data, stays intact.
//
// Covers: SEC-08, GOV-01, GOV-11
func TestProtection(t *testing.T) {
	t.Parallel()
	f := setup(t, nil)
	const subject = "usr_alice"
	err := f.a.Change(f.ctx(t), func(_ context.Context, tx *audit.Tx) error {
		return tx.Record(audit.Record{
			Type:    "widget.updated",
			Subject: subject,
			Before:  widget{Name: "w", APISecret: "old-secret", OwnerEmail: "old@example.com"},
			After:   widget{Name: "w2", APISecret: secretValue, OwnerEmail: emailValue, Secrets: map[string]string{"k": secretValue + "-2"}},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := f.outbox(t)
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	raw := rows[0].payload
	for _, leak := range []string{secretValue, "old-secret", emailValue, "old@example.com", clientIP, userAgent} {
		if strings.Contains(raw, leak) {
			t.Errorf("outbox payload contains %q", leak)
		}
	}
	ev := decode(t, rows[0])
	byPath := map[string]int{}
	for i, d := range ev.Change.Diff {
		byPath[d.Path] = i
	}
	sec := ev.Change.Diff[byPath["/api_secret"]]
	secAfter, _ := sec.After.(map[string]any)
	if secAfter["redacted"] != "[REDACTED]" || !strings.HasPrefix(secAfter["fingerprint"].(string), "sha256:") ||
		len(secAfter["fingerprint"].(string)) != len("sha256:")+16 {
		t.Errorf("secret entry = %+v", sec)
	}
	nested := ev.Change.Diff[byPath["/secrets"]].After.(map[string]any)["k"].(map[string]any)
	if nested["redacted"] != "[REDACTED]" {
		t.Errorf("nested secret = %+v", nested)
	}
	emailIdx := byPath["/owner_email"]
	email := ev.Change.Diff[emailIdx]
	afterPtr := "/change/diff/" + strconv.Itoa(emailIdx) + "/after"

	reveal := func(ptr, value string) (any, error) {
		var out any
		err := f.env.DB.InTenantTx(f.ctx(t), func(ctx context.Context, tx pgx.Tx) error {
			var err error
			out, err = f.a.DecryptPersonal(ctx, tx, f.tenant, ev.ID, ptr, value)
			return err
		})
		return out, err
	}
	got, err := reveal(afterPtr, email.After.(string))
	if err != nil || got != emailValue {
		t.Fatalf("decrypt email = %v, %v", got, err)
	}
	if _, err := reveal("/change/diff/0/before", email.After.(string)); err == nil {
		t.Error("ciphertext decrypted at another pointer")
	}
	if ip, err := reveal("/channel/ip", ev.Channel.IP); err != nil || ip != clientIP {
		t.Fatalf("decrypt ip = %v, %v", ip, err)
	}

	// Same secret, same tenant: same fingerprint (detects reuse).
	err = f.a.Change(f.ctx(t), func(ctx context.Context, tx *audit.Tx) error {
		if err := tx.Record(audit.Record{Type: "widget.created", After: widget{Name: "z", APISecret: secretValue}}); err != nil {
			return err
		}
		rows, err := f.keys.ShredSubject(ctx, tx, f.tenant, subject)
		if err != nil {
			return err
		}
		refs := make([]audit.RowRef, 0, len(rows))
		for _, r := range rows {
			refs = append(refs, audit.RowRef{Table: r.Table, Key: r.Key})
		}
		return tx.Record(audit.Record{Type: "user.erased", Target: &audit.Target{Type: "user", ID: subject}, Rows: refs})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range f.outbox(t) {
		e := decode(t, r)
		if e.Type == "widget.created" && e.Change.Diff[0].Path == "/api_secret" {
			if fp := e.Change.Diff[0].After.(map[string]any)["fingerprint"]; fp != secAfter["fingerprint"] {
				t.Errorf("fingerprint of the same secret differs: %v vs %v", fp, secAfter["fingerprint"])
			}
		}
		if e.Type == "user.erased" && (len(e.Change.Rows) != 1 || e.Change.Rows[0].Table != keystore.TableDataKeys) {
			t.Errorf("erasure rows = %+v", e.Change.Rows)
		}
	}
	if _, err := reveal(afterPtr, email.After.(string)); !errors.Is(err, keystore.ErrShredded) {
		t.Errorf("decrypt after shred = %v, want ErrShredded", err)
	}
	if ip, err := reveal("/channel/ip", ev.Channel.IP); err != nil || ip != clientIP {
		t.Errorf("actor data after the subject's shred = %v, %v", ip, err)
	}
	if rows2 := f.outbox(t); rows2[0].payload != raw {
		t.Error("the stored record changed after shredding")
	}
}

// TestEmitThroughBus: Emit spools events outside transactions (creating the
// subject's key in its own audited change), and the spool ships them to the
// AUDIT stream of an embedded NATS with the event ID as message ID.
//
// Covers: GOV-11, GOV-01, SEC-08
func TestEmitThroughBus(t *testing.T) {
	t.Parallel()
	b, js := embeddedBus(t)
	f := setup(t, b)
	anon := scope.WithRequest(scope.WithTenant(t.Context(), scope.Tenant{TenantID: f.tenant}),
		scope.Request{Channel: "api", IP: clientIP, UserAgent: userAgent})
	err := f.a.Emit(anon, audit.Emitted{
		Type: "login.failed", Outcome: audit.OutcomeFailure, ErrorCode: errs.CodeUnauthenticated,
		Details: map[string]any{"username": emailValue, "reason": "bad_password", "otp": "123456"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.Emit(anon, audit.Emitted{Type: "nope.never"}); !errs.Is(err, errs.CodeInternal) {
		t.Errorf("Emit of an unregistered type = %v", err)
	}
	if err := f.a.Emit(t.Context(), audit.Emitted{Type: "tenant.created"}); err != nil {
		t.Errorf("platform Emit: %v", err)
	}
	if !f.spool.Pending() {
		t.Fatal("nothing spooled")
	}
	if n, err := f.spool.ShipOnce(t.Context()); err != nil || n != 2 {
		t.Fatalf("ShipOnce = %d, %v", n, err)
	}
	subject, _ := bus.AuditSubject(f.tenant)
	stream, err := js.Stream(t.Context(), bus.StreamAudit)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := stream.GetLastMsgForSubject(t.Context(), subject)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(msg.Data)
	for _, leak := range []string{emailValue, clientIP, userAgent, "123456"} {
		if strings.Contains(raw, leak) {
			t.Errorf("spooled event contains %q", leak)
		}
	}
	var ev audit.Event
	if err := json.Unmarshal(msg.Data, &ev); err != nil {
		t.Fatal(err)
	}
	if msg.Header.Get(nats.MsgIdHdr) != ev.ID.UUID().String() || ev.Outcome != audit.OutcomeFailure || ev.Details["reason"] != "bad_password" {
		t.Errorf("event = %+v header %v", ev, msg.Header)
	}
	if _, err := stream.GetLastMsgForSubject(t.Context(), bus.AuditPlatformSubject); err != nil {
		t.Errorf("platform event not shipped: %v", err)
	}
	// The anonymous subject's key was created in its own audited change.
	var created audit.Event
	err = f.env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		var payload string
		if err := tx.QueryRow(ctx, `SELECT payload::text FROM outbox.messages WHERE payload->>'type' = $1 AND tenant_id = $2`,
			audit.TypeDataKeyCreated, f.tenant.UUID()).Scan(&payload); err != nil {
			return err
		}
		return json.Unmarshal([]byte(payload), &created)
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Change == nil || len(created.Change.Rows) != 1 || created.Change.Rows[0].Table != keystore.TableDataKeys {
		t.Errorf("data key creation not recorded: %+v", created)
	}
	err = f.env.DB.InTenantTx(anon, func(ctx context.Context, tx pgx.Tx) error {
		v, err := f.a.DecryptPersonal(ctx, tx, f.tenant, ev.ID, "/details/username", ev.Details["username"].(string))
		if err == nil && v != emailValue {
			t.Errorf("username = %v", v)
		}
		return err
	})
	if err != nil {
		t.Error(err)
	}
}

func embeddedBus(t *testing.T) (*bus.Bus, jetstream.JetStream) {
	t.Helper()
	srv, err := bus.StartEmbedded(t.Context(), bus.EmbeddedConfig{StoreDir: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Shutdown)
	b, err := bus.Connect(t.Context(), bus.Config{URL: srv.ClientURL(), Credentials: srv.Token()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = b.Close(ctx)
	})
	if err := b.EnsureStreams(t.Context()); err != nil {
		t.Fatal(err)
	}
	nc, err := nats.Connect(srv.ClientURL(), nats.Token(srv.Token().Reveal()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	return b, js
}
