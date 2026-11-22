//go:build integration

// Package tenancytest is the integration-test kit of the tenancy module: the
// database with every migration set the module needs, a ready Module on a
// fresh database with an Auditor, change-capture tracking that fails a test
// for any unaudited row (GOV-12), helpers to provision tenants, and the
// module's scenario suite (RunSuite), which the module's packages run so
// that each is measured by the same scenarios.
package tenancytest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/audit/audittest"
	"github.com/antonyrejith/portun/internal/modules/tenancy"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/keystore"
	"github.com/antonyrejith/portun/internal/platform/kms/envelope"
	"github.com/antonyrejith/portun/internal/platform/outbox"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// BaseDomain is the base domain of the fixture's default hosts.
const BaseDomain = "portun.test"

// TypeKeyed is a test event type that records the creation of a data key.
const TypeKeyed = "tenancytest.keyed"

// Main runs the tests of a package against a database with the outbox, the
// key store, the audit store and the tenancy schema.
func Main(m *testing.M) int {
	return dbtest.Main(m, dbtest.Options{Sets: []migrate.Set{
		outbox.Migrations(), keystore.Migrations(), audit.StoreMigrations(), tenancy.Migrations(),
	}})
}

// Clock is a clock tests can advance.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// Now returns the clock's time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Fixture is a Module on a fresh database. Every row it writes is checked
// against the audit records when the test ends.
type Fixture struct {
	Env       *dbtest.Env
	Module    *tenancy.Module
	Stack     *audittest.Stack
	ExportDir string
	Clock     *Clock
}

// New returns a Fixture. Its Module is closed when the test ends.
func New(tb testing.TB) *Fixture {
	tb.Helper()
	env := dbtest.New(tb)
	stack := audittest.New(tb, env, func(r *audit.Registry) error {
		if err := tenancy.RegisterEvents(r); err != nil {
			return err //nolint:wrapcheck // test kit
		}
		return r.Register(audit.EventType{Name: TypeKeyed, Category: audit.CategoryData, OCSFClassUID: 3004})
	})
	// Track after the setup, which writes the platform's key without a record.
	audittest.AssertAllAudited(tb, env)
	clock := &Clock{now: time.Now().UTC()}
	f := &Fixture{Env: env, Stack: stack, ExportDir: tb.TempDir(), Clock: clock}
	m, err := tenancy.New(tenancy.Deps{
		DB: env.DB, Auditor: stack.Auditor, Keys: stack.Keys, BaseDomain: BaseDomain,
		ExportDir: f.ExportDir, Clock: clock.Now,
	})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			tb.Errorf("close: %v", err)
		}
	})
	f.Module = m
	return f
}

// Slug returns a random valid tenant slug.
func Slug(tb testing.TB) string {
	tb.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		tb.Fatal(err)
	}
	return "t" + hex.EncodeToString(b)
}

// Provision creates a tenant with the default environments.
func (f *Fixture) Provision(tb testing.TB) (tenancy.Tenant, []tenancy.Environment) {
	tb.Helper()
	slug := Slug(tb)
	t, envs, err := f.Module.CreateTenant(tb.Context(), tenancy.CreateTenantInput{Slug: slug, Name: "Tenant " + slug, Region: "eu", Plan: "standard"})
	if err != nil {
		tb.Fatal(err)
	}
	return t, envs
}

// Provision creates a tenant with the default environments in a fresh
// Fixture's database, for tests of other modules that only need a tenant.
func Provision(tb testing.TB) (*Fixture, tenancy.Tenant, []tenancy.Environment) {
	tb.Helper()
	f := New(tb)
	t, envs := f.Provision(tb)
	return f, t, envs
}

// CreateDataKey creates the data key of subject in the tenant (through an
// audited change) and returns its ID.
func (f *Fixture) CreateDataKey(tb testing.TB, tenant ids.TenantID, subject string) ids.KeyID {
	tb.Helper()
	ctx := scope.WithTenant(tb.Context(), scope.Tenant{TenantID: tenant})
	var id ids.KeyID
	err := f.Stack.Auditor.Change(ctx, func(ctx context.Context, tx *audit.Tx) error {
		k, err := f.Stack.Keys.SubjectKey(ctx, tx, tenant, subject)
		if err != nil {
			return err //nolint:wrapcheck // test kit
		}
		id = k.ID
		rows := make([]audit.RowRef, 0, len(k.Rows))
		for _, r := range k.Rows {
			rows = append(rows, audit.RowRef{Table: r.Table, Key: r.Key})
		}
		return tx.Record(audit.Record{Type: TypeKeyed, Rows: rows})
	})
	if err != nil {
		tb.Fatal(err)
	}
	return id
}

// DataKey returns the data key with the given ID, as a decrypting reader
// would.
func (f *Fixture) DataKey(tb testing.TB, tenant ids.TenantID, id ids.KeyID) (envelope.DEK, error) {
	tb.Helper()
	return f.Stack.DataKey(tb, f.Env, tenant, id) //nolint:wrapcheck // test kit
}
