// Package keystore manages the key hierarchy below the KMS for
// crypto-shredding (plan 04 §8.1, ADR-0008): one key-encryption key (KEK)
// per tenant, held in the KMS, and one data-encryption key (DEK) per data
// subject (for example a user), wrapped by the tenant's KEK and stored in
// keys.data_keys. Personal data is encrypted under the subject's DEK;
// destroying the DEK (ShredSubject) makes that data unreadable everywhere,
// including in immutable audit records, and destroying the KEK
// (DestroyTenantKEK) does so for the whole tenant.
//
// # Transactions and audit
//
// Every operation that writes runs inside the caller's transaction and
// returns RowRefs for the rows it wrote, so the caller's audit record
// declares them (ADR-0008 reconciliation). Tenant work passes a transaction
// from db.InTenantTx (row-level security limits it to the tenant); the
// platform's own keys (the zero tenant ID, stored under the nil UUID) and
// tenant KEK management need db.InPlatformTx.
//
// # Wrapping and binding
//
// DEKs are wrapped with package envelope; the owner bound into the wrapping
// is "tenant:<tenant TypeID or platform>/subject:<subject>", so a wrapped DEK
// copied to another tenant or subject does not unwrap.
//
// # Cache
//
// Unwrapped DEKs are kept in a bounded LRU cache (Config.CacheSize). DataKey
// (decryption) always checks the key row first, so a destroyed key is never
// served from the cache. SubjectKey (encryption) trusts a cached subject
// mapping for Config.CacheTTL: after ShredSubject on another instance, data
// may still be encrypted under the destroyed key for up to that long. Such
// data is unreadable, because the wrapped key is gone, so it is shredded too.
// Keys created by a transaction that has not committed are never cached.
package keystore

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/antonyrejith/portun/internal/platform/crypto"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/kms"
	"github.com/antonyrejith/portun/internal/platform/kms/envelope"
)

// Schema is the PostgreSQL schema of the key store.
const Schema = "keys"

// Tables written by this package.
const (
	TableTenantKEKs = "keys.tenant_keks"
	TableDataKeys   = "keys.data_keys"
)

// MaxSubjectLength bounds a subject identifier.
const MaxSubjectLength = 512

// ErrShredded reports a data key that has been destroyed. It comes wrapped in
// an *errs.Error with errs.CodeNotFound.
var ErrShredded = errors.New("keystore: data key destroyed")

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations returns the keys schema's migration set.
func Migrations() migrate.Set {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic("keystore: embedded migrations missing: " + err.Error())
	}
	return migrate.Set{Schema: Schema, FS: sub}
}

// DBTX is the part of a transaction the key store uses. pgx.Tx and audit.Tx
// implement it.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// RowRef identifies a row the key store wrote: schema-qualified table and
// primary key values in key-column order, as text. Callers copy it into
// their audit record.
type RowRef struct {
	Table string
	Key   []string
}

// Key is a subject's active data key.
type Key struct {
	ID  ids.KeyID
	DEK envelope.DEK
	// Rows lists the rows written to create the key; empty when it existed.
	Rows []RowRef
}

// Config configures the store. Zero values take the defaults.
type Config struct {
	// CacheSize bounds the number of cached unwrapped keys (default 10000).
	CacheSize int `env:"CACHE_SIZE" default:"10000"`
	// CacheTTL is how long a cached subject-to-key mapping is used for
	// encryption without rereading the key row (default 1m).
	CacheTTL time.Duration `env:"CACHE_TTL" default:"1m"`
}

const (
	defaultCacheSize = 10000
	defaultCacheTTL  = time.Minute
)

// Store is the key store. It is safe for concurrent use.
type Store struct {
	kms   kms.KMS
	env   *envelope.Envelope
	ttl   time.Duration
	cache *lru
	now   func() time.Time
}

// New returns a store that wraps keys through k.
func New(k kms.KMS, cfg Config) *Store {
	if cfg.CacheSize <= 0 {
		cfg.CacheSize = defaultCacheSize
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = defaultCacheTTL
	}
	return &Store{kms: k, env: envelope.New(k), ttl: cfg.CacheTTL, cache: newLRU(cfg.CacheSize), now: time.Now}
}

// tenantLabel names a tenant in owners and cache keys.
func tenantLabel(t ids.TenantID) string {
	if t.IsZero() {
		return "platform"
	}
	return t.String()
}

func owner(t ids.TenantID, subject string) string {
	return "tenant:" + tenantLabel(t) + "/subject:" + subject
}

// mapErr maps a database error to an errs code (db.MapError).
func mapErr(err error) error {
	return db.MapError(err) //nolint:wrapcheck // db.MapError wraps in an *errs.Error
}

func checkSubject(subject string) error {
	if subject == "" || len(subject) > MaxSubjectLength {
		return errs.New(errs.CodeInternal, fmt.Sprintf("keystore: subject must be 1 to %d bytes", MaxSubjectLength))
	}
	return nil
}

// CreateTenantKEK creates the tenant's KEK in the KMS and records it. The
// zero tenant ID creates the platform's KEK. tx must run as the platform
// role. A tenant that already has a KEK fails with errs.CodeConflict. If the
// transaction rolls back, the KMS key is left unused.
func (s *Store) CreateTenantKEK(ctx context.Context, tx DBTX, tenantID ids.TenantID) (kms.KeyRef, []RowRef, error) {
	suffix, err := crypto.RandomBytes(6)
	if err != nil {
		return "", nil, fmt.Errorf("keystore: %w", err)
	}
	name := fmt.Sprintf("kek-%s-%x", tenantID.UUID(), suffix)
	ref, err := s.kms.CreateKey(ctx, name, kms.PurposeEncrypt)
	if err != nil {
		return "", nil, fmt.Errorf("keystore: create tenant KEK: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO keys.tenant_keks (tenant_id, kek_ref) VALUES ($1, $2)`, tenantID.UUID(), string(ref)); err != nil {
		return "", nil, mapErr(err)
	}
	return ref, []RowRef{{Table: TableTenantKEKs, Key: []string{tenantID.UUID().String()}}}, nil
}

// DestroyTenantKEK marks the tenant's KEK destroyed and destroys it in the
// KMS (kms.DestroyKey), which makes every data key of the tenant, and so all
// its encrypted personal data, unrecoverable. Call it as the last step of
// the transaction: the KMS destruction cannot be rolled back. A tenant
// without an active KEK fails with errs.CodeNotFound.
func (s *Store) DestroyTenantKEK(ctx context.Context, tx DBTX, tenantID ids.TenantID) ([]RowRef, error) {
	var ref string
	err := tx.QueryRow(ctx, `UPDATE keys.tenant_keks SET destroyed_at = clock_timestamp()
		WHERE tenant_id = $1 AND destroyed_at IS NULL RETURNING kek_ref`, tenantID.UUID()).Scan(&ref)
	if err != nil {
		return nil, mapErr(err)
	}
	label := tenantLabel(tenantID)
	s.cache.removeIf(func(e cacheEntry) bool { return tenantOfCacheKey(e.key) == label })
	if err := s.kms.DestroyKey(ctx, kms.KeyRef(ref)); err != nil {
		return nil, fmt.Errorf("keystore: destroy tenant KEK: %w", err)
	}
	return []RowRef{{Table: TableTenantKEKs, Key: []string{tenantID.UUID().String()}}}, nil
}

// CachedSubjectKey returns the subject's key from the cache when a fresh
// entry exists, without touching the database.
func (s *Store) CachedSubjectKey(tenantID ids.TenantID, subject string) (Key, bool) {
	e, ok := s.cache.get(subjectCacheKey(tenantLabel(tenantID), subject))
	if !ok || s.now().Sub(e.stored) > s.ttl {
		return Key{}, false
	}
	id, err := ids.Parse[ids.KeyKind](e.id)
	if err != nil {
		return Key{}, false
	}
	return Key{ID: id, DEK: e.dek}, true
}

// lookupSQL finds the subject's active key and whether the current
// transaction wrote that row (such rows are not cached: the transaction may
// still roll back).
const lookupSQL = `
SELECT id, kek_ref, wrapped_dek,
       coalesce(xmin = pg_current_xact_id_if_assigned()::xid, false)
FROM keys.data_keys
WHERE tenant_id = $1 AND subject = $2 AND destroyed_at IS NULL`

// LookupSubjectKey returns the subject's active key without creating one.
// found is false when the subject has none.
func (s *Store) LookupSubjectKey(ctx context.Context, tx DBTX, tenantID ids.TenantID, subject string) (Key, bool, error) {
	if err := checkSubject(subject); err != nil {
		return Key{}, false, err
	}
	if k, ok := s.CachedSubjectKey(tenantID, subject); ok {
		return k, true, nil
	}
	var (
		id      uuid.UUID
		kekRef  string
		wrapped []byte
		own     bool
	)
	err := tx.QueryRow(ctx, lookupSQL, tenantID.UUID(), subject).Scan(&id, &kekRef, &wrapped, &own)
	if errors.Is(err, pgx.ErrNoRows) {
		return Key{}, false, nil
	}
	if err != nil {
		return Key{}, false, mapErr(err)
	}
	keyID := ids.FromUUID[ids.KeyKind](id)
	dek, err := s.unwrap(ctx, tenantID, keyID, subject, kekRef, wrapped, !own)
	if err != nil {
		return Key{}, false, err
	}
	if !own {
		s.cache.put(cacheEntry{key: subjectCacheKey(tenantLabel(tenantID), subject), id: keyID.String(), dek: dek, stored: s.now()})
	}
	return Key{ID: keyID, DEK: dek}, true, nil
}

// unwrap returns the DEK, from the cache when possible.
func (s *Store) unwrap(ctx context.Context, tenantID ids.TenantID, id ids.KeyID, subject, kekRef string, wrapped []byte, cache bool) (envelope.DEK, error) {
	ck := idCacheKey(tenantLabel(tenantID), id.String())
	if e, ok := s.cache.get(ck); ok {
		return e.dek, nil
	}
	dek, err := s.env.Unwrap(ctx, envelope.Wrapped{KEK: kms.KeyRef(kekRef), Ciphertext: wrapped}, owner(tenantID, subject))
	if err != nil {
		return envelope.DEK{}, fmt.Errorf("keystore: %w", err)
	}
	if cache {
		s.cache.put(cacheEntry{key: ck, id: id.String(), dek: dek, stored: s.now()})
	}
	return dek, nil
}

const insertKeySQL = `
INSERT INTO keys.data_keys (tenant_id, id, subject, kek_ref, wrapped_dek)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (tenant_id, subject) WHERE destroyed_at IS NULL DO NOTHING
RETURNING id`

// SubjectKey returns the subject's active data key, creating it (wrapped by
// the tenant's KEK) when the subject has none. A created key's row is listed
// in Key.Rows. The tenant must have a KEK (CreateTenantKEK); otherwise it
// fails with errs.CodeInternal.
func (s *Store) SubjectKey(ctx context.Context, tx DBTX, tenantID ids.TenantID, subject string) (Key, error) {
	k, found, err := s.LookupSubjectKey(ctx, tx, tenantID, subject)
	if err != nil || found {
		return k, err
	}
	var kekRef string
	err = tx.QueryRow(ctx, `SELECT kek_ref FROM keys.tenant_keks WHERE tenant_id = $1 AND destroyed_at IS NULL`,
		tenantID.UUID()).Scan(&kekRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return Key{}, errs.New(errs.CodeInternal, "keystore: the tenant has no active key-encryption key")
	}
	if err != nil {
		return Key{}, mapErr(err)
	}
	dek, wrapped, err := s.env.NewDEK(ctx, kms.KeyRef(kekRef), owner(tenantID, subject))
	if err != nil {
		return Key{}, fmt.Errorf("keystore: %w", err)
	}
	keyID, err := ids.New[ids.KeyKind]()
	if err != nil {
		return Key{}, err
	}
	var got uuid.UUID
	err = tx.QueryRow(ctx, insertKeySQL, tenantID.UUID(), keyID.UUID(), subject, kekRef, wrapped.Ciphertext).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent transaction created the key first and has committed.
		k, found, err := s.LookupSubjectKey(ctx, tx, tenantID, subject)
		if err != nil {
			return Key{}, err
		}
		if !found {
			return Key{}, errs.New(errs.CodeConflict, "keystore: the subject's key changed concurrently; retry")
		}
		return k, nil
	}
	if err != nil {
		return Key{}, mapErr(err)
	}
	return Key{
		ID:   keyID,
		DEK:  dek,
		Rows: []RowRef{{Table: TableDataKeys, Key: []string{tenantID.UUID().String(), keyID.UUID().String()}}},
	}, nil
}

// DataKey returns the data key with the given ID for decryption. It reads
// the key row every time, so a destroyed key fails with errs.CodeNotFound
// wrapping ErrShredded even if it was cached.
func (s *Store) DataKey(ctx context.Context, tx DBTX, tenantID ids.TenantID, keyID ids.KeyID) (envelope.DEK, error) {
	var (
		subject, kekRef string
		wrapped         []byte
		own             bool
	)
	err := tx.QueryRow(ctx, `SELECT subject, kek_ref, wrapped_dek,
			coalesce(xmin = pg_current_xact_id_if_assigned()::xid, false)
		FROM keys.data_keys WHERE tenant_id = $1 AND id = $2`, tenantID.UUID(), keyID.UUID()).
		Scan(&subject, &kekRef, &wrapped, &own)
	if errors.Is(err, pgx.ErrNoRows) {
		return envelope.DEK{}, errs.New(errs.CodeNotFound, "keystore: data key not found")
	}
	if err != nil {
		return envelope.DEK{}, mapErr(err)
	}
	if wrapped == nil {
		s.cache.removeIf(func(e cacheEntry) bool { return e.id == keyID.String() })
		return envelope.DEK{}, errs.Wrap(errs.CodeNotFound, ErrShredded, "keystore: the data key has been destroyed")
	}
	return s.unwrap(ctx, tenantID, keyID, subject, kekRef, wrapped, !own)
}

// ShredSubject destroys the subject's active data key: the wrapped key is
// deleted and the row kept as a tombstone, so everything encrypted under it
// becomes unreadable. It returns the rows written (none when the subject
// had no key).
func (s *Store) ShredSubject(ctx context.Context, tx DBTX, tenantID ids.TenantID, subject string) ([]RowRef, error) {
	if err := checkSubject(subject); err != nil {
		return nil, err
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, `UPDATE keys.data_keys SET wrapped_dek = NULL, destroyed_at = clock_timestamp()
		WHERE tenant_id = $1 AND subject = $2 AND destroyed_at IS NULL RETURNING id`, tenantID.UUID(), subject).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapErr(err)
	}
	keyID := ids.FromUUID[ids.KeyKind](id).String()
	s.cache.removeIf(func(e cacheEntry) bool { return e.id == keyID })
	return []RowRef{{Table: TableDataKeys, Key: []string{tenantID.UUID().String(), id.String()}}}, nil
}
