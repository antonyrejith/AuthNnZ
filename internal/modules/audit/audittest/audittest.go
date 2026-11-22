//go:build integration

// Package audittest is the integration-test kit other modules use to check
// the audit guarantees: it exposes the change-capture reconciler
// (GOV-12) and builds an Auditor on a test database.
package audittest

import (
	"context"
	"testing"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/reconcile"
	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/keystore"
	"github.com/antonyrejith/portun/internal/platform/kms/envelope"
	"github.com/antonyrejith/portun/internal/platform/kms/local"
	"github.com/antonyrejith/portun/internal/platform/scope"
	"github.com/antonyrejith/portun/internal/platform/secret"
	"github.com/jackc/pgx/v5"
)

// AssertAllAudited starts tracking env's database and, when the test ends,
// fails it for every row change that no audit record declared (GOV-12).
// Call it before the test writes anything. exclude lists "schema.table" or
// "schema.*" entries that are not reconciled.
func AssertAllAudited(tb testing.TB, env *dbtest.Env, exclude ...string) {
	tb.Helper()
	reconcile.AssertAllAudited(tb, env, exclude...)
}

// discard is a spool publisher that drops what it receives.
type discard struct{}

func (discard) Publish(context.Context, bus.Message) (bus.PubAck, error) {
	return bus.PubAck{Stream: bus.StreamAudit, Sequence: 1}, nil
}

// Stack is an Auditor with its dependencies on a test database.
type Stack struct {
	Auditor  *audit.Auditor
	Registry *audit.Registry
	Keys     *keystore.Store
}

// New returns an Auditor on env's database with a local in-memory KMS and a
// spool in a temporary directory, after creating the platform's
// key-encryption key (the chain of platform records needs it). register
// receives the registry to add event types before the Auditor is created.
func New(tb testing.TB, env *dbtest.Env, register func(*audit.Registry) error) *Stack {
	tb.Helper()
	k, err := local.NewMemory()
	if err != nil {
		tb.Fatal(err)
	}
	keys := keystore.New(k, keystore.Config{})
	err = env.DB.InPlatformTx(tb.Context(), func(ctx context.Context, tx pgx.Tx) error {
		_, _, err := keys.CreateTenantKEK(ctx, tx, ids.TenantID{})
		return err //nolint:wrapcheck // audit and keystore errors are *errs.Error
	})
	if err != nil {
		tb.Fatal(err)
	}
	sp, err := audit.OpenSpool(audit.SpoolConfig{Dir: tb.TempDir()}, discard{}, nil)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = sp.Close() })
	reg := audit.NewRegistry()
	if register != nil {
		if err := register(reg); err != nil {
			tb.Fatal(err)
		}
	}
	root := make([]byte, 32)
	copy(root, "fingerprint-root-for-tests-only!")
	a, err := audit.New(audit.Deps{
		DB: env.DB, Registry: reg, Keystore: keys, Spool: sp,
		FingerprintRoot: secret.NewBytes(root), Origin: audit.Origin{Service: "portun-test", Region: "eu"},
	})
	if err != nil {
		tb.Fatal(err)
	}
	return &Stack{Auditor: a, Registry: reg, Keys: keys}
}

// DataKey reads the data key with the given ID in the tenant's scope, as a
// decrypting reader would. A destroyed key (or tenant key-encryption key)
// fails.
func (s *Stack) DataKey(tb testing.TB, env *dbtest.Env, tenant ids.TenantID, id ids.KeyID) (envelope.DEK, error) {
	tb.Helper()
	ctx := scope.WithTenant(tb.Context(), scope.Tenant{TenantID: tenant})
	var dek envelope.DEK
	err := env.DB.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		dek, err = s.Keys.DataKey(ctx, tx, tenant, id)
		return err //nolint:wrapcheck // test kit
	})
	return dek, err //nolint:wrapcheck // test kit
}
