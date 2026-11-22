//go:build integration

package retention_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/checkpoint"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/checkpoint/tsatest"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/retention"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store/storetest"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/verify"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/kms"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

type recorder struct {
	mu      sync.Mutex
	actions []string
	fail    bool
}

func (r *recorder) RecordRetention(_ context.Context, action string, _ any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("spool full")
	}
	r.actions = append(r.actions, action)
	return nil
}

type setup struct {
	env *dbtest.Env
	k   kms.KMS
	ref kms.KeyRef
	tsa *tsatest.TSA
	tn  ids.TenantID
}

// newSetup stores 10 events 100 days old and 5 current ones, checkpointed
// in blocks of 5.
func newSetup(t *testing.T) *setup {
	t.Helper()
	s := &setup{env: dbtest.New(t), tsa: tsatest.Start(t), tn: storetest.NewTenant(t)}
	s.k, s.ref = storetest.SigningKey(t)
	old := store.NewAppender(func() time.Time { return time.Now().AddDate(0, 0, -100) })
	for range 10 {
		storetest.Append(t, s.env.DB, old, storetest.Event(t, s.tn, "widget.created"))
	}
	cur := store.NewAppender(nil)
	for range 5 {
		storetest.Append(t, s.env.DB, cur, storetest.Event(t, s.tn, "widget.updated"))
	}
	cp, err := checkpoint.New(checkpoint.Config{Logger: storetest.Logger(), DB: s.env.DB, KMS: s.k, Key: s.ref, TSA: checkpoint.HTTPStamper{URL: s.tsa.URL}, MaxRange: 5})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := cp.RunOnce(t.Context()); err != nil || n != 3 {
		t.Fatalf("checkpoints = %d, %v", n, err)
	}
	return s
}

func (s *setup) service(t *testing.T, rec retention.Recorder) *retention.Service {
	t.Helper()
	svc, err := retention.New(retention.Config{Logger: storetest.Logger(), DB: s.env.DB, KMS: s.k, Key: s.ref, Recorder: rec})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func (s *setup) verify(t *testing.T) *verify.Report {
	t.Helper()
	var rep *verify.Report
	err := s.env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rep, err = verify.Chain(ctx, verify.DBSource{Q: tx, Chain: s.tn.UUID()}, verify.Options{TSARoots: s.tsa.Roots, RequireTSA: true})
		return err
	}, db.RepeatableRead())
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestRetention removes the expired range under a signed mark; the chain
// still verifies, and the removal was recorded first.
//
// Covers: GOV-01, GOV-18
func TestRetention(t *testing.T) {
	t.Parallel()
	s := newSetup(t)
	rec := &recorder{}
	svc := s.service(t, rec)
	if err := svc.SetPolicy(t.Context(), retention.Policy{Chain: s.tn.UUID(), RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	marks, err := svc.ApplyAll(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(marks) != 1 || marks[0].FromSeq != 1 || marks[0].ThroughSeq != 10 {
		t.Fatalf("marks = %+v", marks)
	}
	if n := storetest.Count(t, s.env.DB, `SELECT count(*) FROM audit.events WHERE tenant_id = $1`, s.tn.UUID()); n != 5 {
		t.Fatalf("events left = %d, want 5", n)
	}
	rep := s.verify(t)
	if !rep.OK() || rep.FromSeq != 11 || rep.Events != 5 || rep.RetentionMarks != 1 {
		t.Fatalf("verify after retention = %+v", rep)
	}
	if m, err := svc.Apply(t.Context(), s.tn.UUID()); err != nil || m != nil {
		t.Errorf("second Apply = %+v, %v", m, err)
	}
	if len(rec.actions) != 2 || rec.actions[0] != retention.ActionPolicyChanged || rec.actions[1] != retention.ActionApplied {
		t.Errorf("recorded = %v", rec.actions)
	}
	// The removal path cannot be abused to skip a range.
	err = s.env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT audit.apply_retention($1, gen_random_uuid(), 12, 15, 'x', now(), '\x00', 'k', 1, '\x00')`, s.tn.UUID())
		return err
	})
	if err == nil {
		t.Error("apply_retention accepted a non-contiguous range")
	}
}

// TestRetentionBlocked checks the legal hold and the fail-closed recording.
//
// Covers: GOV-01
func TestRetentionBlocked(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		policy retention.Policy
		fail   bool
		code   errs.Code
	}{
		{"legal hold", retention.Policy{RetentionDays: 30, LegalHold: true}, false, errs.CodePreconditionFailed},
		{"recording fails", retention.Policy{RetentionDays: 30}, true, errs.CodeUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newSetup(t)
			rec := &recorder{}
			svc := s.service(t, rec)
			p := tt.policy
			p.Chain = s.tn.UUID()
			if err := svc.SetPolicy(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			rec.fail = tt.fail
			if _, err := svc.Apply(t.Context(), s.tn.UUID()); !errs.Is(err, tt.code) {
				t.Fatalf("Apply = %v, want %s", err, tt.code)
			}
			if n := storetest.Count(t, s.env.DB, `SELECT count(*) FROM audit.events WHERE tenant_id = $1`, s.tn.UUID()); n != 15 {
				t.Errorf("events = %d, want 15", n)
			}
			if rep := s.verify(t); !rep.OK() {
				t.Errorf("verify = %+v", rep)
			}
		})
	}
	// The database refuses a removal under legal hold even if asked directly.
	s := newSetup(t)
	svc := s.service(t, &recorder{})
	if err := svc.SetPolicy(t.Context(), retention.Policy{Chain: s.tn.UUID(), LegalHold: true}); err != nil {
		t.Fatal(err)
	}
	err := s.env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		var h string
		if err := tx.QueryRow(ctx, `SELECT hash FROM audit.events WHERE tenant_id = $1 AND sequence = 5`, s.tn.UUID()).Scan(&h); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `SELECT audit.apply_retention($1, gen_random_uuid(), 1, 5, $2, now(), '\x00', 'k', 1, '\x00')`, s.tn.UUID(), h)
		return err
	})
	if err == nil {
		t.Error("apply_retention ignored the legal hold")
	}
}
