// Package config is the configuration module's facade (ADR-0003): versioned
// configuration objects (draft, validate, publish, rollback; DYN-01) whose
// specs are checked against a JSON Schema per kind and whose CEL expressions
// are type-checked at publish (DYN-05); settings that inherit along the
// hierarchy platform → tenant → environment with modes, override policies
// and merge rules (TEN-06); and the compiled, immutable runtime bundles the
// data plane reads (DYN-01).
//
// # Writes
//
// Every write goes through audit.Auditor.Change in the tenant scope of the
// context, so each change is recorded in the same transaction and every row
// it writes is declared to the reconciler. A write needs the tenant scope
// (scope.WithTenant); object, publish and bundle calls also need its
// environment.
//
// # Settings and bundles
//
// Settings are saved at the tenant or environment level (SetSetting) and the
// platform sets the baseline and the override policy (SetPlatformSetting).
// EffectiveSettings resolves them live. A bundle is a snapshot: a setting
// change reaches the data plane when its environment is next published
// (Publish with no objects publishes just the settings).
//
// # Registries
//
// Kinds and settings are registered in a Registry value passed to New; there
// are no globals.
package config

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/config/internal/adapters/postgres"
	"github.com/antonyrejith/portun/internal/modules/config/internal/app"
	"github.com/antonyrejith/portun/internal/modules/config/internal/domain"
	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/db/page"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

// Hierarchy, settings and registry types.
type (
	// Level is a level of the configuration hierarchy.
	Level = domain.Level
	// Mode says how a level obtains a setting's value.
	Mode = domain.Mode
	// Policy is a setting's override policy.
	Policy = domain.Policy
	// MergeRule says how values combine along the hierarchy.
	MergeRule = domain.MergeRule
	// Order says which end of a number line is stricter.
	Order = domain.Order
	// Setting is the definition of a setting, registered with RegisterSetting.
	Setting = domain.Setting
	// SettingValue is what one level saved for a setting.
	SettingValue = domain.SettingRow
	// EffectiveSetting is the resolved state of a setting at a level.
	EffectiveSetting = app.EffectiveSetting
	// Kind is the definition of a kind of configuration object.
	Kind = domain.Kind
	// CELField declares a CEL expression inside a kind's specs.
	CELField = domain.CELField
	// CELVar declares a variable of a CEL expression.
	CELVar = domain.CELVar
	// CELType is the type of a CEL variable or result.
	CELType = domain.CELType
	// Registry holds the registered kinds and settings.
	Registry = app.Registry
)

// Objects, versions and publications.
type (
	// Object is a configuration object of one environment.
	Object = domain.Object
	// Version is one version of an object's spec.
	Version = domain.Version
	// Publication records one publish of an environment.
	Publication = domain.Publication
	// PublishedObject is an object version made current by a publish.
	PublishedObject = domain.PublishedObject
	// ValidationReport is the result of Validate.
	ValidationReport = app.ValidationReport
	// Issue is one problem found in a spec.
	Issue = app.Issue
	// Bundle is the immutable runtime form of an environment's configuration.
	Bundle = app.Bundle
)

// Levels.
const (
	LevelPlatform    = domain.LevelPlatform
	LevelTenant      = domain.LevelTenant
	LevelEnvironment = domain.LevelEnvironment
)

// Modes.
const (
	ModeDefault = domain.ModeDefault
	ModeParent  = domain.ModeParent
	ModeOwn     = domain.ModeOwn
)

// Policies.
const (
	PolicyOverridable = domain.PolicyOverridable
	PolicyBounded     = domain.PolicyBounded
	PolicyAdditive    = domain.PolicyAdditive
	PolicyLocked      = domain.PolicyLocked
)

// Merge rules.
const (
	MergeMostSpecific = domain.MergeMostSpecific
	MergeStrictest    = domain.MergeStrictest
	MergeIntersection = domain.MergeIntersection
	MergeUnion        = domain.MergeUnion
	MergeCeiling      = domain.MergeCeiling
)

// Orders.
const (
	LowerIsStricter  = domain.LowerIsStricter
	HigherIsStricter = domain.HigherIsStricter
)

// CEL types.
const (
	CELBool   = domain.CELBool
	CELString = domain.CELString
	CELInt    = domain.CELInt
	CELDouble = domain.CELDouble
	CELList   = domain.CELList
	CELMap    = domain.CELMap
)

// Version statuses.
const (
	VersionDraft     = domain.VersionDraft
	VersionPublished = domain.VersionPublished
)

// NewRegistry returns an empty registry; fill it with RegisterKind and
// RegisterSetting before passing it to New.
func NewRegistry() *Registry { return app.NewRegistry() }

// Deps are the module's dependencies.
type Deps struct {
	// DB runs the transactions. Required.
	DB *db.DB
	// Auditor records every write. Required.
	Auditor *audit.Auditor
	// Bus carries bundle notifications to Run. Optional: without it bundles
	// propagate by polling only.
	Bus *bus.Bus
	// Registry holds the kinds and settings. Required.
	Registry *Registry
	// InstanceID names this process; the CONFIG consumer is
	// "config-<InstanceID>" (a valid bus name). Required.
	InstanceID string
	// Clock returns the current time (default time.Now).
	Clock func() time.Time
	// Logger receives operational logs (default: discard).
	Logger *slog.Logger
	// CacheSize bounds the bundle cache in tenant environments (default 1024).
	CacheSize int
	// PollInterval is how often Run checks for newer bundles (default 10 s).
	PollInterval time.Duration
}

// Module is the configuration module.
type Module struct{ svc *app.Service }

// New returns the module. It fails with errs.CodeInternal when a required
// dependency is missing.
func New(d Deps) (*Module, error) {
	svc, err := app.New(app.Deps{
		DB: d.DB, Auditor: d.Auditor, Bus: d.Bus, Registry: d.Registry, Repo: postgres.Repo{},
		InstanceID: d.InstanceID, Clock: d.Clock, Logger: d.Logger, CacheSize: d.CacheSize, PollInterval: d.PollInterval,
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // already an *errs.Error
	}
	return &Module{svc: svc}, nil
}

// Migrations returns the module's migration set (schema "config").
func Migrations() migrate.Set { return postgres.Migrations() }

// CreateDraft saves a draft of the object kind/name: a new object, or a new
// draft version of an existing one (at most one open draft per object, else
// errs.CodeConflict). The spec must satisfy the kind's JSON Schema, else
// errs.CodeValidationFailed with JSON Pointers under /spec; an unknown kind
// fails at /kind.
func (m *Module) CreateDraft(ctx context.Context, kind, name string, spec json.RawMessage, note string) (Object, Version, error) {
	return m.svc.CreateDraft(ctx, kind, name, spec, note) //nolint:wrapcheck // already an *errs.Error
}

// UpdateDraft replaces the open draft's spec. expectedVersion is the
// object's etag (Object.Version); a stale one fails with
// errs.CodePreconditionFailed.
func (m *Module) UpdateDraft(ctx context.Context, objectID ids.ConfigObjectID, expectedVersion int64, spec json.RawMessage, note string) (Version, error) {
	return m.svc.UpdateDraft(ctx, objectID, expectedVersion, spec, note) //nolint:wrapcheck // already an *errs.Error
}

// Validate checks one version against its kind (JSON Schema and CEL type
// check) and reports every problem. It never writes.
func (m *Module) Validate(ctx context.Context, objectID ids.ConfigObjectID, version int) (ValidationReport, error) {
	return m.svc.Validate(ctx, objectID, version) //nolint:wrapcheck // already an *errs.Error
}

// Publish publishes each object's open draft and compiles the environment's
// bundle in one audited transaction. With no objects it publishes the
// current settings and specs as a new bundle.
func (m *Module) Publish(ctx context.Context, objectIDs []ids.ConfigObjectID, note string) (Publication, error) {
	return m.svc.Publish(ctx, objectIDs, note) //nolint:wrapcheck // already an *errs.Error
}

// Rollback creates a new version copying toVersion's spec, publishes it and
// compiles the bundle.
func (m *Module) Rollback(ctx context.Context, objectID ids.ConfigObjectID, toVersion int, note string) (Publication, error) {
	return m.svc.Rollback(ctx, objectID, toVersion, note) //nolint:wrapcheck // already an *errs.Error
}

// GetObject returns an object of the scope's environment.
func (m *Module) GetObject(ctx context.Context, id ids.ConfigObjectID) (Object, error) {
	return m.svc.GetObject(ctx, id) //nolint:wrapcheck // already an *errs.Error
}

// ListVersions returns the versions of an object, newest first, and the
// token of the next page ("" on the last).
func (m *Module) ListVersions(ctx context.Context, id ids.ConfigObjectID, req page.Request) ([]Version, string, error) {
	return m.svc.ListVersions(ctx, id, req) //nolint:wrapcheck // already an *errs.Error
}

// Diff returns the field-level difference between two versions' specs.
func (m *Module) Diff(ctx context.Context, id ids.ConfigObjectID, from, to int) ([]audit.DiffEntry, error) {
	entries, err := m.svc.Diff(ctx, id, from, to)
	if err != nil {
		return nil, err //nolint:wrapcheck // already an *errs.Error
	}
	out := make([]audit.DiffEntry, len(entries))
	for i, e := range entries {
		out[i] = audit.DiffEntry{Op: e.Op, Path: e.Path, Before: e.Before, After: e.After}
	}
	return out, nil
}

// SetSetting saves a setting's mode (and, for mode own, its value) at the
// tenant or environment level. expectedVersion is the saved row's version (0
// when nothing is saved); negative skips the check. A locked setting fails
// with errs.CodePermissionDenied, a value outside a bounded parent's limits
// with errs.CodeValidationFailed at /value (or /mode for the product default).
func (m *Module) SetSetting(ctx context.Context, level Level, key string, mode Mode, value json.RawMessage, expectedVersion int64) (SettingValue, error) {
	return m.svc.SetSetting(ctx, level, key, mode, value, expectedVersion) //nolint:wrapcheck // already an *errs.Error
}

// ResetSettings returns the settings matching patterns (an exact key,
// "section.*" or "*") to mode default at the level, or to mode parent where
// the override policy forbids default. With dryRun it only previews: the
// effective settings, as they would be afterwards, of those it would change.
func (m *Module) ResetSettings(ctx context.Context, level Level, patterns []string, dryRun bool) ([]EffectiveSetting, error) {
	return m.svc.ResetSettings(ctx, level, patterns, dryRun) //nolint:wrapcheck // already an *errs.Error
}

// EffectiveSettings returns the effective settings of the scope's
// environment (keys nil: all registered settings).
func (m *Module) EffectiveSettings(ctx context.Context, keys []string) ([]EffectiveSetting, error) {
	return m.svc.EffectiveSettings(ctx, keys) //nolint:wrapcheck // already an *errs.Error
}

// EffectiveSettingsAt is EffectiveSettings at the tenant or environment
// level; the tenant level needs no environment in the scope.
func (m *Module) EffectiveSettingsAt(ctx context.Context, level Level, keys []string) ([]EffectiveSetting, error) {
	return m.svc.EffectiveSettingsAt(ctx, level, keys) //nolint:wrapcheck // already an *errs.Error
}

// SetPlatformSetting sets the platform's value and override policy of a
// setting, recorded as a platform change.
func (m *Module) SetPlatformSetting(ctx context.Context, key string, value json.RawMessage, policy Policy) error {
	return m.svc.SetPlatformSetting(ctx, key, value, policy) //nolint:wrapcheck // already an *errs.Error
}

// Bundle returns the current bundle of the scope's tenant environment from
// the cache, loading it on first use. An environment that never published
// has a version 0 bundle of its effective settings.
func (m *Module) Bundle(ctx context.Context) (*Bundle, error) {
	return m.svc.Bundle(ctx) //nolint:wrapcheck // already an *errs.Error
}

// Run keeps the cached bundles current until ctx is done: it consumes the
// CONFIG stream (when a Bus is configured) and polls for newer bundles, so a
// bus outage never stops updates. It returns nil when ctx is done.
func (m *Module) Run(ctx context.Context) error {
	return m.svc.Run(ctx) //nolint:wrapcheck // already an *errs.Error
}
