package spool_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/spool"
	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// notifyBus fails the first failures publishes, then records each ID and
// signals it on got.
type notifyBus struct {
	failures atomic.Int32
	got      chan string
}

func (n *notifyBus) Publish(_ context.Context, m bus.Message) (bus.PubAck, error) {
	if n.failures.Add(-1) >= 0 {
		return bus.PubAck{}, errs.New(errs.CodeUnavailable, "bus down")
	}
	n.got <- m.ID
	return bus.PubAck{Stream: bus.StreamAudit}, nil
}

func segFiles(t *testing.T, dir string) []string {
	t.Helper()
	segs, err := filepath.Glob(filepath.Join(dir, "*.seg"))
	if err != nil {
		t.Fatal(err)
	}
	return segs
}

func writeCursor(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "cursor"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRunShipsAndStops: Run ships appended records (also after publish
// failures, using the retry backoff) and returns nil when its context ends.
//
// Covers: GOV-11
func TestRunShipsAndStops(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		failures int32
	}{
		{"healthy bus", 0},
		{"bus fails first", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			nb := &notifyBus{got: make(chan string, 16)}
			nb.failures.Store(c.failures)
			j := open(t, spool.Config{Dir: t.TempDir(), ShipInterval: time.Millisecond, RetryBackoff: time.Millisecond}, nb)
			appendN(t, j, 0, 3)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- j.Run(ctx) }()
			var got []string
			collect := func(n int) {
				t.Helper()
				for len(got) < n {
					select {
					case id := <-nb.got:
						got = append(got, id)
					case <-time.After(10 * time.Second):
						t.Fatalf("timed out; shipped %v", got)
					}
				}
			}
			collect(3)
			appendN(t, j, 3, 5) // an append while Run is idle wakes the shipper
			collect(5)
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("Run = %v, want nil", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Run did not stop after cancellation")
			}
			// A failed publish must not skip or reorder any record.
			equal(t, got[:3], ids(0, 3))
			equal(t, got[3:], ids(3, 5))
		})
	}
}

// TestOpenRejectsBadDirectory: Open refuses an empty directory and one that
// cannot be created.
//
// Covers: GOV-11
func TestOpenRejectsBadDirectory(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		dir  string
		want errs.Code
	}{
		{"empty", "", errs.CodeInvalidRequest},
		{"below a regular file", filepath.Join(file, "spool"), errs.CodeUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			j, err := spool.Open(spool.Config{Dir: c.dir}, &fakeBus{}, nil)
			if !errs.Is(err, c.want) {
				t.Fatalf("Open = %v, want code %v", err, c.want)
			}
			if j != nil {
				t.Error("Open returned a journal with an error")
			}
		})
	}
}

// TestClosedJournal: a closed journal refuses appends and shipping, and
// closing twice is harmless.
//
// Covers: GOV-11
func TestClosedJournal(t *testing.T) {
	t.Parallel()
	j, err := spool.Open(spool.Config{Dir: t.TempDir()}, &fakeBus{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Errorf("second Close = %v", err)
	}
	if err := j.Append(entry(1)); !errs.Is(err, errs.CodeUnavailable) {
		t.Errorf("Append after Close = %v", err)
	}
	if _, err := j.ShipOnce(t.Context()); !errs.Is(err, errs.CodeUnavailable) {
		t.Errorf("ShipOnce after Close = %v", err)
	}
}

// TestAppendTooLarge: an entry above the record limit is refused with
// payload-too-large and leaves the journal empty.
//
// Covers: GOV-11
func TestAppendTooLarge(t *testing.T) {
	t.Parallel()
	j := open(t, spool.Config{Dir: t.TempDir()}, &fakeBus{})
	big := json.RawMessage(`"` + strings.Repeat("x", 17<<20) + `"`)
	err := j.Append(spool.Entry{ID: "big", Subject: bus.AuditPlatformSubject, Data: big})
	if !errs.Is(err, errs.CodePayloadTooLarge) {
		t.Fatalf("Append = %v, want payload too large", err)
	}
	if j.Used() != 0 || j.Pending() {
		t.Errorf("journal changed by a refused entry: used=%d pending=%v", j.Used(), j.Pending())
	}
}

// TestCursorRecovery: unreadable, stale and out-of-range cursors are
// repaired on open without losing records.
//
// Covers: GOV-11
func TestCursorRecovery(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		// cursor builds the cursor file content from the first and last segment numbers.
		cursor      func(first, last string) string
		wantShipped int // -1: not checked
		wantSegs    int
	}{
		{"garbage cursor resends everything", func(_, _ string) string { return "{not json" }, 12, 4},
		{"cursor in last segment drops older ones", func(_, last string) string { return `{"segment":` + last + `,"offset":0}` }, -1, 1},
		{"offset beyond segment end", func(first, _ string) string { return `{"segment":` + first + `,"offset":1000000}` }, -1, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			cfg := spool.Config{Dir: dir, SegmentBytes: 150}
			j, err := spool.Open(cfg, &fakeBus{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			appendN(t, j, 0, 12)
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			segs := segFiles(t, dir)
			if len(segs) < 3 {
				t.Fatalf("setup: %d segments, want several", len(segs))
			}
			wantSegs := c.wantSegs
			if wantSegs == 4 {
				wantSegs = len(segs)
			}
			num := func(p string) string { return strings.TrimLeft(strings.TrimSuffix(filepath.Base(p), ".seg"), "0") }
			writeCursor(t, dir, c.cursor(num(segs[0]), num(segs[len(segs)-1])))

			fb := &fakeBus{}
			j2 := open(t, cfg, fb)
			if got := len(segFiles(t, dir)); got != wantSegs {
				t.Errorf("%d segments after open, want %d", got, wantSegs)
			}
			n, err := j2.ShipOnce(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if c.wantShipped >= 0 && n != c.wantShipped {
				t.Errorf("shipped %d records, want %d", n, c.wantShipped)
			}
			if j2.Pending() {
				t.Error("still pending after ShipOnce")
			}
			if c.wantShipped == 12 {
				equal(t, fb.published(), ids(0, 12))
			}
		})
	}
}

// TestCursorUnreadable: a cursor path that cannot be read fails Open with
// unavailable instead of silently resending.
//
// Covers: GOV-11
func TestCursorUnreadable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "cursor"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Open(spool.Config{Dir: dir}, &fakeBus{}, nil); !errs.Is(err, errs.CodeUnavailable) {
		t.Fatalf("Open = %v, want unavailable", err)
	}
}

// TestCursorWriteFailure: when the cursor cannot be persisted, ShipOnce
// fails with unavailable and the segment is not deleted.
//
// Covers: GOV-11
func TestCursorWriteFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "cursor.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	j := open(t, spool.Config{Dir: dir}, &fakeBus{})
	appendN(t, j, 0, 2)
	if _, err := j.ShipOnce(t.Context()); !errs.Is(err, errs.CodeUnavailable) {
		t.Fatalf("ShipOnce = %v, want unavailable", err)
	}
	if !j.Pending() {
		t.Error("journal not pending although the cursor was not saved")
	}
	if len(segFiles(t, dir)) != 1 {
		t.Error("segment removed although the cursor was not saved")
	}
}

// TestRotateFailure: when the next segment cannot be created, Append fails
// with unavailable and earlier records stay shippable.
//
// Covers: GOV-11
func TestRotateFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fb := &fakeBus{}
	j := open(t, spool.Config{Dir: dir, SegmentBytes: 100}, fb)
	appendN(t, j, 0, 1)
	if got := len(segFiles(t, dir)); got != 1 {
		t.Fatalf("setup: %d segments", got)
	}
	// Block the next segment name with a directory.
	if err := os.Mkdir(filepath.Join(dir, "00000000000000000002.seg"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(entry(1)); !errs.Is(err, errs.CodeUnavailable) {
		t.Fatalf("Append needing rotation = %v, want unavailable", err)
	}
	if n, err := j.ShipOnce(t.Context()); err != nil || n != 1 {
		t.Fatalf("ShipOnce = %d, %v", n, err)
	}
	equal(t, fb.published(), ids(0, 1))
}

// TestUndecodableRecordWhileShipping: a record with a valid checksum but a
// payload that is not an entry is reported as an internal error and nothing
// is published for it.
//
// Covers: GOV-11
func TestUndecodableRecordWhileShipping(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fb := &fakeBus{}
	j, err := spool.Open(spool.Config{Dir: dir}, fb, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`[1,2,3]`)
	rec := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(rec[:4], uint32(len(payload)))
	binary.BigEndian.PutUint32(rec[4:], crc32.Checksum(payload, crc32.MakeTable(crc32.Castagnoli)))
	rec = append(rec, payload...)
	segs := segFiles(t, dir)
	if err := os.WriteFile(segs[0], rec, 0o600); err != nil {
		t.Fatal(err)
	}
	j2 := open(t, spool.Config{Dir: dir}, fb)
	if _, err := j2.ShipOnce(t.Context()); !errs.Is(err, errs.CodeInternal) {
		t.Fatalf("ShipOnce = %v, want internal", err)
	}
	if got := fb.published(); len(got) != 0 {
		t.Errorf("published %v for an undecodable record", got)
	}
}

// TestUnrelatedFilesIgnored: files and directories in the spool directory
// that are not numbered segments do not disturb recovery.
//
// Covers: GOV-11
func TestUnrelatedFilesIgnored(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fb := &fakeBus{}
	j, err := spool.Open(spool.Config{Dir: dir}, fb, nil)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, j, 0, 2)
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.seg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "00000000000000000099.seg"), 0o700); err != nil {
		t.Fatal(err)
	}
	j2 := open(t, spool.Config{Dir: dir}, fb)
	if n, err := j2.ShipOnce(t.Context()); err != nil || n != 2 {
		t.Fatalf("ShipOnce = %d, %v", n, err)
	}
	equal(t, fb.published(), ids(0, 2))
}

// TestCursorBeyondLastSegmentKeepsRecords: a valid cursor naming a segment
// after the last existing one is inconsistent; no segment is deleted and
// every record is shipped, in order, from the first segment.
//
// Covers: GOV-11
func TestCursorBeyondLastSegmentKeepsRecords(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := spool.Config{Dir: dir, SegmentBytes: 150}
	j, err := spool.Open(cfg, &fakeBus{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, j, 0, 12)
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	before := segFiles(t, dir)
	if len(before) < 2 {
		t.Fatalf("setup: %d segments, want at least 2", len(before))
	}
	writeCursor(t, dir, `{"segment":999,"offset":5}`)

	fb := &fakeBus{}
	j2 := open(t, cfg, fb)
	after := segFiles(t, dir)
	if len(after) != len(before) {
		t.Fatalf("segments after open = %v, want %v", after, before)
	}
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("segments after open = %v, want %v", after, before)
		}
	}
	if !j2.Pending() {
		t.Error("records not pending after open")
	}
	if n, err := j2.ShipOnce(t.Context()); err != nil || n != 12 {
		t.Fatalf("ShipOnce = %d, %v", n, err)
	}
	equal(t, fb.published(), ids(0, 12))
}
