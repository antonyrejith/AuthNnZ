package app

import (
	"context"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/tenancy/internal/domain"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

// ListFilter selects tenants, ordered by (created_at, id).
type ListFilter struct {
	PartnerID ids.PartnerID
	// Status limits the list to one status; empty lists every tenant that is
	// not deleted.
	Status string
	// After holds the key values (created_at RFC 3339, id) of the last row of
	// the previous page; nil for the first page.
	After []string
	Limit int
}

// TenantRecords is everything the tenancy module stores about a tenant.
type TenantRecords struct {
	Tenant       domain.Tenant
	Environments []domain.Environment
	Hosts        []domain.Host
}

// Repository is the persistence port. Methods with a transaction write (or
// lock) inside the audited transaction of the caller; the others read in
// their own read-only transaction. A missing row is errs.CodeNotFound, a
// unique violation errs.CodeConflict.
type Repository interface {
	// PartnerDepth returns the number of partners from id up to the root, or
	// 0 when the partner does not exist.
	PartnerDepth(ctx context.Context, tx *audit.Tx, id ids.PartnerID) (int, error)
	InsertPartner(ctx context.Context, tx *audit.Tx, p domain.Partner) error
	InsertTenant(ctx context.Context, tx *audit.Tx, t domain.Tenant) error
	SetKEKRef(ctx context.Context, tx *audit.Tx, id ids.TenantID, ref string) error
	InsertEnvironment(ctx context.Context, tx *audit.Tx, e domain.Environment) error
	EnvironmentExists(ctx context.Context, tx *audit.Tx, tenant ids.TenantID, env ids.EnvironmentID) error
	InsertHost(ctx context.Context, tx *audit.Tx, h domain.Host) error
	DeleteHost(ctx context.Context, tx *audit.Tx, host string) (domain.Host, error)
	DeleteHostsOf(ctx context.Context, tx *audit.Tx, tenant ids.TenantID) ([]domain.Host, error)
	// LockTenant reads the tenant FOR UPDATE.
	LockTenant(ctx context.Context, tx *audit.Tx, id ids.TenantID) (domain.Tenant, error)
	// UpdateTenant writes name, plan, status, deleted_at, version and
	// updated_at.
	UpdateTenant(ctx context.Context, tx *audit.Tx, t domain.Tenant) error
	InsertOperation(ctx context.Context, tx *audit.Tx, o domain.Operation) error
	UpdateOperation(ctx context.Context, tx *audit.Tx, o domain.Operation) error

	GetTenant(ctx context.Context, id ids.TenantID) (domain.Tenant, error)
	GetOperation(ctx context.Context, id ids.OperationID) (domain.Operation, error)
	ListTenants(ctx context.Context, f ListFilter) ([]domain.Tenant, error)
	// ResolveHost returns the host's target and its tenant in any status
	// except deleted.
	ResolveHost(ctx context.Context, host string) (domain.Host, domain.Tenant, error)
	TenantRecords(ctx context.Context, id ids.TenantID) (TenantRecords, error)
}
