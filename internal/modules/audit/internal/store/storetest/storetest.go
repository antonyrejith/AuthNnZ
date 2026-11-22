//go:build integration

// Package storetest is the integration-test kit of the audit store: the
// migrations (audit schema, the outbox, test tenant tables), record builders and helpers.
package storetest

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/kms"
	"github.com/antonyrejith/portun/internal/platform/kms/local"
	"github.com/antonyrejith/portun/internal/platform/outbox"
)

//go:embed testdata
var testdata embed.FS

func sub(dir string) fs.FS {
	s, err := fs.Sub(testdata, "testdata/"+dir)
	if err != nil {
		panic(err)
	}
	return s
}

// Main runs the tests of a package against a database with the audit
// store, the outbox and the test tables.
func Main(m *testing.M) int {
	return dbtest.Main(m, dbtest.Options{Sets: []migrate.Set{
		outbox.Migrations(),
		{Schema: "audittest_app", FS: sub("app")},
		store.Migrations(),
	}})
}

// Logger returns a logger that discards everything.
func Logger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// NewTenant returns a fresh tenant ID.
func NewTenant(tb testing.TB) ids.TenantID {
	tb.Helper()
	t, err := ids.New[ids.TenantKind]()
	if err != nil {
		tb.Fatal(err)
	}
	return t
}

// Event returns a record for tenant (zero: platform) with the given type.
func Event(tb testing.TB, tenant ids.TenantID, typ string) audit.Event {
	tb.Helper()
	id, err := ids.New[ids.AuditEventKind]()
	if err != nil {
		tb.Fatal(err)
	}
	return audit.Event{
		ID: id, Time: time.Now().UTC().Truncate(time.Millisecond), TenantID: tenant,
		Category: audit.CategoryData, Type: typ, Outcome: audit.OutcomeSuccess,
		Actor:   audit.Actor{Type: "admin", ID: "usr_tester"},
		Channel: audit.Channel{Type: "api"},
		Target:  &audit.Target{Type: "widget", ID: "w1"},
		Request: audit.Request{RequestID: "req_" + id.String()[4:], TraceID: "trace-" + typ},
	}
}

// JSON marshals v.
func JSON(tb testing.TB, v any) []byte {
	tb.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

// Append stores records directly (as the writer would) and returns the
// results.
func Append(tb testing.TB, d *db.DB, a *store.Appender, events ...audit.Event) []store.Appended {
	tb.Helper()
	var out []store.Appended
	for _, ev := range events {
		err := d.InPlatformTx(context.Background(), func(ctx context.Context, tx pgx.Tx) error {
			res, err := a.Append(ctx, tx, JSON(tb, ev))
			out = append(out, res)
			return err //nolint:wrapcheck // test kit
		})
		if err != nil {
			tb.Fatalf("append: %v", err)
		}
	}
	return out
}

// SigningKey returns an in-memory KMS with a signing key.
func SigningKey(tb testing.TB) (kms.KMS, kms.KeyRef) {
	tb.Helper()
	k, err := local.NewMemory()
	if err != nil {
		tb.Fatal(err)
	}
	ref, err := k.CreateKey(context.Background(), "audit-checkpoints", kms.PurposeSign)
	if err != nil {
		tb.Fatal(err)
	}
	return k, ref
}

// Bus starts an embedded NATS server with the streams.
func Bus(tb testing.TB) *bus.Bus {
	tb.Helper()
	srv, err := bus.StartEmbedded(context.Background(), bus.EmbeddedConfig{StoreDir: tb.TempDir()}, nil)
	if err != nil {
		tb.Fatalf("StartEmbedded: %v", err)
	}
	tb.Cleanup(srv.Shutdown)
	b, err := bus.Connect(context.Background(), bus.Config{URL: srv.ClientURL(), Credentials: srv.Token(), AllowInsecure: true}, nil)
	if err != nil {
		tb.Fatalf("Connect: %v", err)
	}
	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = b.Close(ctx)
	})
	if err := b.EnsureStreams(context.Background()); err != nil {
		tb.Fatalf("EnsureStreams: %v", err)
	}
	return b
}

// WaitFor polls cond until it holds or 30s pass.
func WaitFor(tb testing.TB, what string, cond func() bool) {
	tb.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			tb.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Count runs a count query as the platform role.
func Count(tb testing.TB, d *db.DB, sql string, args ...any) int64 {
	tb.Helper()
	var n int64
	err := d.InPlatformTx(context.Background(), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, sql, args...).Scan(&n)
	})
	if err != nil {
		tb.Fatalf("count: %v", err)
	}
	return n
}
