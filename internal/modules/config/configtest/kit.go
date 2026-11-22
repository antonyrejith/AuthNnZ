//go:build integration

package configtest

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/audit/audittest"
	"github.com/antonyrejith/portun/internal/modules/config"
	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/keystore"
	"github.com/antonyrejith/portun/internal/platform/outbox"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// typeKeyed records the creation of a tenant's key-encryption key.
const typeKeyed = "configtest.keyed"

// Main runs the tests of a package against a database with the outbox, the
// key store, the audit store and the config schema.
func Main(m *testing.M) {
	os.Exit(dbtest.Main(m, dbtest.Options{Sets: []migrate.Set{
		outbox.Migrations(), keystore.Migrations(), audit.StoreMigrations(), config.Migrations(),
	}}))
}

// Fixture is a Module on a fresh database with the example registry. Every
// row it writes is checked against the audit records when the test ends
// (GOV-12).
type Fixture struct {
	Env      *dbtest.Env
	Stack    *audittest.Stack
	Registry *config.Registry
	Module   *config.Module
	// Logs captures the modules' log records.
	Logs *LogCapture
}

// LogCapture is a slog handler that keeps the messages it receives.
type LogCapture struct {
	mu   sync.Mutex
	msgs []string
}

// Enabled implements slog.Handler.
func (*LogCapture) Enabled(context.Context, slog.Level) bool { return true }

// Handle implements slog.Handler.
func (c *LogCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, r.Message)
	return nil
}

// WithAttrs implements slog.Handler.
func (c *LogCapture) WithAttrs([]slog.Attr) slog.Handler { return c }

// WithGroup implements slog.Handler.
func (c *LogCapture) WithGroup(string) slog.Handler { return c }

// Count returns the number of captured messages containing substr.
func (c *LogCapture) Count(substr string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, m := range c.msgs {
		if strings.Contains(m, substr) {
			n++
		}
	}
	return n
}

// NewFixture returns a Fixture. exclude lists "schema.table" entries the test
// writes outside the module (not reconciled).
func NewFixture(tb testing.TB, exclude ...string) *Fixture {
	tb.Helper()
	env := dbtest.New(tb)
	stack := audittest.New(tb, env, func(r *audit.Registry) error {
		if err := config.RegisterEvents(r); err != nil {
			return err //nolint:wrapcheck // test kit
		}
		return r.Register(audit.EventType{Name: typeKeyed, Category: audit.CategoryKeyManagement, OCSFClassUID: 3004})
	})
	// Track after the setup, which writes the platform's key without a record.
	audittest.AssertAllAudited(tb, env, exclude...)
	reg, err := NewRegistry()
	if err != nil {
		tb.Fatal(err)
	}
	f := &Fixture{Env: env, Stack: stack, Registry: reg, Logs: &LogCapture{}}
	f.Module = f.NewModule(tb, "main", nil, time.Hour)
	return f
}

// NewModule returns another Module on the same database, as a second process
// would be. A nil bus means it polls only.
func (f *Fixture) NewModule(tb testing.TB, instance string, b *bus.Bus, poll time.Duration) *config.Module {
	tb.Helper()
	m, err := config.New(config.Deps{
		DB: f.Env.DB, Auditor: f.Stack.Auditor, Bus: b, Registry: f.Registry,
		InstanceID: instance, PollInterval: poll, Logger: slog.New(f.Logs),
	})
	if err != nil {
		tb.Fatal(err)
	}
	return m
}

// Tenant returns a new tenant whose audit chain can be written.
func (f *Fixture) Tenant(tb testing.TB) ids.TenantID {
	tb.Helper()
	id, err := ids.New[ids.TenantKind]()
	if err != nil {
		tb.Fatal(err)
	}
	err = f.Stack.Auditor.PlatformChange(PlatformCtx(tb), func(ctx context.Context, tx *audit.Tx) error {
		_, rows, err := f.Stack.Keys.CreateTenantKEK(ctx, tx, id)
		if err != nil {
			return err //nolint:wrapcheck // test kit
		}
		refs := make([]audit.RowRef, 0, len(rows))
		for _, r := range rows {
			refs = append(refs, audit.RowRef{Table: r.Table, Key: r.Key})
		}
		return tx.Record(audit.Record{Type: typeKeyed, Rows: refs})
	})
	if err != nil {
		tb.Fatal(err)
	}
	return id
}

// Environment returns a new environment ID.
func Environment(tb testing.TB) ids.EnvironmentID {
	tb.Helper()
	id, err := ids.New[ids.EnvironmentKind]()
	if err != nil {
		tb.Fatal(err)
	}
	return id
}

// Ctx returns a context scoped to the tenant and environment (zero for none)
// with an admin actor and a console request.
func Ctx(tb testing.TB, tenant ids.TenantID, env ids.EnvironmentID) context.Context {
	tb.Helper()
	ctx := scope.WithTenant(tb.Context(), scope.Tenant{TenantID: tenant, EnvironmentID: env})
	ctx = scope.WithActor(ctx, scope.Actor{Type: scope.ActorAdmin, ID: "usr_admin", SessionID: "ses_1"})
	return scope.WithRequest(ctx, scope.Request{RequestID: "req_1", Channel: "console", Operation: "ConfigTest", ChangeReason: "testing"})
}

// PlatformCtx returns a context for platform-level changes.
func PlatformCtx(tb testing.TB) context.Context {
	tb.Helper()
	ctx := scope.WithActor(tb.Context(), scope.Actor{Type: scope.ActorPlatformOperator, ID: "op_1"})
	return scope.WithRequest(ctx, scope.Request{RequestID: "req_p", Channel: "api", Operation: "ConfigTest"})
}
