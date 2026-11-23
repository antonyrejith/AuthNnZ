// Package idempotency implements the Idempotency-Key header of POST requests
// (ADR-0009): the first request with a key runs, its response is stored for 24
// hours, and a repeated request with the same key and the same content gets
// the stored response again, marked Idempotent-Replayed: true.
//
// # Behavior
//
//   - The key is 1 to 255 printable ASCII characters, else 422.
//   - The request hash is the SHA-256 of the method, path, query and body.
//     The same key with another hash is rejected with 422 (validation_failed).
//   - The first request stores the key as in_progress with a short lease
//     (Config.InProgressTTL); a repeat while it runs gets 409 (conflict). A
//     request that crashes leaves a row that expires with its lease.
//   - A response below 500 is stored as completed and kept for Config.TTL
//     (24 hours). A response of 500 or more, a panic, and a response larger
//     than Config.MaxResponseBytes are not stored: the key is released, so the
//     client can retry.
//   - Expired rows are ignored and replaced; Purge deletes them.
//
// # Storage
//
// Rows live in api.idempotency_keys, written through db.InPlatformTx. It is a
// platform-level, ephemeral table without row-level security that never holds
// tenant business data: a stored body is what the API returned to the same
// caller. It is the one approved exception to the audited-write rule: the
// package is on the write-path allowlist of the architecture test
// (internal/modules/audit/arch_test.go) and the table is on the audit
// reconciler's reviewed exclusion list (ADR-0008).
//
// The scope of a key is chosen by the caller (Middleware's scopeOf): the nil
// UUID for platform APIs, the tenant's ID for tenant APIs. A scope must not
// span callers that must not see each other's responses.
package idempotency

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// Schema is the PostgreSQL schema of the table.
const Schema = "api"

// Table is the schema-qualified table, for the reconciler's exclusion list.
const Table = "api.idempotency_keys"

// Defaults of Config.
const (
	DefaultTTL              = 24 * time.Hour
	DefaultInProgressTTL    = 5 * time.Minute
	DefaultMaxResponseBytes = 1 << 20
	DefaultMaxRequestBytes  = 1 << 20
	// writeTimeout bounds the bookkeeping that runs after the handler, which
	// must finish even when the client has gone.
	writeTimeout = 10 * time.Second
	// claimAttempts bounds the retries when a row disappears between the
	// insert and the read.
	claimAttempts = 3
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations returns the migration set of the table.
func Migrations() migrate.Set {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		// The embedded directory is part of the binary; this cannot fail.
		panic("idempotency: embedded migrations missing: " + err.Error())
	}
	return migrate.Set{Schema: Schema, FS: sub}
}

// Config configures a Store.
type Config struct {
	// DB is required.
	DB *db.DB
	// TTL is how long a completed response is kept; DefaultTTL when zero.
	TTL time.Duration
	// InProgressTTL is the lease of a request that is running;
	// DefaultInProgressTTL when zero.
	InProgressTTL time.Duration
	// MaxResponseBytes is the largest response that is stored;
	// DefaultMaxResponseBytes when zero.
	MaxResponseBytes int
	// MaxRequestBytes is the largest request body the middleware reads to
	// hash it; DefaultMaxRequestBytes when zero. A larger body is a 413.
	MaxRequestBytes int
	Clock           func() time.Time
	Logger          *slog.Logger
}

// Store is the idempotency cache.
type Store struct {
	db       *db.DB
	ttl      time.Duration
	lease    time.Duration
	maxBytes int
	maxBody  int64
	clock    func() time.Time
	logger   *slog.Logger
}

// New returns a Store.
func New(cfg Config) (*Store, error) {
	if cfg.DB == nil {
		return nil, errs.New(errs.CodeInternal, "idempotency: DB is required")
	}
	s := &Store{db: cfg.DB, ttl: cfg.TTL, lease: cfg.InProgressTTL, maxBytes: cfg.MaxResponseBytes, maxBody: int64(cfg.MaxRequestBytes), clock: cfg.Clock, logger: cfg.Logger}
	if s.ttl <= 0 {
		s.ttl = DefaultTTL
	}
	if s.lease <= 0 {
		s.lease = DefaultInProgressTTL
	}
	if s.maxBytes <= 0 {
		s.maxBytes = DefaultMaxResponseBytes
	}
	if s.maxBody <= 0 {
		s.maxBody = DefaultMaxRequestBytes
	}
	if s.clock == nil {
		s.clock = time.Now
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	return s, nil
}

// outcome is the result of claiming a key.
type outcome int

const (
	claimed    outcome = iota // the caller runs the request
	replay                    // a completed response is stored
	mismatch                  // the key was used for another request
	inProgress                // the first request is still running
)

// stored is a completed response.
type stored struct {
	status  int
	headers http.Header
	body    []byte
}

const claimSQL = `
INSERT INTO api.idempotency_keys AS k (scope_id, key, request_hash, status, created_at, expires_at)
VALUES ($1, $2, $3, 'in_progress', $4, $5)
ON CONFLICT (scope_id, key) DO UPDATE SET
    request_hash = EXCLUDED.request_hash, status = 'in_progress', response_status = NULL,
    response_headers = NULL, response_body = NULL, created_at = EXCLUDED.created_at, expires_at = EXCLUDED.expires_at
WHERE k.expires_at <= $4
RETURNING 1`

const loadSQL = `
SELECT request_hash, status, response_status, response_headers, response_body
FROM api.idempotency_keys WHERE scope_id = $1 AND key = $2 AND expires_at > $3`

// claim registers the request or reports what the key already stands for.
func (s *Store) claim(ctx context.Context, scopeID uuid.UUID, key, hash string) (outcome, stored, error) {
	for range claimAttempts {
		out, rec, retry, err := s.claimOnce(ctx, scopeID, key, hash)
		if err != nil {
			return 0, stored{}, err
		}
		if !retry {
			return out, rec, nil
		}
	}
	return 0, stored{}, errs.New(errs.CodeConflict, "The idempotency key is being replaced; retry the request.")
}

// claimOnce makes one attempt to claim the key. retry is set when the row
// vanished between the insert and the read (it was released or purged).
func (s *Store) claimOnce(ctx context.Context, scopeID uuid.UUID, key, hash string) (out outcome, rec stored, retry bool, _ error) {
	err := s.db.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		now := s.clock().UTC()
		var one int
		err := tx.QueryRow(ctx, claimSQL, scopeID, key, hash, now, now.Add(s.lease)).Scan(&one)
		switch {
		case err == nil:
			out = claimed
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return err //nolint:wrapcheck // mapped by InPlatformTx
		}
		var (
			storedHash, status string
			code               *int
			headers, body      []byte
		)
		err = tx.QueryRow(ctx, loadSQL, scopeID, key, now).Scan(&storedHash, &status, &code, &headers, &body)
		if errors.Is(err, pgx.ErrNoRows) {
			retry = true
			return nil
		}
		if err != nil {
			return err //nolint:wrapcheck // mapped by InPlatformTx
		}
		switch {
		case storedHash != hash:
			out = mismatch
		case status != statusCompleted || code == nil:
			out = inProgress
		default:
			rec = stored{status: *code, body: body, headers: http.Header{}}
			if err := json.Unmarshal(headers, &rec.headers); err != nil {
				return errs.Wrap(errs.CodeInternal, err, "idempotency: stored headers are not valid")
			}
			out = replay
		}
		return nil
	})
	return out, rec, retry, err //nolint:wrapcheck // already an *errs.Error
}

const statusCompleted = "completed"

const completeSQL = `
UPDATE api.idempotency_keys SET status = 'completed', response_status = $4, response_headers = $5,
    response_body = $6, expires_at = $7
WHERE scope_id = $1 AND key = $2 AND request_hash = $3 AND status = 'in_progress'`

// complete stores the response of the request that claimed the key.
func (s *Store) complete(ctx context.Context, scopeID uuid.UUID, key, hash string, rec stored) error {
	headers, err := json.Marshal(rec.headers)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "idempotency: encode headers")
	}
	return s.db.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error { //nolint:wrapcheck // mapped by InPlatformTx
		tag, err := tx.Exec(ctx, completeSQL, scopeID, key, hash, rec.status, headers, rec.body, s.clock().UTC().Add(s.ttl))
		if err != nil {
			return err //nolint:wrapcheck // mapped by InPlatformTx
		}
		if tag.RowsAffected() == 0 {
			// The lease ran out and another request took the key over.
			s.logger.WarnContext(ctx, "idempotency lease lost before the response was stored", slog.String("scope_id", scopeID.String()))
		}
		return nil
	})
}

const releaseSQL = `
DELETE FROM api.idempotency_keys
WHERE scope_id = $1 AND key = $2 AND request_hash = $3 AND status = 'in_progress'`

// release gives the key up, so that the client can retry.
func (s *Store) release(ctx context.Context, scopeID uuid.UUID, key, hash string) error {
	return s.db.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error { //nolint:wrapcheck // mapped by InPlatformTx
		_, err := tx.Exec(ctx, releaseSQL, scopeID, key, hash)
		return err //nolint:wrapcheck // mapped by InPlatformTx
	})
}

// Purge deletes expired rows and returns how many it deleted. Run it
// periodically; expired rows are ignored by requests, so purging only frees
// space.
func (s *Store) Purge(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.InPlatformTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM api.idempotency_keys WHERE expires_at <= $1`, s.clock().UTC())
		if err != nil {
			return err //nolint:wrapcheck // mapped by InPlatformTx
		}
		n = tag.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, err //nolint:wrapcheck // already an *errs.Error
	}
	return n, nil
}

// requestHash returns the hash that identifies a request's content: the
// method, path, query and body, separated so that they cannot run together.
func requestHash(r *http.Request, body []byte) string {
	h := sha256.New()
	for _, part := range [][]byte{[]byte(r.Method), []byte(r.URL.EscapedPath()), []byte(r.URL.RawQuery), body} {
		h.Write(binary.LittleEndian.AppendUint64(nil, uint64(len(part))))
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil))
}
