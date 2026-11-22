package app

// Audit event types written by the module (registered by the facade).
const (
	TypePartnerCreated       = "partner.created"
	TypeTenantCreated        = "tenant.created"
	TypeTenantUpdated        = "tenant.updated"
	TypeTenantSuspended      = "tenant.suspended"
	TypeTenantResumed        = "tenant.resumed"
	TypeTenantDeleted        = "tenant.deleted"
	TypeTenantExportRequest  = "tenant.export_requested"
	TypeTenantExportComplete = "tenant.export_completed"
	TypeEnvironmentCreated   = "environment.created"
	TypeTenantHostAdded      = "tenant_host.added"
	TypeTenantHostRemoved    = "tenant_host.removed"
)

// Detail keys declared by the event types.
const (
	DetailReason = "reason"
	DetailStatus = "status"
	DetailHost   = "host"
)
