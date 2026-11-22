//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store/storetest"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// TestReadAPI exercises the store's read and maintenance functions.
//
// Covers: GOV-01, GOV-18
func TestReadAPI(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	a := store.NewAppender(nil)
	tn := storetest.NewTenant(t)
	ev := storetest.Event(t, tn, "widget.created")
	ev.Target = nil
	res := storetest.Append(t, env.DB, a, ev, storetest.Event(t, tn, "widget.updated"), storetest.Event(t, tn, "widget.deleted"))
	chain := tn.UUID()
	err := env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		head, hash, err := store.Head(ctx, tx, chain)
		if err != nil || head != 3 || hash != res[2].Hash {
			t.Errorf("Head = %d %s %v", head, hash, err)
		}
		if head, hash, err := store.Head(ctx, tx, uuid.New()); err != nil || head != 0 || hash != store.GenesisHash {
			t.Errorf("Head of an empty chain = %d %s %v", head, hash, err)
		}
		chains, err := store.Chains(ctx, tx)
		if err != nil || len(chains) != 1 || chains[0] != chain {
			t.Errorf("Chains = %v %v", chains, err)
		}
		var seqs []uint64
		if err := store.Events(ctx, tx, chain, 2, 0, func(e store.StoredEvent) error {
			seqs = append(seqs, e.Sequence)
			return nil
		}); err != nil || len(seqs) != 2 || seqs[0] != 2 {
			t.Errorf("Events = %v %v", seqs, err)
		}
		hashes, hs, err := store.EventHashes(ctx, tx, chain, 1, 3)
		if err != nil || len(hashes) != 3 || hs[2] != 3 || hashes[0] != res[0].Hash {
			t.Errorf("EventHashes = %v %v %v", hashes, hs, err)
		}
		if c, err := store.LastCheckpoint(ctx, tx, chain); err != nil || c != nil {
			t.Errorf("LastCheckpoint = %v %v", c, err)
		}
		if _, err := store.CheckpointCovering(ctx, tx, chain, 1); !errs.Is(err, errs.CodeNotFound) {
			t.Errorf("CheckpointCovering = %v", err)
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		cp := store.Checkpoint{
			Chain: chain, ID: uuid.New(), FromSeq: 1, ToSeq: 3, Root: res[0].Hash, Time: now, TreeHead: []byte("{}"),
			KeyRef: "k", KeyVersion: 1, Signature: []byte{1}, TSAToken: []byte{2}, TSATime: &now,
		}
		for i, want := range []bool{true, false} {
			if ok, err := store.InsertCheckpoint(ctx, tx, cp); err != nil || ok != want {
				t.Errorf("InsertCheckpoint #%d = %v %v", i, ok, err)
			}
			cp.ID = uuid.New()
		}
		last, err := store.LastCheckpoint(ctx, tx, chain)
		if err != nil || last == nil || last.ToSeq != 3 || !last.TSATime.Equal(now) {
			t.Errorf("LastCheckpoint = %+v %v", last, err)
		}
		if c, err := store.CheckpointCovering(ctx, tx, chain, 2); err != nil || c.FromSeq != 1 {
			t.Errorf("CheckpointCovering = %+v %v", c, err)
		}
		if cps, err := store.Checkpoints(ctx, tx, chain); err != nil || len(cps) != 1 {
			t.Errorf("Checkpoints = %v %v", cps, err)
		}
		if marks, err := store.RetentionMarks(ctx, tx, chain); err != nil || len(marks) != 0 {
			t.Errorf("RetentionMarks = %v %v", marks, err)
		}
		if err := store.PublishSigningKey(ctx, tx, store.SigningKey{KeyRef: "k", Version: 1, PublicKey: []byte{1}}); err != nil {
			return err
		}
		keys, err := store.SigningKeys(ctx, tx)
		if err != nil || len(keys) != 1 {
			t.Errorf("SigningKeys = %v %v", keys, err)
		}
		if _, err := keys[0].Parse(); !errs.Is(err, errs.CodeInvalidRequest) {
			t.Errorf("Parse of a bad key = %v", err)
		}
		for range 2 {
			if err := store.Reject(ctx, tx, "m1", "audit.x", 7, []byte("{"), "bad"); err != nil {
				return err
			}
		}
		return store.EnsurePartitions(ctx, tx, now.AddDate(1, 0, 0))
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := storetest.Count(t, env.DB, `SELECT count(*) FROM audit.rejected_messages`); n != 1 {
		t.Errorf("rejected = %d", n)
	}
	// Database errors carry errs codes.
	err = env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		_, err := store.Chains(ctx, tx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = env.DB.InPlatformTx(t.Context(), func(_ context.Context, tx pgx.Tx) error {
		_, _, err := store.Head(ctx, tx, chain)
		return err
	})
	if err == nil {
		t.Error("Head with a canceled context succeeded")
	}
}

// TestStatement checks the retention statement form.
//
// Covers: GOV-01
func TestStatement(t *testing.T) {
	t.Parallel()
	m := store.RetentionMark{
		Chain: uuid.Nil, FromSeq: 1, ThroughSeq: 5, LastHash: store.GenesisHash,
		RemovedAt: time.Date(2026, 11, 1, 0, 0, 0, 5, time.UTC),
	}
	b, err := store.StatementOf(m).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"chain":"00000000-0000-0000-0000-000000000000","from_seq":1,"kind":"portun.audit.retention.v1","last_hash":"` +
		store.GenesisHash + `","removed_at":"2026-11-01T00:00:00.000000005Z","through_seq":5}`
	if string(b) != want {
		t.Errorf("statement = %s", b)
	}
}
