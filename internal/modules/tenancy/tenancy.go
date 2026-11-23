// Package tenancy is the tenancy module's facade (ADR-0002, ADR-0016):
// partners, tenants, environments, tenant lifecycle (provisioning, suspend,
// resume, crypto-shredding delete), tenant export and tenant resolution by
// host name.
//
// # Audit
//
// Every write is one audit.Auditor.PlatformChange: the rows, the audit
// records and the outbox messages commit together, and every row written is
// declared in a record (ADR-0008). The module's records are on the platform
// chain. Register the event types with RegisterEvents.
//
// # Tenant resolution
//
// ResolveHost maps the request's host name to a tenant and environment. A
// positive result is cached (1024 entries, 30 seconds). A change made through
// this Module drops the cache at once; another instance's change is seen
// after at most the time-to-live. Middleware sets the scope of the request
// from r.Host and never trusts forwarding headers.
//
// # Deleting a tenant (documented limit of B0)
//
// DeleteTenant destroys the tenant's key-encryption key (crypto-shredding,
// TEN-09) and removes its hosts, so the tenant's encrypted data and its audit
// records' personal data become unreadable and its hosts stop resolving. The
// business rows of other modules are not purged: they stay in the database,
// readable where they are not encrypted, until a later task adds purge hooks
// for the modules that own data. The tenancy rows themselves are kept
// (status "deleted") as a tombstone.
//
// # Export
//
// StartExport records an operation and writes the export file in the
// background; poll GetOperation. A failed or interrupted completion record
// leaves the operation "running" (the failure is logged). Call Close at
// shutdown to wait for running exports.
package tenancy

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/tenancy/internal/adapters/postgres"
	"github.com/antonyrejith/portun/internal/modules/tenancy/internal/app"
	"github.com/antonyrejith/portun/internal/modules/tenancy/internal/domain"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/db/page"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/httpx"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/keystore"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// Statuses and kinds.
const (
	PartnerActive    = domain.PartnerActive
	PartnerSuspended = domain.PartnerSuspended

	TenantActive    = domain.TenantActive
	TenantSuspended = domain.TenantSuspended
	TenantDeleting  = domain.TenantDeleting
	TenantDeleted   = domain.TenantDeleted

	KindDevelopment = domain.KindDevelopment
	KindStaging     = domain.KindStaging
	KindProduction  = domain.KindProduction

	OperationExport    = domain.OperationExport
	OperationRunning   = domain.OperationRunning
	OperationSucceeded = domain.OperationSucceeded
	OperationFailed    = domain.OperationFailed
)

// Schema is the module's PostgreSQL schema.
const Schema = postgres.Schema

// Migrations returns the module's migration set.
func Migrations() migrate.Set { return postgres.Migrations() }

// Exporter writes one module's data of a tenant to w as JSON lines. The
// context carries the tenant's scope (without an environment).
type Exporter = app.Exporter

// Deps are the Module's dependencies. All are required except Clock and
// Logger.
type Deps struct {
	DB      *db.DB
	Auditor *audit.Auditor
	Keys    *keystore.Store
	// BaseDomain is the domain of the default hosts
	// (<slug>-<environment>.<BaseDomain>).
	BaseDomain string
	// ExportDir is the directory of tenant export files.
	ExportDir string
	Clock     func() time.Time
	Logger    *slog.Logger
}

// Module is the tenancy module.
type Module struct {
	svc *app.Service
}

// New returns a Module. It fails with errs.CodeInternal when a dependency is
// missing or the base domain is invalid.
func New(d Deps) (*Module, error) {
	if d.DB == nil {
		return nil, errs.New(errs.CodeInternal, "tenancy: DB is required")
	}
	svc, err := app.New(app.Deps{
		Repo: postgres.New(d.DB), Auditor: d.Auditor, Keys: d.Keys, BaseDomain: d.BaseDomain,
		ExportDir: d.ExportDir, Clock: d.Clock, Logger: d.Logger,
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // already an *errs.Error
	}
	return &Module{svc: svc}, nil
}

// Partner is a reseller or MSP that owns tenants.
type Partner struct {
	ID        ids.PartnerID
	ParentID  ids.PartnerID
	Name      string
	Status    string
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Tenant is a customer of the platform.
type Tenant struct {
	ID        ids.TenantID
	PartnerID ids.PartnerID
	Slug      string
	Name      string
	Status    string
	Region    string
	Plan      string
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Environment is one of a tenant's environments.
type Environment struct {
	ID        ids.EnvironmentID
	TenantID  ids.TenantID
	Name      string
	Kind      string
	Version   int64
	CreatedAt time.Time
}

// Operation is a long-running operation on a tenant.
type Operation struct {
	ID         ids.OperationID
	TenantID   ids.TenantID
	Kind       string
	Status     string
	ResultPath string
	ErrorCode  errs.Code
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func partnerOf(p domain.Partner) Partner {
	return Partner{ID: p.ID, ParentID: p.ParentID, Name: p.Name, Status: p.Status, Version: p.Version, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

func tenantOf(t domain.Tenant) Tenant {
	return Tenant{
		ID: t.ID, PartnerID: t.PartnerID, Slug: t.Slug, Name: t.Name, Status: t.Status, Region: t.Region, Plan: t.Plan,
		Version: t.Version, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

func environmentsOf(in []domain.Environment) []Environment {
	out := make([]Environment, 0, len(in))
	for _, e := range in {
		out = append(out, Environment{ID: e.ID, TenantID: e.TenantID, Name: e.Name, Kind: e.Kind, Version: e.Version, CreatedAt: e.CreatedAt})
	}
	return out
}

func operationOf(o domain.Operation) Operation {
	return Operation{
		ID: o.ID, TenantID: o.TenantID, Kind: o.Kind, Status: o.Status, ResultPath: o.ResultPath, ErrorCode: o.ErrorCode,
		CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
	}
}

// CreatePartnerInput is the input of CreatePartner.
type CreatePartnerInput struct {
	Name     string
	ParentID ids.PartnerID
}

// CreatePartner creates a partner, nested under ParentID (at most three
// levels).
func (m *Module) CreatePartner(ctx context.Context, in CreatePartnerInput) (Partner, error) {
	p, err := m.svc.CreatePartner(ctx, in.Name, in.ParentID)
	return partnerOf(p), err
}

// EnvironmentInput is a requested environment.
type EnvironmentInput struct {
	Name string
	Kind string
}

// CreateTenantInput is the input of CreateTenant.
type CreateTenantInput struct {
	Slug, Name, Region, Plan string
	PartnerID                ids.PartnerID
	// Environments defaults to development, staging and production, each
	// with its name as kind.
	Environments []EnvironmentInput
}

// CreateTenant provisions a tenant with its environments, default hosts and
// key-encryption key.
func (m *Module) CreateTenant(ctx context.Context, in CreateTenantInput) (Tenant, []Environment, error) {
	spec := domain.TenantSpec{Slug: in.Slug, Name: in.Name, Region: in.Region, Plan: in.Plan}
	for _, e := range in.Environments {
		spec.Environments = append(spec.Environments, domain.EnvSpec{Name: e.Name, Kind: e.Kind})
	}
	t, envs, err := m.svc.CreateTenant(ctx, spec, in.PartnerID)
	if err != nil {
		return Tenant{}, nil, err //nolint:wrapcheck // already an *errs.Error
	}
	return tenantOf(t), environmentsOf(envs), nil
}

// GetTenant returns a tenant; a deleted tenant is errs.CodeNotFound.
func (m *Module) GetTenant(ctx context.Context, id ids.TenantID) (Tenant, error) {
	t, err := m.svc.GetTenant(ctx, id)
	return tenantOf(t), err
}

// ListEnvironments returns the environments of a tenant; a deleted tenant is
// errs.CodeNotFound.
func (m *Module) ListEnvironments(ctx context.Context, id ids.TenantID) ([]Environment, error) {
	envs, err := m.svc.ListEnvironments(ctx, id)
	if err != nil {
		return nil, err //nolint:wrapcheck // already an *errs.Error
	}
	return environmentsOf(envs), nil
}

// ListTenantsInput selects tenants.
type ListTenantsInput struct {
	PartnerID ids.PartnerID
	// Status limits the list to one status; by default deleted tenants are
	// not listed.
	Status string
	Page   page.Request
}

// ListTenants returns a page of tenants ordered by creation, and the token of
// the next page ("" on the last).
func (m *Module) ListTenants(ctx context.Context, in ListTenantsInput) ([]Tenant, string, error) {
	rows, next, err := m.svc.ListTenants(ctx, app.ListTenantsInput{PartnerID: in.PartnerID, Status: in.Status, Page: in.Page})
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already an *errs.Error
	}
	out := make([]Tenant, 0, len(rows))
	for _, t := range rows {
		out = append(out, tenantOf(t))
	}
	return out, next, nil
}

// UpdateTenantInput holds the fields to change; nil fields stay.
type UpdateTenantInput struct {
	Name *string
	Plan *string
}

// UpdateTenant changes a tenant's name or plan. A stale expectedVersion is
// errs.CodePreconditionFailed.
func (m *Module) UpdateTenant(ctx context.Context, id ids.TenantID, expectedVersion int64, in UpdateTenantInput) (Tenant, error) {
	t, err := m.svc.UpdateTenant(ctx, id, expectedVersion, app.UpdateTenantInput{Name: in.Name, Plan: in.Plan})
	return tenantOf(t), err
}

// SuspendTenant suspends an active tenant. The reason is required.
func (m *Module) SuspendTenant(ctx context.Context, id ids.TenantID, expectedVersion int64, reason string) (Tenant, error) {
	t, err := m.svc.SuspendTenant(ctx, id, expectedVersion, reason)
	return tenantOf(t), err
}

// ResumeTenant resumes a suspended tenant. The reason is required.
func (m *Module) ResumeTenant(ctx context.Context, id ids.TenantID, expectedVersion int64, reason string) (Tenant, error) {
	t, err := m.svc.ResumeTenant(ctx, id, expectedVersion, reason)
	return tenantOf(t), err
}

// DeleteTenant deletes a tenant and destroys its key-encryption key
// (crypto-shredding). The reason is required. See the package documentation
// for what is not purged.
func (m *Module) DeleteTenant(ctx context.Context, id ids.TenantID, expectedVersion int64, reason string) (Tenant, error) {
	t, err := m.svc.DeleteTenant(ctx, id, expectedVersion, reason)
	return tenantOf(t), err
}

// StartExport starts the export of a tenant's data and returns the running
// operation (poll GetOperation).
func (m *Module) StartExport(ctx context.Context, id ids.TenantID) (Operation, error) {
	o, err := m.svc.StartExport(ctx, id)
	return operationOf(o), err
}

// GetOperation returns an operation.
func (m *Module) GetOperation(ctx context.Context, id ids.OperationID) (Operation, error) {
	o, err := m.svc.GetOperation(ctx, id)
	return operationOf(o), err
}

// RegisterExporter adds a module's exporter to tenant exports.
func (m *Module) RegisterExporter(name string, fn Exporter) error {
	return m.svc.RegisterExporter(name, fn) //nolint:wrapcheck // already an *errs.Error
}

// Close stops accepting exports and waits for the running ones; when ctx is
// done first they are canceled.
func (m *Module) Close(ctx context.Context) error {
	return m.svc.Close(ctx) //nolint:wrapcheck // already an *errs.Error
}

// ResolveHost returns the tenant scope and the tenant of a host name (lower
// cased; a port and one trailing dot are ignored). An unknown host is
// errs.CodeNotFound; a suspended tenant is errs.CodePermissionDenied.
func (m *Module) ResolveHost(ctx context.Context, host string) (scope.Tenant, Tenant, error) {
	h, t, err := m.svc.ResolveHost(ctx, host)
	if err != nil {
		return scope.Tenant{}, Tenant{}, err //nolint:wrapcheck // already an *errs.Error
	}
	return scope.Tenant{TenantID: h.TenantID, EnvironmentID: h.EnvironmentID}, tenantOf(t), nil
}

// AddHost registers a host name for a tenant environment.
func (m *Module) AddHost(ctx context.Context, tenant ids.TenantID, env ids.EnvironmentID, host string) error {
	return m.svc.AddHost(ctx, tenant, env, host) //nolint:wrapcheck // already an *errs.Error
}

// RemoveHost unregisters a host name.
func (m *Module) RemoveHost(ctx context.Context, host string) error {
	return m.svc.RemoveHost(ctx, host) //nolint:wrapcheck // already an *errs.Error
}

// Middleware resolves the request's host (r.Host; forwarding headers are
// never used) and puts the tenant scope into the request context. Unknown
// hosts get a 404 problem, suspended tenants a 403 problem.
func (m *Module) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			st, _, err := m.ResolveHost(r.Context(), r.Host)
			if err != nil {
				httpx.WriteProblem(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(scope.WithTenant(r.Context(), st)))
		})
	}
}
