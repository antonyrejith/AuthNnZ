//go:build integration

package keystore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/keystore"
)

func lookup(t *testing.T, f fixture, tn ids.TenantID, subject string) keystore.Key {
	t.Helper()
	var k keystore.Key
	err := inTenant(t, f, tn, func(ctx context.Context, tx pgx.Tx) error {
		var found bool
		var err error
		k, found, err = f.store.LookupSubjectKey(ctx, tx, tn, subject)
		if err == nil && !found {
			t.Errorf("LookupSubjectKey(%s): not found", subject)
		}
		return err
	})
	if err != nil {
		t.Fatalf("LookupSubjectKey: %v", err)
	}
	return k
}

// TestDestroyKEKDropsOnlyThatTenantsCache: destroying a tenant's KEK removes
// the tenant's cached keys from the cache and leaves other tenants' entries.
//
// Covers: SEC-08
func TestDestroyKEKDropsOnlyThatTenantsCache(t *testing.T) {
	t.Parallel()
	a, b := newTenant(t), newTenant(t)
	f := setup(t, keystore.Config{}, a, b)
	subjectKey(t, f, a, "usr_a")
	subjectKey(t, f, b, "usr_b")
	ka, kb := lookup(t, f, a, "usr_a"), lookup(t, f, b, "usr_b") // committed lookups fill the cache
	if c, ok := f.store.CachedSubjectKey(a, "usr_a"); !ok || c.ID != ka.ID {
		t.Fatalf("tenant A key not cached: %v, %v", c.ID, ok)
	}
	err := f.db.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.store.DestroyTenantKEK(ctx, tx, a)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.store.CachedSubjectKey(a, "usr_a"); ok {
		t.Error("destroyed tenant's key still cached")
	}
	if c, ok := f.store.CachedSubjectKey(b, "usr_b"); !ok || c.ID != kb.ID {
		t.Error("other tenant's cache entry was dropped")
	}
}

// TestCacheTTLExpiry: a cached subject mapping stops being served for
// encryption once the TTL has passed, and a fresh lookup reads the row again.
//
// Covers: SEC-08
func TestCacheTTLExpiry(t *testing.T) {
	t.Parallel()
	tn := newTenant(t)
	f := setup(t, keystore.Config{CacheTTL: time.Nanosecond}, tn)
	k := subjectKey(t, f, tn, "usr_a")
	lookup(t, f, tn, "usr_a")
	if _, ok := f.store.CachedSubjectKey(tn, "usr_a"); ok {
		t.Error("expired entry served from the cache")
	}
	if again := lookup(t, f, tn, "usr_a"); again.ID != k.ID {
		t.Errorf("lookup after expiry = %v, want %v", again.ID, k.ID)
	}
}

// TestShredEvictsCachedKey: a key served from the cache for decryption stops
// being available as soon as its row is destroyed.
//
// Covers: SEC-08
func TestShredEvictsCachedKey(t *testing.T) {
	t.Parallel()
	tn := newTenant(t)
	f := setup(t, keystore.Config{}, tn)
	subjectKey(t, f, tn, "usr_a")
	k := lookup(t, f, tn, "usr_a")
	ct, err := k.DEK.Seal([]byte("secret"), nil)
	if err != nil {
		t.Fatal(err)
	}
	read := func() error {
		return inTenant(t, f, tn, func(ctx context.Context, tx pgx.Tx) error {
			dek, err := f.store.DataKey(ctx, tx, tn, k.ID)
			if err != nil {
				return err
			}
			pt, err := dek.Open(ct, nil)
			if err == nil && string(pt) != "secret" {
				t.Errorf("decrypted %q", pt)
			}
			return err
		})
	}
	if err := read(); err != nil { // fills the key-by-ID cache
		t.Fatal(err)
	}
	if err := read(); err != nil { // served from the cache
		t.Fatal(err)
	}
	err = inTenant(t, f, tn, func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.store.ShredSubject(ctx, tx, tn, "usr_a")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := read(); !errors.Is(err, keystore.ErrShredded) || !errs.Is(err, errs.CodeNotFound) {
		t.Errorf("DataKey after shred = %v, want ErrShredded", err)
	}
	if _, ok := f.store.CachedSubjectKey(tn, "usr_a"); ok {
		t.Error("shredded key still cached for encryption")
	}
}

// TestDataKeyUnknownAndMisbound: an unknown key ID is a plain not-found,
// and a wrapped key whose stored subject was altered does not unwrap.
//
// Covers: SEC-08
func TestDataKeyUnknownAndMisbound(t *testing.T) {
	t.Parallel()
	tn := newTenant(t)
	f := setup(t, keystore.Config{}, tn)
	k := subjectKey(t, f, tn, "usr_a")
	unknown, err := ids.New[ids.KeyKind]()
	if err != nil {
		t.Fatal(err)
	}
	err = inTenant(t, f, tn, func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.store.DataKey(ctx, tx, tn, unknown)
		return err
	})
	if !errs.Is(err, errs.CodeNotFound) || errors.Is(err, keystore.ErrShredded) {
		t.Errorf("DataKey of an unknown ID = %v, want plain not found", err)
	}

	err = f.db.InPlatformTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE keys.data_keys SET subject = 'usr_other' WHERE tenant_id = $1 AND id = $2`, tn.UUID(), k.ID.UUID())
		return err
	})
	if err != nil {
		t.Fatalf("altering the subject: %v", err)
	}
	var dekErr error
	err = inTenant(t, f, tn, func(ctx context.Context, tx pgx.Tx) error {
		_, dekErr = f.store.DataKey(ctx, tx, tn, k.ID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if dekErr == nil {
		t.Error("a key bound to another subject was unwrapped")
	}
}
