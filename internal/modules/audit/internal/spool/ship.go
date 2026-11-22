package spool

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

func (j *Journal) readCursor() (position, bool, error) {
	b, err := os.ReadFile(filepath.Join(j.cfg.Dir, cursorFile))
	if errors.Is(err, os.ErrNotExist) {
		return position{}, false, nil
	}
	if err != nil {
		return position{}, false, errs.Wrap(errs.CodeUnavailable, err, "audit spool: read cursor")
	}
	var p position
	if err := json.Unmarshal(b, &p); err != nil {
		// A cursor is written atomically, so this is outside damage. Starting
		// over resends records; the bus and the writer deduplicate them.
		j.logger.Warn("audit spool: unreadable cursor; shipping from the oldest segment")
		return position{}, false, nil //nolint:nilerr // documented recovery: resend from the oldest segment
	}
	return p, true, nil
}

// writeCursor replaces the cursor file atomically and durably.
func (j *Journal) writeCursor(p position) error {
	b, err := json.Marshal(p)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "audit spool: encode cursor")
	}
	tmp := filepath.Join(j.cfg.Dir, cursorFile+".tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // G304: inside the configured spool directory
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "audit spool: write cursor")
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return errs.Wrap(errs.CodeUnavailable, err, "audit spool: write cursor")
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return errs.Wrap(errs.CodeUnavailable, err, "audit spool: sync cursor")
	}
	if err := f.Close(); err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "audit spool: close cursor")
	}
	if err := os.Rename(tmp, filepath.Join(j.cfg.Dir, cursorFile)); err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "audit spool: replace cursor")
	}
	return syncDir(j.cfg.Dir)
}

// Run ships records until ctx is done, then returns nil. Publish failures
// are logged and retried after RetryBackoff.
func (j *Journal) Run(ctx context.Context) error {
	for {
		_, err := j.ShipOnce(ctx)
		wait := j.cfg.ShipInterval
		if ctx.Err() != nil {
			return nil //nolint:nilerr // stopping on cancellation is the normal end of Run
		}
		if err != nil {
			j.logger.WarnContext(ctx, "audit spool: shipping failed; retrying", slog.String("error", err.Error()))
			wait = j.cfg.RetryBackoff
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-j.wake:
			t.Stop()
			if err != nil {
				// Do not spin on a failing bus because of new appends.
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(j.cfg.RetryBackoff):
				}
			}
		case <-t.C:
		}
	}
}

// ShipOnce publishes every record after the cursor, in order, advancing the
// durable cursor as acknowledgements arrive and deleting segments that are
// fully shipped. It returns the number of records published and stops at
// the first failure.
func (j *Journal) ShipOnce(ctx context.Context) (int, error) {
	j.ship.Lock()
	defer j.ship.Unlock()
	total := 0
	for {
		j.mu.Lock()
		if j.closed {
			j.mu.Unlock()
			return total, errs.New(errs.CodeUnavailable, "audit spool: closed")
		}
		cur := j.cursor
		var end int64
		next := uint64(0)
		for i, s := range j.segs {
			if s.num == cur.Segment {
				end = s.size
				if i+1 < len(j.segs) {
					next = j.segs[i+1].num
				}
			}
		}
		j.mu.Unlock()

		n, err := j.shipSegment(ctx, cur, end)
		total += n
		if err != nil {
			return total, err
		}
		if next == 0 {
			return total, nil
		}
		// The segment is complete (a later one exists) and fully shipped.
		if err := j.advance(position{Segment: next}, cur.Segment); err != nil {
			return total, err
		}
	}
}

// shipSegment publishes the records of segment cur.Segment between cur.Offset
// and end, persisting the cursor every ShipBatch records and at the end.
func (j *Journal) shipSegment(ctx context.Context, cur position, end int64) (int, error) {
	if cur.Offset >= end {
		return 0, nil
	}
	f, err := os.Open(j.segPath(cur.Segment))
	if err != nil {
		return 0, errs.Wrap(errs.CodeUnavailable, err, "audit spool: open segment for shipping")
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(cur.Offset, io.SeekStart); err != nil {
		return 0, errs.Wrap(errs.CodeUnavailable, err, "audit spool: seek segment")
	}
	r := bufio.NewReader(io.LimitReader(f, end-cur.Offset))
	pos, saved := cur, cur
	shipped := 0
	save := func() error {
		if pos == saved {
			return nil
		}
		if err := j.advance(pos, 0); err != nil {
			return err
		}
		saved = pos
		return nil
	}
	for pos.Offset < end {
		size, err := j.shipRecord(ctx, r)
		if err != nil {
			return shipped, errors.Join(err, save())
		}
		pos.Offset += size
		shipped++
		if shipped%j.cfg.ShipBatch == 0 {
			if err := save(); err != nil {
				return shipped, err
			}
		}
	}
	return shipped, save()
}

// shipRecord reads and publishes one record and returns its size.
func (j *Journal) shipRecord(ctx context.Context, r io.Reader) (int64, error) {
	payload, size, err := readRecord(r)
	if err != nil {
		return 0, errs.Wrap(errs.CodeInternal, err, "audit spool: corrupt record while shipping")
	}
	var e Entry
	if err := json.Unmarshal(payload, &e); err != nil {
		return 0, errs.Wrap(errs.CodeInternal, err, "audit spool: undecodable record")
	}
	if _, err := j.pub.Publish(ctx, bus.Message{Subject: e.Subject, ID: e.ID, Data: e.Data}); err != nil {
		return 0, errs.Wrap(errs.CodeOf(err), err, "audit spool: publish")
	}
	return size, nil
}

// advance persists the cursor at p and, when drop is non-zero, deletes that
// (fully shipped) segment.
func (j *Journal) advance(p position, drop uint64) error {
	if err := j.writeCursor(p); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cursor = p
	if drop == 0 {
		return nil
	}
	for i, s := range j.segs {
		if s.num == drop {
			if err := os.Remove(j.segPath(drop)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return errs.Wrap(errs.CodeUnavailable, err, "audit spool: delete shipped segment")
			}
			j.used -= s.size
			j.segs = append(j.segs[:i], j.segs[i+1:]...)
			break
		}
	}
	return syncDir(j.cfg.Dir)
}
