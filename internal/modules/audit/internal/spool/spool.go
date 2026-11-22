// Package spool is the durable local journal for audit events produced
// outside a database transaction (ADR-0008): Append returns only after the
// record is on disk (fsync), and a shipper publishes records to the bus in
// order and forgets them only after the bus acknowledged them.
//
// # Files
//
// The directory holds numbered segment files ("<20 digits>.seg") and a
// cursor file. A segment is a sequence of records:
//
//	length (4 bytes, big-endian) ‖ CRC-32C of the payload (4 bytes, big-endian) ‖ payload (JSON Entry)
//
// Appends go to the last segment; when it would exceed SegmentBytes a new
// segment is created and the directory synced. The cursor file ("cursor",
// JSON {"segment","offset"}, replaced atomically with a synced temporary
// file and rename) marks the first record not yet acknowledged by the bus.
// Segments wholly before the cursor are deleted.
//
// # Recovery
//
// Open scans every segment from its start. At the first record that is
// incomplete or fails its checksum (a torn write from a crash), the segment
// is truncated there and the loss is logged; the cursor is clamped to the
// valid data. Records after the cursor are shipped again; the bus
// deduplicates them by message ID (the event ID) and the audit writer
// ignores duplicate event IDs.
//
// # Capacity
//
// The spool refuses appends (errs.CodeUnavailable) once its segment files
// would exceed MaxBytes: strict mode, always on. The default sizes the
// spool for 24 hours of bus outage at the expected rate:
//
//	MaxBytes = ExpectedRate (events/s) × AvgRecordBytes × 86 400 s × 2 (headroom)
//
// which with the defaults (20 events/s, 2 KiB) is about 7 GB. Deployments set
// ExpectedRate (or MaxBytes) from their measured peak.
package spool

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// Entry is one spooled message.
type Entry struct {
	ID      string          `json:"id"`      // bus message ID (the event ID)
	Subject string          `json:"subject"` // audit.<tenant> or audit.platform
	Data    json.RawMessage `json:"data"`    // the event
}

// Publisher publishes one message and waits for the acknowledgement.
type Publisher interface {
	Publish(ctx context.Context, msg bus.Message) (bus.PubAck, error)
}

// Config configures the spool. Zero values take the defaults.
type Config struct {
	// Dir holds the journal. Required; created with mode 0700.
	Dir string
	// ExpectedRate is the expected peak of spooled events per second, used
	// for the default MaxBytes (default 20).
	ExpectedRate int64
	// AvgRecordBytes is the expected average record size (default 2048).
	AvgRecordBytes int64
	// MaxBytes caps the journal's size (default: see the package documentation).
	MaxBytes int64
	// SegmentBytes is the size at which a new segment starts (default 64 MiB).
	SegmentBytes int64
	// ShipInterval is the shipper's pause when idle (default 200ms; appends
	// wake it at once).
	ShipInterval time.Duration
	// RetryBackoff is the shipper's pause after a failed publish (default 1s).
	RetryBackoff time.Duration
	// ShipBatch is the number of records shipped between cursor writes
	// (default 100).
	ShipBatch int
}

const (
	defaultExpectedRate   = 20
	defaultAvgRecordBytes = 2048
	outageSeconds         = 24 * 60 * 60
	headroom              = 2
	defaultSegmentBytes   = 64 << 20
	defaultShipInterval   = 200 * time.Millisecond
	defaultRetryBackoff   = time.Second
	defaultShipBatch      = 100
	headerSize            = 8
	maxRecordBytes        = 16 << 20
	segmentSuffix         = ".seg"
	cursorFile            = "cursor"
)

// DefaultMaxBytes returns the capacity for 24 hours at rate events per
// second of avgBytes each, with a headroom factor of 2.
func DefaultMaxBytes(rate, avgBytes int64) int64 {
	return rate * avgBytes * outageSeconds * headroom
}

func (c Config) withDefaults() Config {
	if c.ExpectedRate <= 0 {
		c.ExpectedRate = defaultExpectedRate
	}
	if c.AvgRecordBytes <= 0 {
		c.AvgRecordBytes = defaultAvgRecordBytes
	}
	if c.MaxBytes <= 0 {
		c.MaxBytes = DefaultMaxBytes(c.ExpectedRate, c.AvgRecordBytes)
	}
	if c.SegmentBytes <= 0 {
		c.SegmentBytes = defaultSegmentBytes
	}
	if c.ShipInterval <= 0 {
		c.ShipInterval = defaultShipInterval
	}
	if c.RetryBackoff <= 0 {
		c.RetryBackoff = defaultRetryBackoff
	}
	if c.ShipBatch <= 0 {
		c.ShipBatch = defaultShipBatch
	}
	return c
}

type segment struct {
	num  uint64
	size int64 // bytes of complete, synced records
}

type position struct {
	Segment uint64 `json:"segment"`
	Offset  int64  `json:"offset"`
}

// Journal is the spool. It is safe for concurrent use.
type Journal struct {
	cfg    Config
	pub    Publisher
	logger *slog.Logger
	wake   chan struct{}
	ship   sync.Mutex // one shipper at a time

	mu     sync.Mutex
	segs   []segment // ascending; the last one is active
	active *os.File
	used   int64
	cursor position
	closed bool
	broken error // a failed write that could not be undone
}

var castagnoli = crc32.MakeTable(crc32.Castagnoli) //nolint:gochecknoglobals // immutable lookup table

// Open opens or creates the journal in cfg.Dir and recovers it (see the
// package documentation). The shipper does not run until Run is called.
func Open(cfg Config, pub Publisher, logger *slog.Logger) (*Journal, error) {
	cfg = cfg.withDefaults()
	if cfg.Dir == "" {
		return nil, errs.New(errs.CodeInvalidRequest, "audit spool: directory is required")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, errs.Wrap(errs.CodeUnavailable, err, "audit spool: create directory")
	}
	j := &Journal{cfg: cfg, pub: pub, logger: logger.With(slog.String("component", "audit_spool")), wake: make(chan struct{}, 1)}
	if err := j.recover(); err != nil {
		return nil, err
	}
	return j, nil
}

func (j *Journal) segPath(num uint64) string {
	return filepath.Join(j.cfg.Dir, fmt.Sprintf("%020d%s", num, segmentSuffix))
}

func (j *Journal) listSegments() ([]uint64, error) {
	ents, err := os.ReadDir(j.cfg.Dir)
	if err != nil {
		return nil, errs.Wrap(errs.CodeUnavailable, err, "audit spool: read directory")
	}
	var nums []uint64
	for _, e := range ents {
		name, ok := strings.CutSuffix(e.Name(), segmentSuffix)
		if !ok || e.IsDir() {
			continue
		}
		n, err := strconv.ParseUint(name, 10, 64)
		if err != nil {
			continue
		}
		nums = append(nums, n)
	}
	slices.Sort(nums)
	return nums, nil
}

func (j *Journal) recover() error {
	cur, found, err := j.readCursor()
	if err != nil {
		return err
	}
	nums, cursorValid, err := j.liveSegments(cur, found)
	if err != nil {
		return err
	}
	if !found || !cursorValid || cur.Segment < nums[0] {
		cur = position{Segment: nums[0]}
	}
	for _, n := range nums {
		size, err := j.repairSegment(n)
		if err != nil {
			return err
		}
		j.segs = append(j.segs, segment{num: n, size: size})
		j.used += size
		if n == cur.Segment && cur.Offset > size {
			cur.Offset = size
		}
	}
	last := j.segs[len(j.segs)-1]
	if cur.Segment > last.num {
		cur = position{Segment: last.num, Offset: last.size}
	}
	j.cursor = cur
	f, err := os.OpenFile(j.segPath(last.num), os.O_RDWR, 0o600)
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "audit spool: open active segment")
	}
	if _, err := f.Seek(last.size, io.SeekStart); err != nil {
		_ = f.Close()
		return errs.Wrap(errs.CodeUnavailable, err, "audit spool: seek active segment")
	}
	j.active = f
	return nil
}

// liveSegments removes segments before the cursor (left by a crash between
// the cursor update and the deletion) and returns the rest, creating the
// first segment of an empty journal. A cursor naming a segment after the
// last existing one is inconsistent: cursorValid is false, nothing is
// deleted and the caller resends from the first segment.
func (j *Journal) liveSegments(cur position, found bool) (nums []uint64, cursorValid bool, err error) {
	nums, err = j.listSegments()
	if err != nil {
		return nil, false, err
	}
	if found && len(nums) > 0 && cur.Segment > nums[len(nums)-1] {
		return nums, false, nil
	}
	for len(nums) > 0 && found && nums[0] < cur.Segment {
		if err := os.Remove(j.segPath(nums[0])); err != nil {
			return nil, false, errs.Wrap(errs.CodeUnavailable, err, "audit spool: remove shipped segment")
		}
		nums = nums[1:]
	}
	if len(nums) > 0 {
		return nums, true, nil
	}
	next := max(cur.Segment, 1)
	f, err := os.OpenFile(j.segPath(next), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, errs.Wrap(errs.CodeUnavailable, err, "audit spool: create segment")
	}
	_ = f.Close()
	if err := syncDir(j.cfg.Dir); err != nil {
		return nil, false, err
	}
	return []uint64{next}, true, nil
}

// repairSegment returns the length of the segment's valid prefix,
// truncating anything after it.
func (j *Journal) repairSegment(num uint64) (int64, error) {
	path := j.segPath(num)
	f, err := os.OpenFile(path, os.O_RDWR, 0o600) //nolint:gosec // G304: a segment in the configured spool directory
	if err != nil {
		return 0, errs.Wrap(errs.CodeUnavailable, err, "audit spool: open segment")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return 0, errs.Wrap(errs.CodeUnavailable, err, "audit spool: stat segment")
	}
	r := bufio.NewReader(f)
	var valid int64
	for {
		_, n, err := readRecord(r)
		if err != nil {
			break
		}
		valid += n
	}
	if valid < info.Size() {
		j.logger.Warn("audit spool: truncated an incomplete or corrupt record (torn write)",
			slog.String("segment", filepath.Base(path)), slog.Int64("valid_bytes", valid), slog.Int64("dropped_bytes", info.Size()-valid))
		if err := f.Truncate(valid); err != nil {
			return 0, errs.Wrap(errs.CodeUnavailable, err, "audit spool: truncate segment")
		}
		if err := f.Sync(); err != nil {
			return 0, errs.Wrap(errs.CodeUnavailable, err, "audit spool: sync segment")
		}
	}
	return valid, nil
}

var errBadRecord = errors.New("audit spool: bad record")

// readRecord reads one record and returns its payload and total size.
func readRecord(r io.Reader) ([]byte, int64, error) {
	var hdr [headerSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, 0, err //nolint:wrapcheck // io.EOF is the expected end marker
	}
	n := binary.BigEndian.Uint32(hdr[:4])
	if n == 0 || n > maxRecordBytes {
		return nil, 0, errBadRecord
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, 0, errBadRecord
	}
	if crc32.Checksum(payload, castagnoli) != binary.BigEndian.Uint32(hdr[4:]) {
		return nil, 0, errBadRecord
	}
	return payload, int64(headerSize) + int64(n), nil
}

func encodeRecord(payload []byte) []byte {
	out := make([]byte, headerSize, headerSize+len(payload))
	binary.BigEndian.PutUint32(out[:4], uint32(len(payload))) //nolint:gosec // G115: bounded by maxRecordBytes
	binary.BigEndian.PutUint32(out[4:], crc32.Checksum(payload, castagnoli))
	return append(out, payload...)
}

// Append writes e durably. It fails with errs.CodeUnavailable when the
// journal is full or cannot be written, and with errs.CodeInvalidRequest for
// an invalid entry.
func (j *Journal) Append(e Entry) error {
	if e.ID == "" || e.Subject == "" || !json.Valid(e.Data) {
		return errs.New(errs.CodeInvalidRequest, "audit spool: entry needs an ID, a subject and JSON data")
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "audit spool: encode entry")
	}
	if len(payload) > maxRecordBytes {
		return errs.New(errs.CodePayloadTooLarge, "audit spool: entry too large")
	}
	rec := encodeRecord(payload)
	j.mu.Lock()
	defer j.mu.Unlock()
	switch {
	case j.closed:
		return errs.New(errs.CodeUnavailable, "audit spool: closed")
	case j.broken != nil:
		return errs.Wrap(errs.CodeUnavailable, j.broken, "audit spool: journal unusable after a failed write")
	case j.used+int64(len(rec)) > j.cfg.MaxBytes:
		return errs.New(errs.CodeUnavailable, "audit spool: full; refusing the event (strict mode)")
	}
	last := &j.segs[len(j.segs)-1]
	if last.size > 0 && last.size+int64(len(rec)) > j.cfg.SegmentBytes {
		if err := j.rotate(); err != nil {
			return err
		}
		last = &j.segs[len(j.segs)-1]
	}
	if err := j.writeSynced(rec, last.size); err != nil {
		return err
	}
	last.size += int64(len(rec))
	j.used += int64(len(rec))
	select {
	case j.wake <- struct{}{}:
	default:
	}
	return nil
}

// writeSynced writes rec at the end of the active segment and syncs it. On
// failure it truncates back to prev; if that fails too the journal is marked
// broken.
func (j *Journal) writeSynced(rec []byte, prev int64) error {
	_, err := j.active.Write(rec)
	if err == nil {
		err = j.active.Sync()
	}
	if err == nil {
		return nil
	}
	if terr := j.active.Truncate(prev); terr != nil {
		j.broken = errors.Join(err, terr)
	} else if _, serr := j.active.Seek(prev, io.SeekStart); serr != nil {
		j.broken = serr
	}
	return errs.Wrap(errs.CodeUnavailable, err, "audit spool: write record")
}

// rotate syncs and closes the active segment and starts the next one.
func (j *Journal) rotate() error {
	if err := j.active.Sync(); err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "audit spool: sync segment")
	}
	next := j.segs[len(j.segs)-1].num + 1
	f, err := os.OpenFile(j.segPath(next), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "audit spool: create segment")
	}
	if err := syncDir(j.cfg.Dir); err != nil {
		_ = f.Close()
		_ = os.Remove(j.segPath(next))
		return err
	}
	_ = j.active.Close()
	j.active = f
	j.segs = append(j.segs, segment{num: next})
	return nil
}

// Used returns the journal's size in bytes (what counts against MaxBytes).
func (j *Journal) Used() int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.used
}

// Pending reports whether records await shipping.
func (j *Journal) Pending() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	last := j.segs[len(j.segs)-1]
	return j.cursor.Segment != last.num || j.cursor.Offset != last.size
}

// Close closes the journal. Call it after Run has returned.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	if err := j.active.Close(); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "audit spool: close")
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: the configured spool directory
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "audit spool: open directory")
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "audit spool: sync directory")
	}
	return nil
}
