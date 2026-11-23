// Package app holds the use cases of the tenancy module. Every write is one
// audited platform change (audit.Auditor.PlatformChange): the rows are
// written, the audit records are made and the change commits together, and
// every row written is declared in its record (ADR-0008).
package app

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/tenancy/internal/domain"
	"github.com/antonyrejith/portun/internal/platform/db/page"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/keystore"
)

// Deps are the Service's dependencies. All are required except Clock and
// Logger.
type Deps struct {
	Repo       Repository
	Auditor    *audit.Auditor
	Keys       *keystore.Store
	BaseDomain string
	ExportDir  string
	Clock      func() time.Time
	Logger     *slog.Logger
}

// Service implements the tenancy use cases.
type Service struct {
	repo       Repository
	aud        *audit.Auditor
	keys       *keystore.Store
	baseDomain string
	exportDir  string
	clock      func() time.Time
	log        *slog.Logger
	cache      *hostCache

	mu        sync.Mutex
	exporters map[string]Exporter
	closing   bool
	wg        sync.WaitGroup
	halt      chan struct{} // closed by Close to cancel running exports
	haltOnce  sync.Once
}

// New returns a Service. It fails with errs.CodeInternal when a dependency
// is missing or the base domain is not a DNS name.
func New(d Deps) (*Service, error) {
	if d.Repo == nil || d.Auditor == nil || d.Keys == nil || d.ExportDir == "" {
		return nil, errs.New(errs.CodeInternal, "tenancy: Repo, Auditor, Keys and ExportDir are required")
	}
	if err := domain.ValidateBaseDomain(d.BaseDomain); err != nil {
		return nil, err //nolint:wrapcheck // already an *errs.Error
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	return &Service{
		repo: d.Repo, aud: d.Auditor, keys: d.Keys, baseDomain: d.BaseDomain, exportDir: d.ExportDir,
		clock: d.Clock, log: d.Logger.With(slog.String("component", "tenancy")),
		cache: newHostCache(hostCacheSize, hostCacheTTL), exporters: map[string]Exporter{},
		halt: make(chan struct{}),
	}, nil
}

// now returns the current time in UTC with the microsecond precision of the
// database.
func (s *Service) now() time.Time { return s.clock().UTC().Truncate(time.Microsecond) }

func rowRef(table string, key ...string) audit.RowRef {
	return audit.RowRef{Table: "tenancy." + table, Key: key}
}

func rowRefs(rows []keystore.RowRef) []audit.RowRef {
	out := make([]audit.RowRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, audit.RowRef{Table: r.Table, Key: append([]string(nil), r.Key...)})
	}
	return out
}

func tenantTarget(t domain.Tenant) *audit.Target {
	return &audit.Target{Type: "tenant", ID: t.ID.String(), Name: t.Slug}
}

func fieldError(pointer, code, detail string) error {
	return errs.New(errs.CodeValidationFailed, "the request contains invalid fields").
		WithFields(errs.FieldError{Pointer: pointer, Code: code, Detail: detail})
}

// CreatePartner creates a partner, nested under parent when it is not zero.
func (s *Service) CreatePartner(ctx context.Context, name string, parent ids.PartnerID) (domain.Partner, error) {
	if err := domain.ValidatePartner(name); err != nil {
		return domain.Partner{}, err //nolint:wrapcheck // already an *errs.Error
	}
	id, err := ids.New[ids.PartnerKind]()
	if err != nil {
		return domain.Partner{}, err
	}
	now := s.now()
	p := domain.Partner{ID: id, ParentID: parent, Name: name, Status: domain.PartnerActive, Version: 1, CreatedAt: now, UpdatedAt: now}
	err = s.aud.PlatformChange(ctx, func(ctx context.Context, tx *audit.Tx) error {
		depth := 0
		if !parent.IsZero() {
			if depth, err = s.repo.PartnerDepth(ctx, tx, parent); err != nil {
				return err //nolint:wrapcheck // repository errors are *errs.Error
			}
			if depth == 0 {
				return fieldError("/parent_id", "not_found", "the parent partner does not exist")
			}
		}
		if err := domain.CheckPartnerDepth(depth); err != nil {
			return err //nolint:wrapcheck // already an *errs.Error
		}
		if err := s.repo.InsertPartner(ctx, tx, p); err != nil {
			return err //nolint:wrapcheck // repository errors are *errs.Error
		}
		return tx.Record(audit.Record{
			Type: TypePartnerCreated, Target: &audit.Target{Type: "partner", ID: p.ID.String(), Name: p.Name},
			After: p, AfterVersion: p.Version, Rows: []audit.RowRef{rowRef("partners", p.ID.UUID().String())},
		})
	})
	if err != nil {
		return domain.Partner{}, err //nolint:wrapcheck // *errs.Error from the audited change
	}
	return p, nil
}

// CreateTenant provisions a tenant: the tenant, its environments, their
// default hosts and the tenant's key-encryption key, in one audited change.
func (s *Service) CreateTenant(ctx context.Context, spec domain.TenantSpec, partner ids.PartnerID) (domain.Tenant, []domain.Environment, error) {
	envSpecs, err := domain.ValidateTenant(spec)
	if err != nil {
		return domain.Tenant{}, nil, err //nolint:wrapcheck // already an *errs.Error
	}
	id, err := ids.New[ids.TenantKind]()
	if err != nil {
		return domain.Tenant{}, nil, err
	}
	now := s.now()
	t := domain.Tenant{
		ID: id, PartnerID: partner, Slug: spec.Slug, Name: spec.Name, Status: domain.TenantActive,
		Region: spec.Region, Plan: spec.Plan, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	envs := make([]domain.Environment, 0, len(envSpecs))
	hosts := make([]domain.Host, 0, len(envSpecs))
	for _, e := range envSpecs {
		eid, err := ids.New[ids.EnvironmentKind]()
		if err != nil {
			return domain.Tenant{}, nil, err
		}
		envs = append(envs, domain.Environment{ID: eid, TenantID: id, Name: e.Name, Kind: e.Kind, Version: 1, CreatedAt: now, UpdatedAt: now})
		hosts = append(hosts, domain.Host{Host: domain.DefaultHost(spec.Slug, e.Name, s.baseDomain), TenantID: id, EnvironmentID: eid, CreatedAt: now})
	}
	err = s.aud.PlatformChange(ctx, func(ctx context.Context, tx *audit.Tx) error {
		return s.provision(ctx, tx, &t, envs, hosts)
	})
	if err != nil {
		return domain.Tenant{}, nil, err //nolint:wrapcheck // *errs.Error from the audited change
	}
	s.cache.clear()
	return t, envs, nil
}

// provision writes a new tenant, its environments and hosts, creates the
// tenant's key-encryption key and records the events.
func (s *Service) provision(ctx context.Context, tx *audit.Tx, t *domain.Tenant, envs []domain.Environment, hosts []domain.Host) error {
	if !t.PartnerID.IsZero() {
		depth, err := s.repo.PartnerDepth(ctx, tx, t.PartnerID)
		if err != nil {
			return err //nolint:wrapcheck // repository errors are *errs.Error
		}
		if depth == 0 {
			return fieldError("/partner_id", "not_found", "the partner does not exist")
		}
	}
	if err := s.repo.InsertTenant(ctx, tx, *t); err != nil {
		return err //nolint:wrapcheck // repository errors are *errs.Error
	}
	for _, e := range envs {
		if err := s.repo.InsertEnvironment(ctx, tx, e); err != nil {
			return err //nolint:wrapcheck // repository errors are *errs.Error
		}
	}
	for _, h := range hosts {
		if err := s.repo.InsertHost(ctx, tx, h); err != nil {
			return err //nolint:wrapcheck // repository errors are *errs.Error
		}
	}
	ref, keyRows, err := s.keys.CreateTenantKEK(ctx, tx, t.ID)
	if err != nil {
		return err //nolint:wrapcheck // already an *errs.Error or a KMS error
	}
	t.KEKRef = string(ref)
	if err := s.repo.SetKEKRef(ctx, tx, t.ID, t.KEKRef); err != nil {
		return err //nolint:wrapcheck // repository errors are *errs.Error
	}
	return recordProvisioned(tx, *t, keyRows, envs, hosts)
}

// recordProvisioned records the creation of a tenant, its environments and
// its hosts.
func recordProvisioned(tx *audit.Tx, t domain.Tenant, keyRows []keystore.RowRef, envs []domain.Environment, hosts []domain.Host) error {
	if err := tx.Record(audit.Record{
		Type: TypeTenantCreated, Target: tenantTarget(t), After: t, AfterVersion: t.Version,
		Rows: append([]audit.RowRef{rowRef("tenants", t.ID.UUID().String())}, rowRefs(keyRows)...),
	}); err != nil {
		return err //nolint:wrapcheck // an *errs.Error
	}
	for _, e := range envs {
		if err := tx.Record(audit.Record{
			Type: TypeEnvironmentCreated, Target: &audit.Target{Type: "environment", ID: e.ID.String(), Name: e.Name},
			After: e, AfterVersion: e.Version, Rows: []audit.RowRef{rowRef("environments", t.ID.UUID().String(), e.ID.UUID().String())},
		}); err != nil {
			return err //nolint:wrapcheck // audit and keystore errors are *errs.Error
		}
	}
	for _, h := range hosts {
		if err := tx.Record(hostRecord(TypeTenantHostAdded, h, nil, h)); err != nil {
			return err //nolint:wrapcheck // audit and keystore errors are *errs.Error
		}
	}
	return nil
}

func hostRecord(typ string, h domain.Host, before, after any) audit.Record {
	return audit.Record{
		Type: typ, Target: &audit.Target{Type: "tenant_host", ID: h.Host}, Before: before, After: after,
		Details: map[string]any{DetailHost: h.Host}, Rows: []audit.RowRef{rowRef("hosts", h.Host)},
	}
}

// GetTenant returns a tenant; a deleted tenant is errs.CodeNotFound.
func (s *Service) GetTenant(ctx context.Context, id ids.TenantID) (domain.Tenant, error) {
	t, err := s.repo.GetTenant(ctx, id)
	if err != nil {
		return domain.Tenant{}, err //nolint:wrapcheck // repository errors are *errs.Error
	}
	if t.Status == domain.TenantDeleted {
		return domain.Tenant{}, errs.New(errs.CodeNotFound, "tenant not found")
	}
	return t, nil
}

// ListEnvironments returns the environments of a tenant; a deleted tenant is
// errs.CodeNotFound.
func (s *Service) ListEnvironments(ctx context.Context, id ids.TenantID) ([]domain.Environment, error) {
	rec, err := s.repo.TenantRecords(ctx, id)
	if err != nil {
		return nil, err //nolint:wrapcheck // repository errors are *errs.Error
	}
	if rec.Tenant.Status == domain.TenantDeleted {
		return nil, errs.New(errs.CodeNotFound, "tenant not found")
	}
	return rec.Environments, nil
}

// ListTenantsInput selects a page of tenants.
type ListTenantsInput struct {
	PartnerID ids.PartnerID
	Status    string
	Page      page.Request
}

// ListTenants returns a page of tenants and the token of the next page ("" on
// the last page). Without a status filter, deleted tenants are not listed.
func (s *Service) ListTenants(ctx context.Context, in ListTenantsInput) ([]domain.Tenant, string, error) {
	switch in.Status {
	case "", domain.TenantActive, domain.TenantSuspended, domain.TenantDeleting, domain.TenantDeleted:
	default:
		return nil, "", fieldError("/status", "invalid", "status must be active, suspended, deleting or deleted")
	}
	binding := "tenants|partner=" + in.PartnerID.String() + "|status=" + in.Status
	p, err := page.Parse(in.Page, binding, 2)
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already an *errs.Error
	}
	rows, err := s.repo.ListTenants(ctx, ListFilter{PartnerID: in.PartnerID, Status: in.Status, After: p.After, Limit: p.Fetch()})
	if err != nil {
		return nil, "", err //nolint:wrapcheck // repository errors are *errs.Error
	}
	rows, next := page.Trim(p, rows, binding, func(t domain.Tenant) []string {
		return []string{t.CreatedAt.Format(time.RFC3339Nano), t.ID.UUID().String()}
	})
	return rows, next, nil
}

// lockTenant locks the tenant and checks that expectedVersion is current and
// that action is allowed.
func (s *Service) lockTenant(ctx context.Context, tx *audit.Tx, id ids.TenantID, expectedVersion int64, action domain.Action) (domain.Tenant, error) {
	t, err := s.repo.LockTenant(ctx, tx, id)
	if err != nil {
		return domain.Tenant{}, err //nolint:wrapcheck // repository errors are *errs.Error
	}
	if t.Status == domain.TenantDeleted {
		return domain.Tenant{}, errs.New(errs.CodeNotFound, "tenant not found")
	}
	if t.Version != expectedVersion {
		return domain.Tenant{}, errs.New(errs.CodePreconditionFailed, "the tenant has changed (current version "+strconv.FormatInt(t.Version, 10)+")")
	}
	if err := domain.CheckAction(t.Status, action); err != nil {
		return domain.Tenant{}, err //nolint:wrapcheck // already an *errs.Error
	}
	return t, nil
}

// UpdateTenantInput holds the fields to change; nil fields stay as they are.
type UpdateTenantInput struct {
	Name *string
	Plan *string
}

// UpdateTenant changes a tenant's name or plan.
func (s *Service) UpdateTenant(ctx context.Context, id ids.TenantID, expectedVersion int64, in UpdateTenantInput) (domain.Tenant, error) {
	if err := domain.ValidateUpdate(in.Name, in.Plan); err != nil {
		return domain.Tenant{}, err //nolint:wrapcheck // already an *errs.Error
	}
	return s.change(ctx, id, expectedVersion, domain.ActionUpdate, TypeTenantUpdated, "", func(t *domain.Tenant) {
		if in.Name != nil {
			t.Name = *in.Name
		}
		if in.Plan != nil {
			t.Plan = *in.Plan
		}
	})
}

// SuspendTenant suspends an active tenant; its hosts stop resolving.
func (s *Service) SuspendTenant(ctx context.Context, id ids.TenantID, expectedVersion int64, reason string) (domain.Tenant, error) {
	return s.reasoned(ctx, id, expectedVersion, reason, domain.ActionSuspend, TypeTenantSuspended, domain.TenantSuspended)
}

// ResumeTenant resumes a suspended tenant.
func (s *Service) ResumeTenant(ctx context.Context, id ids.TenantID, expectedVersion int64, reason string) (domain.Tenant, error) {
	return s.reasoned(ctx, id, expectedVersion, reason, domain.ActionResume, TypeTenantResumed, domain.TenantActive)
}

func (s *Service) reasoned(ctx context.Context, id ids.TenantID, expectedVersion int64, reason string, action domain.Action, typ, status string) (domain.Tenant, error) {
	if err := domain.ValidateReason(reason); err != nil {
		return domain.Tenant{}, err //nolint:wrapcheck // already an *errs.Error
	}
	return s.change(ctx, id, expectedVersion, action, typ, reason, func(t *domain.Tenant) { t.Status = status })
}

// change applies edit to the locked tenant, bumps its version and records
// the event.
func (s *Service) change(ctx context.Context, id ids.TenantID, expectedVersion int64, action domain.Action, typ, reason string, edit func(*domain.Tenant)) (domain.Tenant, error) {
	var after domain.Tenant
	err := s.aud.PlatformChange(ctx, func(ctx context.Context, tx *audit.Tx) error {
		before, err := s.lockTenant(ctx, tx, id, expectedVersion, action)
		if err != nil {
			return err
		}
		after = before
		edit(&after)
		after.Version, after.UpdatedAt = before.Version+1, s.now()
		if err := s.repo.UpdateTenant(ctx, tx, after); err != nil {
			return err //nolint:wrapcheck // repository errors are *errs.Error
		}
		rec := audit.Record{
			Type: typ, Target: tenantTarget(after), Before: before, After: after,
			BeforeVersion: before.Version, AfterVersion: after.Version,
			Rows: []audit.RowRef{rowRef("tenants", id.UUID().String())},
		}
		if reason != "" {
			rec.Details = map[string]any{DetailReason: reason}
		}
		return tx.Record(rec)
	})
	if err != nil {
		return domain.Tenant{}, err //nolint:wrapcheck // *errs.Error from the audited change
	}
	s.cache.clear()
	return after, nil
}

// DeleteTenant deletes a tenant (crypto-shredding): its hosts are removed
// and its key-encryption key is destroyed, so every data key of the tenant,
// and with it all personal data encrypted under them, becomes unreadable.
// The KMS destruction is the last step of the transaction and cannot be
// rolled back.
func (s *Service) DeleteTenant(ctx context.Context, id ids.TenantID, expectedVersion int64, reason string) (domain.Tenant, error) {
	if err := domain.ValidateReason(reason); err != nil {
		return domain.Tenant{}, err //nolint:wrapcheck // already an *errs.Error
	}
	var after domain.Tenant
	err := s.aud.PlatformChange(ctx, func(ctx context.Context, tx *audit.Tx) error {
		before, err := s.lockTenant(ctx, tx, id, expectedVersion, domain.ActionDelete)
		if err != nil {
			return err
		}
		after = before
		after.Status = domain.TenantDeleting
		if err := s.repo.UpdateTenant(ctx, tx, after); err != nil {
			return err //nolint:wrapcheck // repository errors are *errs.Error
		}
		hosts, err := s.repo.DeleteHostsOf(ctx, tx, id)
		if err != nil {
			return err //nolint:wrapcheck // repository errors are *errs.Error
		}
		for _, h := range hosts {
			if err := tx.Record(hostRecord(TypeTenantHostRemoved, h, h, nil)); err != nil {
				return err //nolint:wrapcheck // audit and keystore errors are *errs.Error
			}
		}
		now := s.now()
		after.Status, after.DeletedAt, after.Version, after.UpdatedAt = domain.TenantDeleted, &now, before.Version+1, now
		if err := s.repo.UpdateTenant(ctx, tx, after); err != nil {
			return err //nolint:wrapcheck // repository errors are *errs.Error
		}
		keyRows, err := s.keys.DestroyTenantKEK(ctx, tx, id)
		if err != nil {
			return err //nolint:wrapcheck // already an *errs.Error or a KMS error
		}
		return tx.Record(audit.Record{
			Type: TypeTenantDeleted, Target: tenantTarget(after), Before: before, After: after,
			BeforeVersion: before.Version, AfterVersion: after.Version,
			Details: map[string]any{DetailReason: reason},
			Rows:    append([]audit.RowRef{rowRef("tenants", id.UUID().String())}, rowRefs(keyRows)...),
		})
	})
	if err != nil {
		return domain.Tenant{}, err //nolint:wrapcheck // *errs.Error from the audited change
	}
	s.cache.clear()
	return after, nil
}

// AddHost registers host for a tenant environment.
func (s *Service) AddHost(ctx context.Context, tenant ids.TenantID, env ids.EnvironmentID, host string) error {
	h, err := domain.ValidateHost(host)
	if err != nil {
		return err //nolint:wrapcheck // already an *errs.Error
	}
	rec := domain.Host{Host: h, TenantID: tenant, EnvironmentID: env, CreatedAt: s.now()}
	err = s.aud.PlatformChange(ctx, func(ctx context.Context, tx *audit.Tx) error {
		t, err := s.repo.LockTenant(ctx, tx, tenant)
		if err != nil {
			return err //nolint:wrapcheck // repository errors are *errs.Error
		}
		if t.Status != domain.TenantActive && t.Status != domain.TenantSuspended {
			return errs.New(errs.CodeNotFound, "tenant not found")
		}
		if err := s.repo.EnvironmentExists(ctx, tx, tenant, env); err != nil {
			return err //nolint:wrapcheck // repository errors are *errs.Error
		}
		if err := s.repo.InsertHost(ctx, tx, rec); err != nil {
			return err //nolint:wrapcheck // repository errors are *errs.Error
		}
		return tx.Record(hostRecord(TypeTenantHostAdded, rec, nil, rec))
	})
	if err != nil {
		return err //nolint:wrapcheck // *errs.Error from the audited change
	}
	s.cache.clear()
	return nil
}

// RemoveHost unregisters host. An unknown host is errs.CodeNotFound.
func (s *Service) RemoveHost(ctx context.Context, host string) error {
	h, err := domain.NormalizeHost(host)
	if err != nil {
		return err //nolint:wrapcheck // already an *errs.Error
	}
	err = s.aud.PlatformChange(ctx, func(ctx context.Context, tx *audit.Tx) error {
		removed, err := s.repo.DeleteHost(ctx, tx, h)
		if err != nil {
			return err //nolint:wrapcheck // repository errors are *errs.Error
		}
		return tx.Record(hostRecord(TypeTenantHostRemoved, removed, removed, nil))
	})
	if err != nil {
		return err //nolint:wrapcheck // *errs.Error from the audited change
	}
	s.cache.clear()
	return nil
}

// ResolveHost returns the environment and tenant a host belongs to. Results
// of active tenants are cached for a short time (and dropped on every local
// change; another instance's change is seen after the time-to-live). A
// suspended tenant fails with errs.CodePermissionDenied, an unknown host or
// a deleted tenant with errs.CodeNotFound.
func (s *Service) ResolveHost(ctx context.Context, host string) (domain.Host, domain.Tenant, error) {
	h, err := domain.NormalizeHost(host)
	if err != nil {
		return domain.Host{}, domain.Tenant{}, err //nolint:wrapcheck // already an *errs.Error
	}
	if e, ok := s.cache.get(h, s.clock()); ok {
		return e.target, e.tenant, nil
	}
	gen := s.cache.generation()
	target, t, err := s.repo.ResolveHost(ctx, h)
	if err != nil {
		return domain.Host{}, domain.Tenant{}, err //nolint:wrapcheck // repository errors are *errs.Error
	}
	switch t.Status {
	case domain.TenantActive:
		s.cache.put(gen, h, target, t, s.clock())
		return target, t, nil
	case domain.TenantSuspended:
		return domain.Host{}, domain.Tenant{}, errs.New(errs.CodePermissionDenied, "tenant is suspended")
	default:
		return domain.Host{}, domain.Tenant{}, errs.New(errs.CodeNotFound, "host not found")
	}
}
