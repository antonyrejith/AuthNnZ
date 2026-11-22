// Package domain holds the tenancy entities and their rules: statuses and
// the allowed lifecycle transitions, validation of the inputs, and the
// normalization of host names. It imports only the standard library,
// platform/ids and platform/errs (ADR-0016).
package domain

import (
	"strings"
	"time"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

// Partner statuses.
const (
	PartnerActive    = "active"
	PartnerSuspended = "suspended"
)

// Tenant statuses.
const (
	TenantActive    = "active"
	TenantSuspended = "suspended"
	TenantDeleting  = "deleting"
	TenantDeleted   = "deleted"
)

// Environment kinds.
const (
	KindDevelopment = "development"
	KindStaging     = "staging"
	KindProduction  = "production"
)

// Operation kinds and statuses.
const (
	OperationExport = "export"

	OperationRunning   = "running"
	OperationSucceeded = "succeeded"
	OperationFailed    = "failed"
)

// MaxPartnerDepth is the deepest partner nesting (plan 15 §1): a top-level
// partner has depth 1.
const MaxPartnerDepth = 3

// Partner is a reseller or MSP that owns tenants. The JSON form is the
// audited value.
type Partner struct {
	ID        ids.PartnerID `json:"id"`
	ParentID  ids.PartnerID `json:"parent_id,omitzero"`
	Name      string        `json:"name"`
	Status    string        `json:"status"`
	Version   int64         `json:"version"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// Tenant is a customer of the platform. The JSON form is the audited value;
// the key-encryption key reference is never part of it.
type Tenant struct {
	ID        ids.TenantID  `json:"id"`
	PartnerID ids.PartnerID `json:"partner_id,omitzero"`
	Slug      string        `json:"slug"`
	Name      string        `json:"name"`
	Status    string        `json:"status"`
	Region    string        `json:"region"`
	Plan      string        `json:"plan"`
	KEKRef    string        `json:"-"`
	Version   int64         `json:"version"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	DeletedAt *time.Time    `json:"deleted_at,omitempty"`
}

// Environment is one of a tenant's environments.
type Environment struct {
	ID        ids.EnvironmentID `json:"id"`
	TenantID  ids.TenantID      `json:"tenant_id"`
	Name      string            `json:"name"`
	Kind      string            `json:"kind"`
	Version   int64             `json:"version"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// Host maps a host name to a tenant environment.
type Host struct {
	Host          string            `json:"host"`
	TenantID      ids.TenantID      `json:"tenant_id"`
	EnvironmentID ids.EnvironmentID `json:"environment_id"`
	CreatedAt     time.Time         `json:"created_at"`
}

// Operation is a long-running operation on a tenant.
type Operation struct {
	ID         ids.OperationID `json:"id"`
	TenantID   ids.TenantID    `json:"tenant_id"`
	Kind       string          `json:"kind"`
	Status     string          `json:"status"`
	ResultPath string          `json:"result_path,omitempty"`
	ErrorCode  errs.Code       `json:"error_code,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// Action is a lifecycle action on a tenant.
type Action string

// Lifecycle actions.
const (
	ActionSuspend Action = "suspend"
	ActionResume  Action = "resume"
	ActionDelete  Action = "delete"
	ActionUpdate  Action = "update"
)

// CheckAction decides whether action may be applied to a tenant in status
// from. Allowed: active to suspended, suspended to active, and active or
// suspended to deleting (and on to deleted). A deleted tenant does not exist
// for the caller (errs.CodeNotFound); any other combination is a conflict.
// Updating the name or plan is allowed while the tenant is active or
// suspended.
func CheckAction(from string, action Action) error {
	if from == TenantDeleted {
		return errs.New(errs.CodeNotFound, "tenant not found")
	}
	ok := false
	switch action {
	case ActionSuspend:
		ok = from == TenantActive
	case ActionResume:
		ok = from == TenantSuspended
	case ActionDelete:
		ok = from == TenantActive || from == TenantSuspended || from == TenantDeleting
	case ActionUpdate:
		ok = from == TenantActive || from == TenantSuspended
	}
	if !ok {
		return errs.New(errs.CodeConflict, "the tenant is "+from+"; it cannot be "+past(action))
	}
	return nil
}

// past returns the past participle of a lifecycle action.
func past(a Action) string {
	if strings.HasSuffix(string(a), "e") {
		return string(a) + "d"
	}
	return string(a) + "ed"
}
