package tenancy

import (
	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/tenancy/internal/app"
)

// Event types written by the module. All belong to audit.CategoryPlatform
// and are recorded on the platform chain.
const (
	TypePartnerCreated       = app.TypePartnerCreated
	TypeTenantCreated        = app.TypeTenantCreated
	TypeTenantUpdated        = app.TypeTenantUpdated
	TypeTenantSuspended      = app.TypeTenantSuspended
	TypeTenantResumed        = app.TypeTenantResumed
	TypeTenantDeleted        = app.TypeTenantDeleted
	TypeTenantExportRequest  = app.TypeTenantExportRequest
	TypeTenantExportComplete = app.TypeTenantExportComplete
	TypeEnvironmentCreated   = app.TypeEnvironmentCreated
	TypeTenantHostAdded      = app.TypeTenantHostAdded
	TypeTenantHostRemoved    = app.TypeTenantHostRemoved
)

// ocsfEntityManagement is the OCSF class_uid of Entity Management, which
// fits the creation, change and deletion of partners, tenants, environments
// and hosts.
const ocsfEntityManagement = 3004

// RegisterEvents registers the module's event types. Call it once, before
// the Auditor starts recording.
func RegisterEvents(r *audit.Registry) error {
	reason := map[string]audit.FieldClass{app.DetailReason: audit.FieldPlain}
	status := map[string]audit.FieldClass{app.DetailStatus: audit.FieldPlain}
	host := map[string]audit.FieldClass{app.DetailHost: audit.FieldPlain}
	for _, t := range []audit.EventType{
		{Name: TypePartnerCreated},
		{Name: TypeTenantCreated},
		{Name: TypeTenantUpdated},
		{Name: TypeTenantSuspended, Details: reason},
		{Name: TypeTenantResumed, Details: reason},
		{Name: TypeTenantDeleted, Details: reason},
		{Name: TypeTenantExportRequest, Details: status},
		{Name: TypeTenantExportComplete, Details: status},
		{Name: TypeEnvironmentCreated},
		{Name: TypeTenantHostAdded, Details: host},
		{Name: TypeTenantHostRemoved, Details: host},
	} {
		t.Category, t.OCSFClassUID = audit.CategoryPlatform, ocsfEntityManagement
		if err := r.Register(t); err != nil {
			return err //nolint:wrapcheck // already an *errs.Error
		}
	}
	return nil
}
