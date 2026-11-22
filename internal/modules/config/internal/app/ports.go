package app

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/antonyrejith/portun/internal/modules/config/internal/domain"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

// Q is a database transaction: a pgx.Tx or the audit module's *audit.Tx.
type Q interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Repo is the storage the use cases need. Every method runs in the
// transaction q; the tenant and environment come from the transaction's
// scope (row-level security) and are also passed explicitly so a statement
// never depends on it alone. Reads that find nothing return errs.CodeNotFound
// or ok == false as documented.
type Repo interface {
	// FindObject returns the object of an environment by kind and name.
	FindObject(ctx context.Context, q db.Querier, tenant ids.TenantID, env ids.EnvironmentID, kind, name string) (domain.Object, bool, error)
	// GetObject returns an object of the environment; with lock it locks the
	// row until the transaction ends. Missing: errs.CodeNotFound.
	GetObject(ctx context.Context, q db.Querier, tenant ids.TenantID, env ids.EnvironmentID, id ids.ConfigObjectID, lock bool) (domain.Object, error)
	InsertObject(ctx context.Context, q Q, tenant ids.TenantID, o domain.Object) error
	// UpdateObject writes the object's current version, status, etag and
	// update time.
	UpdateObject(ctx context.Context, q Q, tenant ids.TenantID, o domain.Object) error

	InsertVersion(ctx context.Context, q Q, tenant ids.TenantID, v domain.Version) error
	// GetVersion returns one version. Missing: errs.CodeNotFound.
	GetVersion(ctx context.Context, q db.Querier, tenant ids.TenantID, id ids.ConfigObjectID, version int) (domain.Version, error)
	// OpenDraft returns the object's draft, if it has one.
	OpenDraft(ctx context.Context, q db.Querier, tenant ids.TenantID, id ids.ConfigObjectID) (domain.Version, bool, error)
	// MaxVersion returns the highest version number of the object, 0 if none.
	MaxVersion(ctx context.Context, q db.Querier, tenant ids.TenantID, id ids.ConfigObjectID) (int, error)
	UpdateDraft(ctx context.Context, q Q, tenant ids.TenantID, v domain.Version) error
	MarkPublished(ctx context.Context, q Q, tenant ids.TenantID, id ids.ConfigObjectID, version int, at time.Time) error
	// ListVersions returns up to limit versions below after (all when after is
	// 0), newest first.
	ListVersions(ctx context.Context, q db.Querier, tenant ids.TenantID, id ids.ConfigObjectID, after, limit int) ([]domain.Version, error)
	// CurrentSpecs returns the published spec in force of every active object
	// of the environment.
	CurrentSpecs(ctx context.Context, q db.Querier, tenant ids.TenantID, env ids.EnvironmentID) ([]domain.CurrentSpec, error)

	// SettingRows returns the tenant-level rows and the rows of env.
	SettingRows(ctx context.Context, q db.Querier, tenant ids.TenantID, env ids.EnvironmentID) ([]domain.SettingRow, error)
	// GetSettingRow returns one row, locking it when lock is set.
	GetSettingRow(ctx context.Context, q db.Querier, tenant ids.TenantID, levelEnv ids.EnvironmentID, key string, lock bool) (domain.SettingRow, bool, error)
	InsertSettingRow(ctx context.Context, q Q, tenant ids.TenantID, r domain.SettingRow) error
	UpdateSettingRow(ctx context.Context, q Q, tenant ids.TenantID, r domain.SettingRow) error
	PlatformSettings(ctx context.Context, q db.Querier) (map[string]domain.PlatformRow, error)
	// UpsertPlatformSetting inserts or updates a platform row and returns the
	// previous row, if any.
	UpsertPlatformSetting(ctx context.Context, q Q, r domain.PlatformRow) (prev domain.PlatformRow, existed bool, err error)

	// LockEnvironment serializes publishes of one environment until the
	// transaction ends.
	LockEnvironment(ctx context.Context, q Q, tenant ids.TenantID, env ids.EnvironmentID) error
	MaxBundleVersion(ctx context.Context, q db.Querier, tenant ids.TenantID, env ids.EnvironmentID) (int64, error)
	InsertBundle(ctx context.Context, q Q, tenant ids.TenantID, env ids.EnvironmentID, b domain.BundleRow) error
	InsertPublication(ctx context.Context, q Q, tenant ids.TenantID, p domain.Publication) error
	// LatestBundle returns the environment's newest bundle, if any.
	LatestBundle(ctx context.Context, q db.Querier, tenant ids.TenantID, env ids.EnvironmentID) (domain.BundleRow, bool, error)
}
