// Package postgres is the PostgreSQL storage of the config module (schema
// "config"). Every statement runs in a transaction handed in by the caller:
// an audited transaction for writes (ADR-0008) and a read-only tenant
// transaction for reads, so row-level security (ADR-0002) always applies.
package postgres

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/modules/config/internal/app"
	"github.com/antonyrejith/portun/internal/modules/config/internal/domain"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

// Schema is the PostgreSQL schema of the module.
const Schema = "config"

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations returns the module's migration set.
func Migrations() migrate.Set {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		// The embedded directory is part of the binary; this cannot fail.
		panic("config: embedded migrations missing: " + err.Error())
	}
	return migrate.Set{Schema: Schema, FS: sub}
}

// Repo implements app.Repo.
type Repo struct{}

var _ app.Repo = Repo{}

// mapErr maps a database error to an errs code (db.MapError).
func mapErr(err error) error {
	return db.MapError(err) //nolint:wrapcheck // db.MapError wraps in an *errs.Error
}

func notFound(what string) error { return errs.New(errs.CodeNotFound, what+" not found") }

// jsonParam turns raw JSON into a statement parameter (NULL when empty).
func jsonParam(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

func lockClause(lock bool) string {
	if lock {
		return " FOR UPDATE"
	}
	return ""
}

const objectColumns = `id, environment_id, kind, name, status, current_version, version, created_at, updated_at`

func scanObject(row pgx.Row) (domain.Object, error) {
	var (
		o       domain.Object
		id, env uuid.UUID
		cur     *int
	)
	if err := row.Scan(&id, &env, &o.Kind, &o.Name, &o.Status, &cur, &o.Version, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return domain.Object{}, err //nolint:wrapcheck // callers map it
	}
	o.ID, o.EnvironmentID = ids.FromUUID[ids.ConfigObjectKind](id), ids.FromUUID[ids.EnvironmentKind](env)
	if cur != nil {
		o.CurrentVersion = *cur
	}
	return o, nil
}

// FindObject implements app.Repo.
func (Repo) FindObject(ctx context.Context, q db.Querier, tenant ids.TenantID, env ids.EnvironmentID, kind, name string) (domain.Object, bool, error) {
	o, err := scanObject(q.QueryRow(ctx, `SELECT `+objectColumns+` FROM config.objects
		WHERE tenant_id = $1 AND environment_id = $2 AND kind = $3 AND name = $4`, tenant.UUID(), env.UUID(), kind, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Object{}, false, nil
	}
	if err != nil {
		return domain.Object{}, false, mapErr(err)
	}
	return o, true, nil
}

// GetObject implements app.Repo.
func (Repo) GetObject(ctx context.Context, q db.Querier, tenant ids.TenantID, env ids.EnvironmentID, id ids.ConfigObjectID, lock bool) (domain.Object, error) {
	o, err := scanObject(q.QueryRow(ctx, `SELECT `+objectColumns+` FROM config.objects
		WHERE tenant_id = $1 AND environment_id = $2 AND id = $3`+lockClause(lock), tenant.UUID(), env.UUID(), id.UUID()))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Object{}, notFound("configuration object")
	}
	if err != nil {
		return domain.Object{}, mapErr(err)
	}
	return o, nil
}

// InsertObject implements app.Repo.
func (Repo) InsertObject(ctx context.Context, q app.Q, tenant ids.TenantID, o domain.Object) error {
	var cur *int
	if o.CurrentVersion > 0 {
		cur = &o.CurrentVersion
	}
	_, err := q.Exec(ctx, `INSERT INTO config.objects (tenant_id, environment_id, id, kind, name, status, current_version, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		tenant.UUID(), o.EnvironmentID.UUID(), o.ID.UUID(), o.Kind, o.Name, o.Status, cur, o.Version, o.CreatedAt, o.UpdatedAt)
	return mapErrIf(err)
}

// UpdateObject implements app.Repo.
func (Repo) UpdateObject(ctx context.Context, q app.Q, tenant ids.TenantID, o domain.Object) error {
	var cur *int
	if o.CurrentVersion > 0 {
		cur = &o.CurrentVersion
	}
	tag, err := q.Exec(ctx, `UPDATE config.objects SET status = $4, current_version = $5, version = $6, updated_at = $7
		WHERE tenant_id = $1 AND environment_id = $2 AND id = $3`,
		tenant.UUID(), o.EnvironmentID.UUID(), o.ID.UUID(), o.Status, cur, o.Version, o.UpdatedAt)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() != 1 {
		return notFound("configuration object")
	}
	return nil
}

// collect reads all rows with scan.
func collect[T any](rows pgx.Rows, scan func(pgx.Row) (T, error)) ([]T, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (T, error) { return scan(r) }) //nolint:wrapcheck // callers map it
}

func mapErrIf(err error) error {
	if err == nil {
		return nil
	}
	return mapErr(err)
}

const versionColumns = `object_id, version, status, spec, checksum, change_note, created_by, created_at, published_at`

func scanVersion(row pgx.Row) (domain.Version, error) {
	var (
		v   domain.Version
		oid uuid.UUID
	)
	if err := row.Scan(&oid, &v.Version, &v.Status, &v.Spec, &v.Checksum, &v.ChangeNote, &v.CreatedBy, &v.CreatedAt, &v.PublishedAt); err != nil {
		return domain.Version{}, err //nolint:wrapcheck // callers map it
	}
	v.ObjectID = ids.FromUUID[ids.ConfigObjectKind](oid)
	return v, nil
}

// InsertVersion implements app.Repo.
func (Repo) InsertVersion(ctx context.Context, q app.Q, tenant ids.TenantID, v domain.Version) error {
	_, err := q.Exec(ctx, `INSERT INTO config.versions (tenant_id, object_id, version, status, spec, checksum, change_note, created_by, created_at, published_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		tenant.UUID(), v.ObjectID.UUID(), v.Version, v.Status, string(v.Spec), v.Checksum, v.ChangeNote, v.CreatedBy, v.CreatedAt, v.PublishedAt)
	return mapErrIf(err)
}

// GetVersion implements app.Repo.
func (Repo) GetVersion(ctx context.Context, q db.Querier, tenant ids.TenantID, id ids.ConfigObjectID, version int) (domain.Version, error) {
	v, err := scanVersion(q.QueryRow(ctx, `SELECT `+versionColumns+` FROM config.versions
		WHERE tenant_id = $1 AND object_id = $2 AND version = $3`, tenant.UUID(), id.UUID(), version))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Version{}, notFound("configuration version")
	}
	if err != nil {
		return domain.Version{}, mapErr(err)
	}
	return v, nil
}

// OpenDraft implements app.Repo.
func (Repo) OpenDraft(ctx context.Context, q db.Querier, tenant ids.TenantID, id ids.ConfigObjectID) (domain.Version, bool, error) {
	v, err := scanVersion(q.QueryRow(ctx, `SELECT `+versionColumns+` FROM config.versions
		WHERE tenant_id = $1 AND object_id = $2 AND status = 'draft' FOR UPDATE`, tenant.UUID(), id.UUID()))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Version{}, false, nil
	}
	if err != nil {
		return domain.Version{}, false, mapErr(err)
	}
	return v, true, nil
}

// MaxVersion implements app.Repo.
func (Repo) MaxVersion(ctx context.Context, q db.Querier, tenant ids.TenantID, id ids.ConfigObjectID) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `SELECT coalesce(max(version), 0) FROM config.versions WHERE tenant_id = $1 AND object_id = $2`,
		tenant.UUID(), id.UUID()).Scan(&n); err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}

// UpdateDraft implements app.Repo.
func (Repo) UpdateDraft(ctx context.Context, q app.Q, tenant ids.TenantID, v domain.Version) error {
	tag, err := q.Exec(ctx, `UPDATE config.versions SET spec = $4, checksum = $5, change_note = $6
		WHERE tenant_id = $1 AND object_id = $2 AND version = $3 AND status = 'draft'`,
		tenant.UUID(), v.ObjectID.UUID(), v.Version, string(v.Spec), v.Checksum, v.ChangeNote)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() != 1 {
		return errs.New(errs.CodeConflict, "the draft is no longer open")
	}
	return nil
}

// MarkPublished implements app.Repo.
func (Repo) MarkPublished(ctx context.Context, q app.Q, tenant ids.TenantID, id ids.ConfigObjectID, version int, at time.Time) error {
	tag, err := q.Exec(ctx, `UPDATE config.versions SET status = 'published', published_at = $4
		WHERE tenant_id = $1 AND object_id = $2 AND version = $3 AND status = 'draft'`, tenant.UUID(), id.UUID(), version, at)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() != 1 {
		return errs.New(errs.CodeConflict, "the draft is no longer open")
	}
	return nil
}

// ListVersions implements app.Repo.
func (Repo) ListVersions(ctx context.Context, q db.Querier, tenant ids.TenantID, id ids.ConfigObjectID, after, limit int) ([]domain.Version, error) {
	rows, err := q.Query(ctx, `SELECT `+versionColumns+` FROM config.versions
		WHERE tenant_id = $1 AND object_id = $2 AND ($3 = 0 OR version < $3) ORDER BY version DESC LIMIT $4`,
		tenant.UUID(), id.UUID(), after, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	out, err := collect(rows, scanVersion)
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// CurrentSpecs implements app.Repo.
func (Repo) CurrentSpecs(ctx context.Context, q db.Querier, tenant ids.TenantID, env ids.EnvironmentID) ([]domain.CurrentSpec, error) {
	rows, err := q.Query(ctx, `SELECT o.kind, o.name, v.spec
		FROM config.objects o
		JOIN config.versions v ON v.tenant_id = o.tenant_id AND v.object_id = o.id AND v.version = o.current_version
		WHERE o.tenant_id = $1 AND o.environment_id = $2 AND o.status = 'active' AND o.current_version IS NOT NULL
		ORDER BY o.kind, o.name`, tenant.UUID(), env.UUID())
	if err != nil {
		return nil, mapErr(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.CurrentSpec, error) {
		var c domain.CurrentSpec
		err := r.Scan(&c.Kind, &c.Name, &c.Spec)
		return c, err //nolint:wrapcheck // mapped by the caller below
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

const settingColumns = `key, level_environment_id, mode, value, version, updated_by, updated_at`

func scanSetting(row pgx.Row) (domain.SettingRow, error) {
	var (
		r   domain.SettingRow
		env uuid.UUID
		mod string
	)
	if err := row.Scan(&r.Key, &env, &mod, &r.Value, &r.Version, &r.UpdatedBy, &r.UpdatedAt); err != nil {
		return domain.SettingRow{}, err //nolint:wrapcheck // callers map it
	}
	r.Mode = domain.Mode(mod)
	r.LevelEnvironmentID = ids.FromUUID[ids.EnvironmentKind](env)
	return r, nil
}

// SettingRows implements app.Repo.
func (Repo) SettingRows(ctx context.Context, q db.Querier, tenant ids.TenantID, env ids.EnvironmentID) ([]domain.SettingRow, error) {
	rows, err := q.Query(ctx, `SELECT `+settingColumns+` FROM config.settings
		WHERE tenant_id = $1 AND level_environment_id IN ($2, $3) ORDER BY key, level_environment_id`,
		tenant.UUID(), uuid.Nil, env.UUID())
	if err != nil {
		return nil, mapErr(err)
	}
	out, err := collect(rows, scanSetting)
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// GetSettingRow implements app.Repo.
func (Repo) GetSettingRow(ctx context.Context, q db.Querier, tenant ids.TenantID, levelEnv ids.EnvironmentID, key string, lock bool) (domain.SettingRow, bool, error) {
	r, err := scanSetting(q.QueryRow(ctx, `SELECT `+settingColumns+` FROM config.settings
		WHERE tenant_id = $1 AND key = $2 AND level_environment_id = $3`+lockClause(lock), tenant.UUID(), key, levelEnv.UUID()))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SettingRow{}, false, nil
	}
	if err != nil {
		return domain.SettingRow{}, false, mapErr(err)
	}
	return r, true, nil
}

// InsertSettingRow implements app.Repo.
func (Repo) InsertSettingRow(ctx context.Context, q app.Q, tenant ids.TenantID, r domain.SettingRow) error {
	_, err := q.Exec(ctx, `INSERT INTO config.settings (tenant_id, level_environment_id, key, mode, value, version, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		tenant.UUID(), r.LevelEnvironmentID.UUID(), r.Key, string(r.Mode), jsonParam(r.Value), r.Version, r.UpdatedBy, r.UpdatedAt)
	return mapErrIf(err)
}

// UpdateSettingRow implements app.Repo.
func (Repo) UpdateSettingRow(ctx context.Context, q app.Q, tenant ids.TenantID, r domain.SettingRow) error {
	tag, err := q.Exec(ctx, `UPDATE config.settings SET mode = $4, value = $5, version = $6, updated_by = $7, updated_at = $8
		WHERE tenant_id = $1 AND level_environment_id = $2 AND key = $3`,
		tenant.UUID(), r.LevelEnvironmentID.UUID(), r.Key, string(r.Mode), jsonParam(r.Value), r.Version, r.UpdatedBy, r.UpdatedAt)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() != 1 {
		return notFound("setting")
	}
	return nil
}

// PlatformSettings implements app.Repo.
func (Repo) PlatformSettings(ctx context.Context, q db.Querier) (map[string]domain.PlatformRow, error) {
	rows, err := q.Query(ctx, `SELECT key, value, policy, version, updated_at FROM config.platform_settings`)
	if err != nil {
		return nil, mapErr(err)
	}
	list, err := collect(rows, scanPlatform)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make(map[string]domain.PlatformRow, len(list))
	for _, r := range list {
		out[r.Key] = r
	}
	return out, nil
}

func scanPlatform(row pgx.Row) (domain.PlatformRow, error) {
	var (
		r   domain.PlatformRow
		pol string
	)
	if err := row.Scan(&r.Key, &r.Value, &pol, &r.Version, &r.UpdatedAt); err != nil {
		return domain.PlatformRow{}, err //nolint:wrapcheck // callers map it
	}
	r.Policy = domain.Policy(pol)
	return r, nil
}

// UpsertPlatformSetting implements app.Repo.
func (Repo) UpsertPlatformSetting(ctx context.Context, q app.Q, r domain.PlatformRow) (domain.PlatformRow, bool, error) {
	prev, err := scanPlatform(q.QueryRow(ctx, `SELECT key, value, policy, version, updated_at FROM config.platform_settings WHERE key = $1 FOR UPDATE`, r.Key))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		_, err := q.Exec(ctx, `INSERT INTO config.platform_settings (key, value, policy, version, updated_at) VALUES ($1, $2, $3, 1, $4)`,
			r.Key, string(r.Value), string(r.Policy), r.UpdatedAt)
		return domain.PlatformRow{}, false, mapErrIf(err)
	case err != nil:
		return domain.PlatformRow{}, false, mapErr(err)
	}
	_, err = q.Exec(ctx, `UPDATE config.platform_settings SET value = $2, policy = $3, version = version + 1, updated_at = $4 WHERE key = $1`,
		r.Key, string(r.Value), string(r.Policy), r.UpdatedAt)
	return prev, true, mapErrIf(err)
}

// LockEnvironment implements app.Repo.
func (Repo) LockEnvironment(ctx context.Context, q app.Q, tenant ids.TenantID, env ids.EnvironmentID) error {
	_, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('config.publish:' || $1::text || ':' || $2::text, 0))`,
		tenant.UUID().String(), env.UUID().String())
	return mapErrIf(err)
}

// MaxBundleVersion implements app.Repo.
func (Repo) MaxBundleVersion(ctx context.Context, q db.Querier, tenant ids.TenantID, env ids.EnvironmentID) (int64, error) {
	var n int64
	if err := q.QueryRow(ctx, `SELECT coalesce(max(version), 0) FROM config.bundles WHERE tenant_id = $1 AND environment_id = $2`,
		tenant.UUID(), env.UUID()).Scan(&n); err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}

// InsertBundle implements app.Repo.
func (Repo) InsertBundle(ctx context.Context, q app.Q, tenant ids.TenantID, env ids.EnvironmentID, b domain.BundleRow) error {
	_, err := q.Exec(ctx, `INSERT INTO config.bundles (tenant_id, environment_id, version, checksum, payload, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		tenant.UUID(), env.UUID(), b.Version, b.Checksum, string(b.Payload), b.CreatedAt)
	return mapErrIf(err)
}

// InsertPublication implements app.Repo.
func (Repo) InsertPublication(ctx context.Context, q app.Q, tenant ids.TenantID, p domain.Publication) error {
	_, err := q.Exec(ctx, `INSERT INTO config.publications (tenant_id, environment_id, bundle_version, published_at, published_by, change_note)
		VALUES ($1, $2, $3, $4, $5, $6)`, tenant.UUID(), p.EnvironmentID.UUID(), p.BundleVersion, p.PublishedAt, p.PublishedBy, p.ChangeNote)
	return mapErrIf(err)
}

// LatestBundle implements app.Repo.
func (Repo) LatestBundle(ctx context.Context, q db.Querier, tenant ids.TenantID, env ids.EnvironmentID) (domain.BundleRow, bool, error) {
	var b domain.BundleRow
	err := q.QueryRow(ctx, `SELECT version, checksum, payload, created_at FROM config.bundles
		WHERE tenant_id = $1 AND environment_id = $2 ORDER BY version DESC LIMIT 1`, tenant.UUID(), env.UUID()).
		Scan(&b.Version, &b.Checksum, &b.Payload, &b.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BundleRow{}, false, nil
	}
	if err != nil {
		return domain.BundleRow{}, false, mapErr(err)
	}
	return b, true, nil
}
