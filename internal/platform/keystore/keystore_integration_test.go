//go:build integration

package keystore_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/keystore"
	"github.com/antonyrejith/portun/internal/platform/kms"
	"github.com/antonyrejith/portun/internal/platform/kms/local"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(m, dbtest.Options{Sets: []migrate.Set{keystore.Migrations()}}))
}

type fixture struct {
	db    *db.DB
	store *keystore.Store
	kms   *local.KMS
}

func setup(t *testing.T, cfg keystore.Config, tenants ...ids.TenantID) fixture {
	t.Helper()
	env := dbtest.New(t)
	k, err := local.NewMemory()
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{db: env.DB, store: keystore.New(k, cfg), kms: k}
	for _, tn := range tenants {
		err := env.DB.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
			_, rows, err := f.store.CreateTenantKEK(ctx, tx, tn)
			if err == nil && (len(rows) != 1 || rows[0].Table != keystore.TableTenantKEKs) {
				t.Errorf("CreateTenantKEK rows = %+v", rows)
			}
			return err
		})
		if err != nil {
			t.Fatalf("CreateTenantKEK: %v", err)
		}
	}
	return f
}

func newTenant(t *testing.T) ids.TenantID {
	t.Helper()
	id, err := ids.New[ids.TenantKind]()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func inTenant(t *testing.T, f fixture, tn ids.TenantID, fn func(ctx context.Context, tx pgx.Tx) error) error {
	t.Helper()
	return f.db.InTenantTx(scope.WithTenant(t.Context(), scope.Tenant{TenantID: tn}), fn)
}

func subjectKey(t *testing.T, f fixture, tn ids.TenantID, subject string) keystore.Key {
	t.Helper()
	var k keystore.Key
	err := inTenant(t, f, tn, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		k, err = f.store.SubjectKey(ctx, tx, tn, subject)
		return err
	})
	if err != nil {
		t.Fatalf("SubjectKey: %v", err)
	}
	return k
}

// TestSubjectKeyLifecycle: get-or-create returns one key per subject,
// declares the row it created, decrypts by ID, and after ShredSubject the key
// is gone while other subjects' keys stay usable.
//
// Covers: SEC-08
func TestSubjectKeyLifecycle(t *testing.T) {
	t.Parallel()
	tn := newTenant(t)
	f := setup(t, keystore.Config{}, tn)
	k1 := subjectKey(t, f, tn, "usr_a")
	if len(k1.Rows) != 1 || k1.Rows[0].Table != keystore.TableDataKeys {
		t.Fatalf("created key rows = %+v", k1.Rows)
	}
	k2 := subjectKey(t, f, tn, "usr_a")
	if k2.ID != k1.ID || len(k2.Rows) != 0 {
		t.Fatalf("second SubjectKey = %v rows %v, want existing %v", k2.ID, k2.Rows, k1.ID)
	}
	other := subjectKey(t, f, tn, "usr_b")
	ct, err := k1.DEK.Seal([]byte("alice@example.com"), []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	ctB, err := other.DEK.Seal([]byte("bob"), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = inTenant(t, f, tn, func(ctx context.Context, tx pgx.Tx) error {
		dek, err := f.store.DataKey(ctx, tx, tn, k1.ID)
		if err != nil {
			return err
		}
		pt, err := dek.Open(ct, []byte("aad"))
		if err == nil && string(pt) != "alice@example.com" {
			t.Errorf("decrypted %q", pt)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	err = inTenant(t, f, tn, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := f.store.ShredSubject(ctx, tx, tn, "usr_a")
		if err == nil && len(rows) != 1 {
			t.Errorf("shred rows = %+v", rows)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = inTenant(t, f, tn, func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.store.DataKey(ctx, tx, tn, k1.ID)
		return err
	})
	if !errors.Is(err, keystore.ErrShredded) || !errs.Is(err, errs.CodeNotFound) {
		t.Errorf("DataKey after shred = %v, want ErrShredded", err)
	}
	err = inTenant(t, f, tn, func(ctx context.Context, tx pgx.Tx) error {
		dek, err := f.store.DataKey(ctx, tx, tn, other.ID)
		if err != nil {
			return err
		}
		_, err = dek.Open(ctB, nil)
		return err
	})
	if err != nil {
		t.Errorf("other subject's key after shred: %v", err)
	}
	k3 := subjectKey(t, f, tn, "usr_a")
	if k3.ID == k1.ID || len(k3.Rows) != 1 {
		t.Errorf("after shred SubjectKey returned %v rows %v, want a new key", k3.ID, k3.Rows)
	}
	err = inTenant(t, f, tn, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := f.store.ShredSubject(ctx, tx, tn, "usr_never")
		if err == nil && rows != nil {
			t.Errorf("shredding a subject without key wrote %v", rows)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestConcurrentCreate: concurrent first uses of a subject agree on one key.
//
// Covers: SEC-08
func TestConcurrentCreate(t *testing.T) {
	t.Parallel()
	tn := newTenant(t)
	f := setup(t, keystore.Config{}, tn)
	const n = 4
	got := make([]ids.KeyID, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			err := inTenant(t, f, tn, func(ctx context.Context, tx pgx.Tx) error {
				k, err := f.store.SubjectKey(ctx, tx, tn, "usr_same")
				got[i] = k.ID
				return err
			})
			if err != nil {
				t.Errorf("SubjectKey: %v", err)
			}
		})
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		if got[i] != got[0] {
			t.Errorf("concurrent keys differ: %v", got)
		}
	}
}

// TestKeyIsolation: row-level security follows the conventions; one tenant
// cannot read or use another tenant's keys.
//
// Covers: TEN-01, SEC-08
func TestKeyIsolation(t *testing.T) {
	t.Parallel()
	a, b := newTenant(t), newTenant(t)
	f := setup(t, keystore.Config{}, a, b)
	env := dbtest.New(t)
	dbtest.AssertRLS(t, env, keystore.Schema)
	kb := subjectKey(t, f, b, "usr_b")
	cases := []struct {
		name string
		fn   func(ctx context.Context, tx pgx.Tx) error
	}{
		{"read data key by ID", func(ctx context.Context, tx pgx.Tx) error {
			_, err := f.store.DataKey(ctx, tx, b, kb.ID)
			return err
		}},
		{"create key for other tenant", func(ctx context.Context, tx pgx.Tx) error {
			_, err := f.store.SubjectKey(ctx, tx, b, "usr_x")
			return err
		}},
		{"shred other tenant", func(ctx context.Context, tx pgx.Tx) error {
			rows, err := f.store.ShredSubject(ctx, tx, b, "usr_b")
			if err == nil && len(rows) > 0 {
				return errors.New("shredded another tenant's key")
			}
			return errors.New("no rows visible (expected)")
		}},
		{"see other tenant rows", func(ctx context.Context, tx pgx.Tx) error {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM keys.data_keys WHERE tenant_id = $1`, b.UUID()).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return nil // visible: failure
			}
			return errors.New("not visible (expected)")
		}},
		{"create tenant KEK as application", func(ctx context.Context, tx pgx.Tx) error {
			_, _, err := f.store.CreateTenantKEK(ctx, tx, a)
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if err := inTenant(t, f, a, c.fn); err == nil {
				t.Errorf("%s succeeded from tenant A", c.name)
			}
		})
	}
	// B's key is still intact.
	err := inTenant(t, f, b, func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.store.DataKey(ctx, tx, b, kb.ID)
		return err
	})
	if err != nil {
		t.Errorf("tenant B key: %v", err)
	}
}

// TestTenantKEK: a tenant without a KEK cannot create data keys; destroying
// the KEK makes every data key of the tenant unusable.
//
// Covers: SEC-08
func TestTenantKEK(t *testing.T) {
	t.Parallel()
	tn, bare := newTenant(t), newTenant(t)
	f := setup(t, keystore.Config{CacheSize: 1}, tn)
	err := inTenant(t, f, bare, func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.store.SubjectKey(ctx, tx, bare, "usr_a")
		return err
	})
	if !errs.Is(err, errs.CodeInternal) {
		t.Errorf("SubjectKey without KEK = %v", err)
	}
	err = f.db.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		_, _, err := f.store.CreateTenantKEK(ctx, tx, tn)
		return err
	})
	if !errs.Is(err, errs.CodeConflict) {
		t.Errorf("second CreateTenantKEK = %v, want conflict", err)
	}
	k := subjectKey(t, f, tn, "usr_a")
	_ = subjectKey(t, f, tn, "usr_b") // evicts usr_a from the one-entry cache
	err = f.db.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		rows, err := f.store.DestroyTenantKEK(ctx, tx, tn)
		if err == nil && len(rows) != 1 {
			t.Errorf("destroy rows = %+v", rows)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = inTenant(t, f, tn, func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.store.DataKey(ctx, tx, tn, k.ID)
		return err
	})
	if !errors.Is(err, kms.ErrKeyDestroyed) {
		t.Errorf("DataKey after KEK destruction = %v, want ErrKeyDestroyed", err)
	}
	err = f.db.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.store.DestroyTenantKEK(ctx, tx, tn)
		return err
	})
	if !errs.Is(err, errs.CodeNotFound) {
		t.Errorf("second DestroyTenantKEK = %v", err)
	}
}

// TestPlatformKeys: the platform's keys (zero tenant) work through the
// platform role.
//
// Covers: SEC-08
func TestPlatformKeys(t *testing.T) {
	t.Parallel()
	f := setup(t, keystore.Config{}, ids.TenantID{})
	var k keystore.Key
	err := f.db.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		k, err = f.store.SubjectKey(ctx, tx, ids.TenantID{}, "op_1")
		return err
	})
	if err != nil || len(k.Rows) != 1 {
		t.Fatalf("platform SubjectKey = %+v, %v", k.Rows, err)
	}
	if c, ok := f.store.CachedSubjectKey(ids.TenantID{}, "op_1"); ok {
		t.Errorf("a key created by the transaction was cached: %v", c.ID)
	}
	err = f.db.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		_, found, err := f.store.LookupSubjectKey(ctx, tx, ids.TenantID{}, "op_1")
		if err == nil && !found {
			t.Error("committed key not found")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := f.store.CachedSubjectKey(ids.TenantID{}, "op_1"); !ok || c.ID != k.ID {
		t.Error("committed key not cached after lookup")
	}
}
