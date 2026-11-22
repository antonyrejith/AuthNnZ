package config

import (
	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/config/internal/app"
)

// Audit event types of the module (ADR-0008).
const (
	// EventDraftSaved: a draft was created or updated.
	EventDraftSaved = app.EventDraftSaved
	// EventVersionPublished: an object's draft became the published version.
	EventVersionPublished = app.EventVersionPublished
	// EventRolledBack: an object was rolled back to an earlier spec.
	EventRolledBack = app.EventRolledBack
	// EventBundlePublished: an environment's bundle was compiled and stored.
	EventBundlePublished = app.EventBundlePublished
	// EventSettingChanged: a setting was saved at the tenant or environment level.
	EventSettingChanged = app.EventSettingChanged
	// EventSettingsReset: settings were reset to default or parent.
	EventSettingsReset = app.EventSettingsReset
	// EventPlatformSettingChanged: the platform's value or policy changed.
	EventPlatformSettingChanged = app.EventPlatformSettingChanged
)

// ocsfAPIActivity is the OCSF class of configuration changes (API Activity).
const ocsfAPIActivity = 6003

// RegisterEvents registers the module's audit event types.
func RegisterEvents(r *audit.Registry) error {
	plain := func(keys ...string) map[string]audit.FieldClass {
		m := make(map[string]audit.FieldClass, len(keys))
		for _, k := range keys {
			m[k] = audit.FieldPlain
		}
		return m
	}
	for _, t := range []audit.EventType{
		{Name: EventDraftSaved, Details: plain("note")},
		{Name: EventVersionPublished, Details: plain("note")},
		{Name: EventRolledBack, Details: plain("note", "to_version")},
		{Name: EventBundlePublished, Details: plain("note", "bundle_version")},
		{Name: EventSettingChanged, Details: plain("mode")},
		{Name: EventSettingsReset, Details: plain("keys", "mode")},
		{Name: EventPlatformSettingChanged},
	} {
		t.Category, t.OCSFClassUID = audit.CategoryConfiguration, ocsfAPIActivity
		if err := r.Register(t); err != nil {
			return err //nolint:wrapcheck // already an *errs.Error
		}
	}
	return nil
}
