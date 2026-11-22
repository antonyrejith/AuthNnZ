// Package postgres is the PostgreSQL adapter of the tenancy module: the
// migrations of schema tenancy and the repository behind the application
// layer's ports.
//
// Writes run in the audited transaction the caller passes (*audit.Tx);
// reads open their own read-only platform transaction (tenancy tables are
// platform-level and read before a tenant is known).
package postgres

import (
	"context"
	"embed"
	"io/fs"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/tenancy/internal/app"
	"github.com/antonyrejith/portun/internal/modules/tenancy/internal/domain"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

// Schema is the PostgreSQL schema of the module.
const Schema = "tenancy"

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations returns the schema's migration set.
func Migrations() migrate.Set {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic("tenancy: embedded migrations missing: " + err.Error())
	}
	return migrate.Set{Schema: Schema, FS: sub}
}

// Repository implements app.Repository.
type Repository struct {
	db *db.DB
}

var _ app.Repository = (*Repository)(nil)

// New returns a repository on d.
func New(d *db.DB) *Repository { return &Repository{db: d} }

// mapErr maps a database error to an errs code.
func mapErr(err error) error {
	return db.MapError(err) //nolint:wrapcheck // db.MapError wraps in an *errs.Error
}

// read runs fn in a read-only platform transaction.
func (r *Repository) read(ctx context.Context, fn func(ctx context.Context, q db.Querier) error) error {
	return r.db.ReadPlatform(ctx, fn) //nolint:wrapcheck // db maps errors to *errs.Error
}

// nullUUID returns the argument for a nullable uuid column.
func nullUUID(u uuid.UUID) any {
	if u == uuid.Nil {
		return nil
	}
	return u
}

func fromNullable[K ids.Kind](u *uuid.UUID) ids.ID[K] {
	if u == nil {
		return ids.ID[K]{}
	}
	return ids.FromUUID[K](*u)
}

const tenantColumns = `id, partner_id, slug, name, status, region, plan, kek_ref, version, created_at, updated_at, deleted_at`

func scanTenant(row pgx.Row) (domain.Tenant, error) {
	var (
		t       domain.Tenant
		id      uuid.UUID
		partner *uuid.UUID
		kek     *string
	)
	if err := row.Scan(&id, &partner, &t.Slug, &t.Name, &t.Status, &t.Region, &t.Plan, &kek, &t.Version, &t.CreatedAt, &t.UpdatedAt, &t.DeletedAt); err != nil {
		return domain.Tenant{}, mapErr(err)
	}
	t.ID = ids.FromUUID[ids.TenantKind](id)
	t.PartnerID = fromNullable[ids.PartnerKind](partner)
	if kek != nil {
		t.KEKRef = *kek
	}
	t.CreatedAt, t.UpdatedAt = t.CreatedAt.UTC(), t.UpdatedAt.UTC()
	if t.DeletedAt != nil {
		d := t.DeletedAt.UTC()
		t.DeletedAt = &d
	}
	return t, nil
}

func scanOperation(row pgx.Row) (domain.Operation, error) {
	var (
		o      domain.Operation
		id, tn uuid.UUID
		path   *string
		code   *string
	)
	if err := row.Scan(&id, &tn, &o.Kind, &o.Status, &path, &code, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return domain.Operation{}, mapErr(err)
	}
	o.ID, o.TenantID = ids.FromUUID[ids.OperationKind](id), ids.FromUUID[ids.TenantKind](tn)
	if path != nil {
		o.ResultPath = *path
	}
	if code != nil {
		o.ErrorCode = errs.Code(*code)
	}
	o.CreatedAt, o.UpdatedAt = o.CreatedAt.UTC(), o.UpdatedAt.UTC()
	return o, nil
}

const operationColumns = `id, owner_tenant_id, kind, status, result_path, error_code, created_at, updated_at`

// PartnerDepth implements app.Repository.
func (r *Repository) PartnerDepth(ctx context.Context, tx *audit.Tx, id ids.PartnerID) (int, error) {
	var depth *int
	err := tx.QueryRow(ctx, `
WITH RECURSIVE chain AS (
    SELECT id, parent_id, 1 AS depth FROM tenancy.partners WHERE id = $1
    UNION ALL
    SELECT p.id, p.parent_id, c.depth + 1 FROM tenancy.partners p JOIN chain c ON p.id = c.parent_id
)
SELECT max(depth) FROM chain`, id.UUID()).Scan(&depth)
	if err != nil {
		return 0, mapErr(err)
	}
	if depth == nil {
		return 0, nil
	}
	return *depth, nil
}

// InsertPartner implements app.Repository.
func (r *Repository) InsertPartner(ctx context.Context, tx *audit.Tx, p domain.Partner) error {
	_, err := tx.Exec(ctx, `INSERT INTO tenancy.partners (id, parent_id, name, status, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)`, p.ID.UUID(), nullUUID(p.ParentID.UUID()), p.Name, p.Status, p.Version, p.CreatedAt, p.UpdatedAt)
	return mapErr(err)
}

// InsertTenant implements app.Repository.
func (r *Repository) InsertTenant(ctx context.Context, tx *audit.Tx, t domain.Tenant) error {
	_, err := tx.Exec(ctx, `INSERT INTO tenancy.tenants (id, partner_id, slug, name, status, region, plan, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		t.ID.UUID(), nullUUID(t.PartnerID.UUID()), t.Slug, t.Name, t.Status, t.Region, t.Plan, t.Version, t.CreatedAt, t.UpdatedAt)
	return mapErr(err)
}

// SetKEKRef implements app.Repository.
func (r *Repository) SetKEKRef(ctx context.Context, tx *audit.Tx, id ids.TenantID, ref string) error {
	_, err := tx.Exec(ctx, `UPDATE tenancy.tenants SET kek_ref = $2 WHERE id = $1`, id.UUID(), ref)
	return mapErr(err)
}

// InsertEnvironment implements app.Repository.
func (r *Repository) InsertEnvironment(ctx context.Context, tx *audit.Tx, e domain.Environment) error {
	_, err := tx.Exec(ctx, `INSERT INTO tenancy.environments (tenant_id, id, name, kind, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)`, e.TenantID.UUID(), e.ID.UUID(), e.Name, e.Kind, e.Version, e.CreatedAt, e.UpdatedAt)
	return mapErr(err)
}

// EnvironmentExists implements app.Repository.
func (r *Repository) EnvironmentExists(ctx context.Context, tx *audit.Tx, tenant ids.TenantID, env ids.EnvironmentID) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM tenancy.environments WHERE tenant_id = $1 AND id = $2`, tenant.UUID(), env.UUID()).Scan(&one)
	return mapErr(err)
}

// InsertHost implements app.Repository.
func (r *Repository) InsertHost(ctx context.Context, tx *audit.Tx, h domain.Host) error {
	_, err := tx.Exec(ctx, `INSERT INTO tenancy.hosts (host, owner_tenant_id, environment_id, created_at) VALUES ($1, $2, $3, $4)`,
		h.Host, h.TenantID.UUID(), h.EnvironmentID.UUID(), h.CreatedAt)
	return mapErr(err)
}

// DeleteHost implements app.Repository.
func (r *Repository) DeleteHost(ctx context.Context, tx *audit.Tx, host string) (domain.Host, error) {
	var (
		h      domain.Host
		tn, en uuid.UUID
	)
	err := tx.QueryRow(ctx, `DELETE FROM tenancy.hosts WHERE host = $1 RETURNING host, owner_tenant_id, environment_id, created_at`, host).
		Scan(&h.Host, &tn, &en, &h.CreatedAt)
	if err != nil {
		return domain.Host{}, mapErr(err)
	}
	h.TenantID, h.EnvironmentID, h.CreatedAt = ids.FromUUID[ids.TenantKind](tn), ids.FromUUID[ids.EnvironmentKind](en), h.CreatedAt.UTC()
	return h, nil
}

// DeleteHostsOf implements app.Repository.
func (r *Repository) DeleteHostsOf(ctx context.Context, tx *audit.Tx, tenant ids.TenantID) ([]domain.Host, error) {
	rows, err := tx.Query(ctx, `DELETE FROM tenancy.hosts WHERE owner_tenant_id = $1
RETURNING host, owner_tenant_id, environment_id, created_at`, tenant.UUID())
	if err != nil {
		return nil, mapErr(err)
	}
	hosts, err := collectHosts(rows)
	if err != nil {
		return nil, err
	}
	return hosts, nil
}

func collectHosts(rows pgx.Rows) ([]domain.Host, error) {
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Host, error) {
		var (
			h      domain.Host
			tn, en uuid.UUID
		)
		if err := r.Scan(&h.Host, &tn, &en, &h.CreatedAt); err != nil {
			return domain.Host{}, mapErr(err)
		}
		h.TenantID, h.EnvironmentID, h.CreatedAt = ids.FromUUID[ids.TenantKind](tn), ids.FromUUID[ids.EnvironmentKind](en), h.CreatedAt.UTC()
		return h, nil
	})
	return out, mapErr(err)
}

// LockTenant implements app.Repository.
func (r *Repository) LockTenant(ctx context.Context, tx *audit.Tx, id ids.TenantID) (domain.Tenant, error) {
	return scanTenant(tx.QueryRow(ctx, `SELECT `+tenantColumns+` FROM tenancy.tenants WHERE id = $1 FOR UPDATE`, id.UUID()))
}

// UpdateTenant implements app.Repository.
func (r *Repository) UpdateTenant(ctx context.Context, tx *audit.Tx, t domain.Tenant) error {
	tag, err := tx.Exec(ctx, `UPDATE tenancy.tenants SET name = $2, plan = $3, status = $4, deleted_at = $5, version = $6, updated_at = $7
WHERE id = $1`, t.ID.UUID(), t.Name, t.Plan, t.Status, t.DeletedAt, t.Version, t.UpdatedAt)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() != 1 {
		return errs.New(errs.CodeNotFound, "tenant not found")
	}
	return nil
}

// InsertOperation implements app.Repository.
func (r *Repository) InsertOperation(ctx context.Context, tx *audit.Tx, o domain.Operation) error {
	_, err := tx.Exec(ctx, `INSERT INTO tenancy.operations (`+operationColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		o.ID.UUID(), o.TenantID.UUID(), o.Kind, o.Status, nullString(o.ResultPath), nullString(string(o.ErrorCode)), o.CreatedAt, o.UpdatedAt)
	return mapErr(err)
}

// UpdateOperation implements app.Repository.
func (r *Repository) UpdateOperation(ctx context.Context, tx *audit.Tx, o domain.Operation) error {
	tag, err := tx.Exec(ctx, `UPDATE tenancy.operations SET status = $2, result_path = $3, error_code = $4, updated_at = $5 WHERE id = $1`,
		o.ID.UUID(), o.Status, nullString(o.ResultPath), nullString(string(o.ErrorCode)), o.UpdatedAt)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() != 1 {
		return errs.New(errs.CodeNotFound, "operation not found")
	}
	return nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// GetTenant implements app.Repository.
func (r *Repository) GetTenant(ctx context.Context, id ids.TenantID) (domain.Tenant, error) {
	var t domain.Tenant
	err := r.read(ctx, func(ctx context.Context, tx db.Querier) (err error) {
		t, err = scanTenant(tx.QueryRow(ctx, `SELECT `+tenantColumns+` FROM tenancy.tenants WHERE id = $1`, id.UUID()))
		return err
	})
	return t, err
}

// GetOperation implements app.Repository.
func (r *Repository) GetOperation(ctx context.Context, id ids.OperationID) (domain.Operation, error) {
	var o domain.Operation
	err := r.read(ctx, func(ctx context.Context, tx db.Querier) (err error) {
		o, err = scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM tenancy.operations WHERE id = $1`, id.UUID()))
		return err
	})
	return o, err
}

// ListTenants implements app.Repository: tenants ordered by (created_at,
// id), after the key values in f.After.
func (r *Repository) ListTenants(ctx context.Context, f app.ListFilter) ([]domain.Tenant, error) {
	var (
		afterTime any
		afterID   any
	)
	if len(f.After) == 2 {
		ts, err := time.Parse(time.RFC3339Nano, f.After[0])
		if err != nil {
			return nil, errs.New(errs.CodeInvalidRequest, "invalid page_token")
		}
		id, err := uuid.Parse(f.After[1])
		if err != nil {
			return nil, errs.New(errs.CodeInvalidRequest, "invalid page_token")
		}
		afterTime, afterID = ts, id
	}
	var out []domain.Tenant
	err := r.read(ctx, func(ctx context.Context, tx db.Querier) error {
		rows, err := tx.Query(ctx, `SELECT `+tenantColumns+` FROM tenancy.tenants
WHERE ($1::uuid IS NULL OR partner_id = $1)
  AND (($2 = '' AND status <> 'deleted') OR status = $2)
  AND ($3::timestamptz IS NULL OR (created_at, id) > ($3, $4::uuid))
ORDER BY created_at, id LIMIT $5`, nullUUID(f.PartnerID.UUID()), f.Status, afterTime, afterID, f.Limit)
		if err != nil {
			return mapErr(err)
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Tenant, error) { return scanTenant(row) })
		return mapErr(err)
	})
	return out, err
}

// ResolveHost implements app.Repository. It returns the tenant of a host in
// any status except deleted.
func (r *Repository) ResolveHost(ctx context.Context, host string) (domain.Host, domain.Tenant, error) {
	var (
		h domain.Host
		t domain.Tenant
	)
	err := r.read(ctx, func(ctx context.Context, tx db.Querier) error {
		var tn, en uuid.UUID
		err := tx.QueryRow(ctx, `SELECT owner_tenant_id, environment_id, created_at FROM tenancy.hosts WHERE host = $1`, host).Scan(&tn, &en, &h.CreatedAt)
		if err != nil {
			return mapErr(err)
		}
		h.Host, h.TenantID, h.EnvironmentID, h.CreatedAt = host, ids.FromUUID[ids.TenantKind](tn), ids.FromUUID[ids.EnvironmentKind](en), h.CreatedAt.UTC()
		t, err = scanTenant(tx.QueryRow(ctx, `SELECT `+tenantColumns+` FROM tenancy.tenants WHERE id = $1`, tn))
		return err
	})
	return h, t, err
}

// TenantRecords implements app.Repository: the tenant with its environments
// and hosts.
func (r *Repository) TenantRecords(ctx context.Context, id ids.TenantID) (app.TenantRecords, error) {
	var rec app.TenantRecords
	err := r.read(ctx, func(ctx context.Context, tx db.Querier) error {
		t, err := scanTenant(tx.QueryRow(ctx, `SELECT `+tenantColumns+` FROM tenancy.tenants WHERE id = $1`, id.UUID()))
		if err != nil {
			return err
		}
		rec.Tenant = t
		rows, err := tx.Query(ctx, `SELECT tenant_id, id, name, kind, version, created_at, updated_at
FROM tenancy.environments WHERE tenant_id = $1 ORDER BY created_at, id`, id.UUID())
		if err != nil {
			return mapErr(err)
		}
		rec.Environments, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Environment, error) {
			var (
				e      domain.Environment
				tn, en uuid.UUID
			)
			if err := r.Scan(&tn, &en, &e.Name, &e.Kind, &e.Version, &e.CreatedAt, &e.UpdatedAt); err != nil {
				return e, mapErr(err)
			}
			e.TenantID, e.ID = ids.FromUUID[ids.TenantKind](tn), ids.FromUUID[ids.EnvironmentKind](en)
			e.CreatedAt, e.UpdatedAt = e.CreatedAt.UTC(), e.UpdatedAt.UTC()
			return e, nil
		})
		if err != nil {
			return mapErr(err)
		}
		hrows, err := tx.Query(ctx, `SELECT host, owner_tenant_id, environment_id, created_at FROM tenancy.hosts
WHERE owner_tenant_id = $1 ORDER BY host`, id.UUID())
		if err != nil {
			return mapErr(err)
		}
		rec.Hosts, err = collectHosts(hrows)
		return err
	})
	return rec, err
}
