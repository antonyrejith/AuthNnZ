// Package configtest holds the example settings and kinds the config module's
// tests and other modules' tests register, so they exercise real merge rules
// and policies without defining their own.
package configtest

import (
	"encoding/json"

	"github.com/antonyrejith/portun/internal/modules/config"
)

// Example setting keys.
const (
	// SessionIdleTimeout is an int, strictest (lower is stricter), bounded.
	SessionIdleTimeout = "session.idle_timeout_seconds"
	// LoginMethods is an array of strings, intersection, bounded.
	LoginMethods = "login.methods"
	// ProductName is a string, most specific, overridable.
	ProductName = "branding.product_name"
	// CustomRoles is an array of strings, union, additive.
	CustomRoles = "roles.custom"
	// MaxUsers is an int, ceiling, bounded.
	MaxUsers = "quota.max_users"
	// AuditRetentionDays is an int, strictest (higher is stricter), locked at
	// the platform.
	AuditRetentionDays = "security.audit_retention_days"
)

// KindFlow is a kind whose specs hold CEL conditions.
const KindFlow = "test.flow"

// Settings returns the example setting definitions.
func Settings() []config.Setting {
	stringArray := []byte(`{"type":"array","items":{"type":"string","minLength":1}}`)
	return []config.Setting{
		{
			Key: SessionIdleTimeout, Schema: []byte(`{"type":"integer","minimum":60,"maximum":86400}`),
			Default: json.RawMessage(`1800`), Merge: config.MergeStrictest, Order: config.LowerIsStricter, Policy: config.PolicyBounded,
		},
		{
			Key: LoginMethods, Schema: stringArray,
			Default: json.RawMessage(`["password","passkey","email_otp"]`), Merge: config.MergeIntersection, Policy: config.PolicyBounded,
		},
		{
			Key: ProductName, Schema: []byte(`{"type":"string","minLength":1,"maxLength":64}`),
			Default: json.RawMessage(`"Portun"`), Merge: config.MergeMostSpecific, Policy: config.PolicyOverridable,
		},
		{
			Key: CustomRoles, Schema: stringArray,
			Default: json.RawMessage(`[]`), Merge: config.MergeUnion, Policy: config.PolicyAdditive,
		},
		{
			Key: MaxUsers, Schema: []byte(`{"type":"integer","minimum":0}`),
			Default: json.RawMessage(`10000`), Merge: config.MergeCeiling, Policy: config.PolicyBounded,
		},
		{
			Key: AuditRetentionDays, Schema: []byte(`{"type":"integer","minimum":30}`),
			Default: json.RawMessage(`365`), Merge: config.MergeStrictest, Order: config.HigherIsStricter, Policy: config.PolicyLocked,
		},
	}
}

// Kinds returns the example kinds: test.flow, a list of steps whose
// conditions are CEL expressions over the variables user (a map), risk (an
// int) and items (a list).
func Kinds() []config.Kind {
	vars := []config.CELVar{
		{Name: "user", Type: config.CELMap},
		{Name: "risk", Type: config.CELInt},
		{Name: "items", Type: config.CELList},
	}
	return []config.Kind{{
		Name: KindFlow,
		Schema: []byte(`{
			"type": "object",
			"required": ["title", "steps"],
			"additionalProperties": false,
			"properties": {
				"title": {"type": "string", "minLength": 1},
				"greeting": {"type": "string"},
				"steps": {
					"type": "array",
					"minItems": 1,
					"items": {
						"type": "object",
						"required": ["id", "condition"],
						"additionalProperties": false,
						"properties": {
							"id": {"type": "string", "minLength": 1},
							"condition": {"type": "string"}
						}
					}
				}
			}
		}`),
		CELFields: []config.CELField{
			{Pointer: "/steps/*/condition", Type: config.CELBool, Vars: vars},
			{Pointer: "/greeting", Type: config.CELString, Vars: vars},
		},
	}}
}

// Register registers the example settings and kinds in r.
func Register(r *config.Registry) error {
	for _, s := range Settings() {
		if err := r.RegisterSetting(s); err != nil {
			return err //nolint:wrapcheck // already an *errs.Error
		}
	}
	for _, k := range Kinds() {
		if err := r.RegisterKind(k); err != nil {
			return err //nolint:wrapcheck // already an *errs.Error
		}
	}
	return nil
}

// NewRegistry returns a registry holding the examples.
func NewRegistry() (*config.Registry, error) {
	r := config.NewRegistry()
	if err := Register(r); err != nil {
		return nil, err
	}
	return r, nil
}
