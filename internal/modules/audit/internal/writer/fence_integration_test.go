//go:build integration

package writer

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// TestFencing refuses to append without the writer lock, and appends while
// holding it.
//
// Covers: GOV-01
func TestFencing(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	logger := discard()
	w, err := New(Config{Bus: &bus.Bus{}, DB: env.DB, LockURL: env.PlatformURL, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	id, err := ids.New[ids.AuditEventKind]()
	if err != nil {
		t.Fatal(err)
	}
	// A plain map, not audit.Event: the audit facade imports this package.
	raw, err := json.Marshal(map[string]any{
		"id": id, "time": time.Now().UTC(), "category": "platform", "type": "tenant.created", "outcome": "success",
	})
	if err != nil {
		t.Fatal(err)
	}
	d := bus.Delivery{Subject: bus.AuditPlatformSubject, ID: uuid.NewString(), Data: raw}
	if err := w.handle(t.Context(), d, 1); !errs.Is(err, errs.CodeUnavailable) {
		t.Fatalf("handle without the lock = %v, want unavailable", err)
	}
	lock, err := acquire(t.Context(), env.PlatformURL, 10*time.Millisecond, logger)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.handle(t.Context(), d, lock.pid); err != nil {
		t.Fatalf("handle with the lock = %v", err)
	}
	if err := w.handle(t.Context(), d, lock.pid); err != nil {
		t.Fatalf("duplicate = %v", err)
	}
	// A second session waits for the lock until its context ends.
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := acquire(ctx, env.PlatformURL, 10*time.Millisecond, logger); err == nil {
		t.Error("a second session took the lock")
	}
	// The watch ends with an error when the lock session dies.
	lock.release(t.Context(), logger)
	if err := lock.watch(t.Context(), 10*time.Millisecond); err == nil {
		t.Error("watch did not notice the lost session")
	}
	if _, err := acquire(t.Context(), secret.NewString("::"), time.Millisecond, logger); !errs.Is(err, errs.CodeInvalidRequest) {
		t.Errorf("acquire with a bad URL = %v", err)
	}
}
