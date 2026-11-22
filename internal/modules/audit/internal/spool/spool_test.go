package spool_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/spool"
	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// fakeBus records publishes; it fails while down is set.
type fakeBus struct {
	mu   sync.Mutex
	down bool
	ids  []string
}

func (f *fakeBus) Publish(_ context.Context, m bus.Message) (bus.PubAck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return bus.PubAck{}, errs.New(errs.CodeUnavailable, "bus down")
	}
	f.ids = append(f.ids, m.ID)
	return bus.PubAck{Stream: bus.StreamAudit, Sequence: uint64(len(f.ids))}, nil
}

func (f *fakeBus) setDown(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = v
}

func (f *fakeBus) published() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ids...)
}

func entry(i int) spool.Entry {
	return spool.Entry{ID: "e" + strconv.Itoa(i), Subject: bus.AuditPlatformSubject, Data: json.RawMessage(fmt.Sprintf(`{"n":%d}`, i))}
}

func open(t *testing.T, cfg spool.Config, pub spool.Publisher) *spool.Journal {
	t.Helper()
	j, err := spool.Open(cfg, pub, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

func appendN(t *testing.T, j *spool.Journal, from, to int) {
	t.Helper()
	for i := from; i < to; i++ {
		if err := j.Append(entry(i)); err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
	}
}

func ids(from, to int) []string {
	var out []string
	for i := from; i < to; i++ {
		out = append(out, "e"+strconv.Itoa(i))
	}
	return out
}

func equal(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("published %d messages %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("message %d = %s, want %s", i, got[i], want[i])
		}
	}
}

// TestShipInOrder: records are shipped in append order across segments,
// and shipped segments are deleted.
//
// Covers: GOV-11
func TestShipInOrder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fb := &fakeBus{}
	j := open(t, spool.Config{Dir: dir, SegmentBytes: 200, ShipBatch: 3}, fb)
	appendN(t, j, 0, 25)
	segs, _ := filepath.Glob(filepath.Join(dir, "*.seg"))
	if len(segs) < 3 {
		t.Fatalf("expected several segments, got %d", len(segs))
	}
	n, err := j.ShipOnce(t.Context())
	if err != nil || n != 25 {
		t.Fatalf("ShipOnce = %d, %v", n, err)
	}
	equal(t, fb.published(), ids(0, 25))
	if j.Pending() {
		t.Error("Pending after shipping everything")
	}
	segs, _ = filepath.Glob(filepath.Join(dir, "*.seg"))
	if len(segs) != 1 {
		t.Errorf("shipped segments not deleted: %d remain", len(segs))
	}
	if n, err := j.ShipOnce(t.Context()); n != 0 || err != nil {
		t.Errorf("second ShipOnce = %d, %v", n, err)
	}
}

// TestBusDownKeepsRecords: a failing bus leaves records spooled; they are
// shipped, in order, once it is back, including after a restart.
//
// Covers: GOV-11
func TestBusDownKeepsRecords(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fb := &fakeBus{down: true}
	j, err := spool.Open(spool.Config{Dir: dir, SegmentBytes: 150}, fb, nil)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, j, 0, 10)
	if _, err := j.ShipOnce(t.Context()); !errs.Is(err, errs.CodeUnavailable) {
		t.Fatalf("ShipOnce with bus down = %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	fb.setDown(false)
	j2 := open(t, spool.Config{Dir: dir, SegmentBytes: 150}, fb)
	if _, err := j2.ShipOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	equal(t, fb.published(), ids(0, 10))
}

// TestCrashRecovery: a torn tail (partial header, partial payload, bad
// checksum) is truncated on open; earlier records survive and unshipped ones
// are resent after a crash between publish and cursor update.
//
// Covers: GOV-11
func TestCrashRecovery(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tail []byte
	}{
		{"partial header", []byte{0, 0, 0}},
		{"partial payload", []byte{0, 0, 0, 50, 1, 2, 3, 4, '{'}},
		{"bad checksum", []byte{0, 0, 0, 2, 0, 0, 0, 0, '{', '}'}},
		{"zero length", []byte{0, 0, 0, 0, 0, 0, 0, 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			fb := &fakeBus{}
			j, err := spool.Open(spool.Config{Dir: dir}, fb, nil)
			if err != nil {
				t.Fatal(err)
			}
			appendN(t, j, 0, 3)
			if _, err := j.ShipOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			appendN(t, j, 3, 6)
			_ = j.Close() // simulate the crash: no further shipping
			segs, _ := filepath.Glob(filepath.Join(dir, "*.seg"))
			last := segs[len(segs)-1]
			f, err := os.OpenFile(last, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write(c.tail); err != nil {
				t.Fatal(err)
			}
			_ = f.Close()

			j2 := open(t, spool.Config{Dir: dir}, fb)
			appendN(t, j2, 6, 8)
			if _, err := j2.ShipOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			equal(t, fb.published(), ids(0, 8))
		})
	}
}

// TestFullSpoolRefuses: strict mode refuses appends beyond the capacity and
// accepts them again after shipping frees space.
//
// Covers: GOV-11
func TestFullSpoolRefuses(t *testing.T) {
	t.Parallel()
	fb := &fakeBus{down: true}
	j := open(t, spool.Config{Dir: t.TempDir(), MaxBytes: 300, SegmentBytes: 100}, fb)
	var full error
	appended := 0
	for i := range 100 {
		if err := j.Append(entry(i)); err != nil {
			full = err
			break
		}
		appended++
	}
	if !errs.Is(full, errs.CodeUnavailable) {
		t.Fatalf("Append on a full spool = %v, want unavailable", full)
	}
	if j.Used() > 300 {
		t.Errorf("Used = %d exceeds MaxBytes", j.Used())
	}
	fb.setDown(false)
	if _, err := j.ShipOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(entry(1000)); err != nil {
		t.Errorf("Append after shipping freed space: %v", err)
	}
	if appended == 0 {
		t.Error("nothing fit into the spool")
	}
}

// TestConcurrentAppends: concurrent appends and a running shipper lose
// nothing and keep each writer's order.
//
// Covers: GOV-11
func TestConcurrentAppends(t *testing.T) {
	t.Parallel()
	fb := &fakeBus{}
	j := open(t, spool.Config{Dir: t.TempDir(), SegmentBytes: 1024, ShipInterval: time.Millisecond}, fb)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- j.Run(ctx) }()
	const writers, each = 8, 50
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range each {
				e := spool.Entry{ID: fmt.Sprintf("w%d-%d", w, i), Subject: bus.AuditPlatformSubject, Data: json.RawMessage(`{}`)}
				if err := j.Append(e); err != nil {
					t.Errorf("Append: %v", err)
				}
			}
		})
	}
	wg.Wait()
	deadline := time.Now().Add(10 * time.Second)
	for j.Pending() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := fb.published()
	if len(got) != writers*each {
		t.Fatalf("published %d, want %d", len(got), writers*each)
	}
	next := map[int]int{}
	for _, id := range got {
		var w, i int
		if _, err := fmt.Sscanf(id, "w%d-%d", &w, &i); err != nil {
			t.Fatal(err)
		}
		if i != next[w] {
			t.Fatalf("writer %d: got record %d, want %d", w, i, next[w])
		}
		next[w]++
	}
}

// TestInvalidInput covers configuration and entry validation.
//
// Covers: GOV-11
func TestInvalidInput(t *testing.T) {
	t.Parallel()
	if _, err := spool.Open(spool.Config{}, &fakeBus{}, nil); err == nil {
		t.Error("Open without a directory succeeded")
	}
	j := open(t, spool.Config{Dir: t.TempDir()}, &fakeBus{})
	for _, e := range []spool.Entry{{Subject: "s", Data: json.RawMessage(`{}`)}, {ID: "x", Data: json.RawMessage(`{}`)}, {ID: "x", Subject: "s", Data: json.RawMessage(`{`)}} {
		if err := j.Append(e); !errs.Is(err, errs.CodeInvalidRequest) {
			t.Errorf("Append(%+v) = %v", e, err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(entry(1)); !errs.Is(err, errs.CodeUnavailable) {
		t.Errorf("Append after Close = %v", err)
	}
	if _, err := j.ShipOnce(t.Context()); err == nil {
		t.Error("ShipOnce after Close succeeded")
	}
	if got := spool.DefaultMaxBytes(20, 2048); got != 20*2048*86400*2 {
		t.Errorf("DefaultMaxBytes = %d", got)
	}
}
